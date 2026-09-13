// Package operator connects the upstream scale-set listener to durable VM state.
package operator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/admission"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/state"
	"io"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	CatalogPath         string                     `json:"catalogPath,omitempty"`
	Name                string                     `json:"name"`
	Namespace           string                     `json:"namespace"`
	GitHubURL           string                     `json:"githubURL"`
	ScaleSetID          int                        `json:"scaleSetID"`
	MaxRunners          int                        `json:"maxRunners"`
	ProvisioningSeconds int                        `json:"provisioningSeconds"`
	MaxLifetimeSeconds  int                        `json:"maxLifetimeSeconds"`
	Requirements        placement.Requirements     `json:"requirements"`
	Catalog             placement.Catalog          `json:"catalog"`
	Providers           map[string]provider.Config `json:"providers"`
}

func (c Config) Validate() error {
	if err := provider.ValidateName(c.Name); err != nil {
		return err
	}
	if c.Namespace == "" || c.ScaleSetID < 1 || c.MaxRunners < 1 || c.MaxRunners > 10 || c.ProvisioningSeconds < 1 || c.ProvisioningSeconds > 600 || c.MaxLifetimeSeconds < c.ProvisioningSeconds || c.MaxLifetimeSeconds > 21600 {
		return errors.New("invalid namespace, scale set, capacity or time limits")
	}
	u, e := url.Parse(c.GitHubURL)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" {
		return errors.New("GitHub.com organization or repository HTTPS URL required")
	}
	if err := c.Requirements.Validate(); err != nil {
		return err
	}
	for _, name := range c.Requirements.Providers {
		p, ok := c.Providers[name]
		if !ok {
			return errors.New("missing provider configuration")
		}
		if err := provider.ValidateName(name); err != nil {
			return err
		}
		if p.Owner != c.Name {
			return errors.New("provider owner must match controller name")
		}
		if err := (&provider.Command{Config: p}).Validate(); err != nil {
			return err
		}
	}
	// Price freshness is checked at admission, never used to block cleanup on restart.
	return nil
}

type fleet struct {
	BindingVersion int                             `json:"bindingVersion,omitempty"`
	Condition      string                          `json:"condition,omitempty"`
	Binding        string                          `json:"binding"`
	Released       map[string]bool                 `json:"released"`
	Admission      admission.State                 `json:"admission"`
	Created        map[string]time.Time            `json:"created"`
	Pending        map[string]lifecycle.Allocation `json:"pending"`
}
type Operator struct {
	// Readiness is an optional concurrency-safe observer of session/reconciliation state.
	Readiness  func(bool)
	Config     Config
	Client     kubernetes.Interface
	GitHub     *scaleset.Client
	Store      *state.Kubernetes
	Controller *lifecycle.Controller
	mu         sync.Mutex
	paused     bool
	draining   bool
}

func New(c Config, k kubernetes.Interface, g *scaleset.Client) *Operator {
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps(c.Namespace), Owner: c.Name}
	o := &Operator{Config: c, Client: k, GitHub: g, Store: s}
	providers := map[string]lifecycle.Provider{}
	for name, p := range c.Providers {
		providers[name] = &provider.Command{Config: p, Exec: provider.OSExecutor{}, Bootstrap: func(ctx context.Context, id string) (string, error) {
			if g == nil {
				return "", errors.New("GitHub JIT client unavailable during recovery")
			}
			r, e := g.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: id, WorkFolder: "_work"}, c.ScaleSetID)
			if e != nil {
				return "", errors.New("GitHub JIT request failed")
			}
			return r.EncodedJITConfig, nil
		}}
	}
	o.Controller = &lifecycle.Controller{Store: s, Providers: providers, Now: time.Now}
	return o
}
func (o *Operator) binding() string {
	return o.bindingWithLimit(0)
}

func (o *Operator) bindingWithLimit(limit int) string {
	c := o.Config
	c.MaxRunners = limit
	c.Catalog = placement.Catalog{}
	c.CatalogPath = ""
	b, _ := json.Marshal(c)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (o *Operator) catalog() (placement.Catalog, error) {
	if o.Config.CatalogPath == "" {
		return o.Config.Catalog, nil
	}
	f, e := os.Open(o.Config.CatalogPath)
	if e != nil {
		return placement.Catalog{}, errors.New("catalog unavailable")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	var c placement.Catalog
	if e = d.Decode(&c); e != nil {
		return c, errors.New("invalid catalog")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("invalid catalog trailer")
	}
	return c, nil
}
func (o *Operator) loadFleet(ctx context.Context) (*corev1.ConfigMap, fleet, error) {
	maps := o.Client.CoreV1().ConfigMaps(o.Config.Namespace)
	cm, e := maps.Get(ctx, o.Config.Name+"-fleet", metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: o.Config.Name + "-fleet", Labels: map[string]string{"runnerscout/owner": o.Config.Name}}, Data: map[string]string{"fleet": "{}"}}
		cm, e = maps.Create(ctx, cm, metav1.CreateOptions{})
	}
	if e != nil {
		return nil, fleet{}, e
	}
	if cm.Labels["runnerscout/owner"] != o.Config.Name {
		return nil, fleet{}, errors.New("fleet ownership mismatch")
	}
	var f fleet
	e = json.Unmarshal([]byte(cm.Data["fleet"]), &f)
	if e != nil {
		return nil, f, e
	}
	if f.Binding != "" && f.Binding != o.binding() {
		legacyMatch := false
		if f.BindingVersion == 0 {
			// The old format also hashed the admission limit. Only that bounded
			// field may vary during migration; every ownership field must match.
			for limit := 1; limit <= 10; limit++ {
				if f.Binding == o.bindingWithLimit(limit) {
					legacyMatch = true
					break
				}
			}
		}
		if !legacyMatch {
			return nil, f, errors.New("provider or class binding changed; restore original configuration for cleanup")
		}
	}
	if f.BindingVersion != 0 && f.BindingVersion != 2 {
		return nil, f, errors.New("unsupported fleet binding version")
	}
	f.BindingVersion = 2
	f.Binding = o.binding()
	if f.Released == nil {
		f.Released = map[string]bool{}
	}
	if f.Created == nil {
		f.Created = map[string]time.Time{}
	}
	if f.Pending == nil {
		f.Pending = map[string]lifecycle.Allocation{}
	}
	return cm, f, e
}
func (o *Operator) saveFleet(ctx context.Context, cm *corev1.ConfigMap, f fleet) error {
	b, e := json.Marshal(f)
	if e != nil {
		return e
	}
	cm.Data["fleet"] = string(b)
	_, e = o.Client.CoreV1().ConfigMaps(o.Config.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return e
}
func (o *Operator) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		return 0, e
	}
	allocs, e := o.Store.List(ctx)
	if e != nil {
		return 0, e
	}
	active := 0
	for _, a := range allocs {
		if a.Completed && a.Phase == lifecycle.Deleted && !f.Released[a.ID] {
			if f.Admission.Admitted > 0 {
				f.Admission.Admitted--
			}
			f.Released[a.ID] = true
		}
		if a.Phase != lifecycle.Deleted && a.Phase != lifecycle.TimedOut {
			active++
		}
	}
	// Pending admissions already count against capacity even before their records exist.
	represented := map[string]bool{}
	for _, a := range allocs {
		represented[a.ID] = true
	}
	for id := range f.Pending {
		if !represented[id] {
			active++
		}
	}
	if o.paused || o.draining {
		f.Condition = "AdmissionsSuspended"
		if o.draining {
			f.Condition = "ScaleSetDeleting"
		}
		return active, o.saveFleet(ctx, cm, f)
	}
	n, e := f.Admission.Reconcile(count, active, o.Config.MaxRunners, active == 0)
	if e != nil {
		return active, e
	}
	if n > 0 && len(f.Created)+n > 1000 {
		return active, errors.New("retained allocation limit reached; operator maintenance required")
	}
	refuse := func(reason string) (int, error) {
		f.Admission.Admitted -= n
		f.Condition = reason
		return active, o.saveFleet(ctx, cm, f)
	}
	catalog, e := o.catalog()
	if e != nil && n > 0 {
		return refuse("CatalogUnavailable")
	}
	if n > 0 {
		if _, e = placement.Choose(time.Now(), o.Config.Requirements, catalog, nil); e != nil {
			return refuse("CatalogNotAdmissible")
		}
	}
	f.Condition = "DemandObserved"
	for range n {
		id := "rs-" + uuid.NewString()
		now := time.Now()
		f.Created[id] = now
		f.Pending[id] = lifecycle.Allocation{ID: id, Phase: lifecycle.Pending, Deadline: now.Add(time.Duration(o.Config.ProvisioningSeconds) * time.Second), MaxAttempts: 3, Catalog: catalog, Requirements: o.Config.Requirements}
	}
	if e = o.saveFleet(ctx, cm, f); e != nil {
		return active, e
	}
	return active + n, nil
}
func (o *Operator) HandleJobStarted(ctx context.Context, j *scaleset.JobStarted) error {
	// RunnerName is the resource identity. The scale-set JobID remains opaque.
	if j == nil {
		return errors.New("nil job start")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	a, e := o.Store.Load(ctx, j.RunnerName)
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	a.Ready = true
	_, e = o.Store.Save(ctx, a, a.Revision)
	return e
}
func (o *Operator) HandleJobCompleted(ctx context.Context, j *scaleset.JobCompleted) error {
	if j == nil {
		return errors.New("nil job completion")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	a, e := o.Store.Load(ctx, j.RunnerName)
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	a.Retire = true
	a.Completed = true
	_, e = o.Store.Save(ctx, a, a.Revision)
	return e
}
func (o *Operator) Tick(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		return e
	}
	for id, a := range f.Pending {
		if o.draining {
			a.Retire = true
		}
		_, e = o.Store.Save(ctx, a, "")
		if e != nil && !errors.Is(e, lifecycle.ErrConflict) {
			return e
		}
		delete(f.Pending, id)
	}
	if e = o.saveFleet(ctx, cm, f); e != nil {
		return e
	}
	allocs, e := o.Store.List(ctx)
	if e != nil {
		return e
	}
	o.Controller.Cooldowns = map[string]time.Time{}
	for _, a := range allocs {
		for pool, at := range a.RejectedAt {
			until := at.Add(5 * time.Minute)
			if until.After(o.Controller.Cooldowns[pool]) {
				o.Controller.Cooldowns[pool] = until
			}
		}
	}
	var failures []error
	for _, a := range allocs {
		created, ok := f.Created[a.ID]
		if !ok {
			return errors.New("allocation has no durable lifetime origin")
		}
		if !a.Retire && (o.draining || !time.Now().Before(created.Add(time.Duration(o.Config.MaxLifetimeSeconds)*time.Second))) {
			a.Retire = true
			if _, e = o.Store.Save(ctx, a, a.Revision); e != nil {
				failures = append(failures, e)
				continue
			}
		}
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		e = o.Controller.Step(call, a.ID)
		cancel()
		if updated, loadErr := o.Store.Load(ctx, a.ID); loadErr == nil {
			for pool, at := range updated.RejectedAt {
				until := at.Add(5 * time.Minute)
				if until.After(o.Controller.Cooldowns[pool]) {
					o.Controller.Cooldowns[pool] = until
				}
			}
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (o *Operator) setReady(ready bool) {
	if o.Readiness != nil {
		o.Readiness(ready)
	}
}

func (o *Operator) runLeader(ctx context.Context) error {
	o.setReady(false)
	defer o.setReady(false)
	// Cloud cleanup is attempted even if GitHub lookup/session establishment fails.
	startupErr := o.Tick(ctx)
	if startupErr != nil {
		slog.Warn("startup reconciliation incomplete; obligations retained")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ss, e := o.GitHub.GetRunnerScaleSetByID(ctx, o.Config.ScaleSetID)
	if e != nil {
		return errors.New("scale-set lookup failed")
	}
	if ss.Name != o.Config.Name {
		return errors.New("scale-set name mismatch")
	}
	host, _ := os.Hostname()
	session, e := o.GitHub.MessageSessionClient(ctx, o.Config.ScaleSetID, host+"-"+uuid.NewString())
	if e != nil {
		return errors.New("scale-set session failed")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = session.Close(closeCtx)
	}()
	l, e := listener.New(session, listener.Config{ScaleSetID: o.Config.ScaleSetID, MaxRunners: o.Config.MaxRunners})
	if e != nil {
		return e
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- l.Run(runCtx, o)
	}()
	defer func() {
		cancel()
		<-done
	}()
	o.setReady(startupErr == nil)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-done:
			return e
		case <-tick.C:
			if e = o.Tick(runCtx); e != nil {
				slog.Warn("reconciliation incomplete; durable obligations retained")
			}
			o.setReady(e == nil)
		}
	}
}
func (o *Operator) Run(ctx context.Context) error {
	return WithLease(ctx, o.Client, o.Config.Namespace, o.Config.Name, o.runLeader)
}

// RunSession serves a worker under an already-held scale-set Lease. The caller
// must stop and join this worker before releasing that Lease or its credentials.
func (o *Operator) RunSession(ctx context.Context) error { return o.runLeader(ctx) }

var _ listener.Scaler = (*Operator)(nil)
