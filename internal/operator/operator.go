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
	"github.com/tsouza/runnerscout/internal/recovery"
	"github.com/tsouza/runnerscout/internal/state"
	"github.com/tsouza/runnerscout/internal/wireguard"
	"io"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"log/slog"
	"net/netip"
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
	Retry               recovery.Policy            `json:"retry"`
	// AWSPriceRefresh opts into live EC2 spot price observation on every
	// admission cycle, using the "aws" entry in Providers for credentials.
	// It defaults to false so existing deployments never start making live
	// AWS API calls without an explicit choice to do so.
	AWSPriceRefresh bool `json:"awsPriceRefresh,omitempty"`
	// AzurePriceRefresh opts into live Azure Retail Prices API spot price
	// observation on every admission cycle, using the "azure" entry in
	// Providers only to confirm Azure is actually a configured provider -
	// the Retail Prices API itself is public and unauthenticated, so no
	// credentials are read from that entry for this call. It defaults to
	// false so existing deployments never start making live Azure API
	// calls without an explicit choice to do so.
	AzurePriceRefresh bool `json:"azurePriceRefresh,omitempty"`
	// GCPPriceRefresh opts into live Cloud Billing Catalog API price
	// observation on every admission cycle, using the "gcp" entry in
	// Providers for its billing API key. Unlike AWSPriceRefresh/
	// AzurePriceRefresh, enabling this alone refreshes nothing: it only
	// permits refreshGCPPrices to observe offerings that also carry their
	// own placement.Offering.GCPSkuRefs (see docs/prices-gcp.md) - there is
	// no generic GCP live-price discovery this flag could opt every GCP
	// offering into (see docs/prices-gcp.background.md). It defaults to
	// false so existing deployments never start making live Cloud Billing
	// API calls without an explicit choice to do so, exactly like
	// AWSPriceRefresh/AzurePriceRefresh above.
	GCPPriceRefresh bool `json:"gcpPriceRefresh,omitempty"`
	// AzureInterruptionQueueURL opts into live Azure Storage Queue polling
	// for spot interruption delivery on every Tick cycle, using the "azure"
	// entry in Providers for credentials (internal/provider.AzureSDK's own
	// already-resolved credential chain, reused rather than separately
	// scoped - see internal/provider.AzureSDK.InterruptionQueue's doc
	// comment for why). It is the full Storage Queue endpoint Event Grid
	// delivers Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated
	// messages to, e.g. "https://<account>.queue.core.windows.net/<queue>"
	// (see docs/azure-interruption-delivery.md). It defaults to "" so
	// existing deployments never start making live Azure Storage Queue
	// calls without an explicit choice to do so - exactly the same opt-in
	// default shape as AWSPriceRefresh/AzurePriceRefresh above.
	AzureInterruptionQueueURL string `json:"azureInterruptionQueueURL,omitempty"`
	// NetworkProfile names the api/v1alpha1.NetworkProfile this scale set's
	// allocations join, as compiled by internal/configapi/compile.go's
	// network(). It is only ever non-empty for a "wireguard" mode profile -
	// "separate" mode, and a scale set with no NetworkProfile at all, both
	// compile to "" - matching lifecycle.Allocation.NetworkProfile's own doc
	// comment ("only ever non-empty for a wireguard mode NetworkProfile").
	// HandleDesiredRunnerCount copies this value onto every newly created
	// Allocation; it is otherwise inert (no provider, poll endpoint, or
	// peer-snapshot code path is affected by a "" value). omitempty keeps
	// every existing deployment's fleet ConfigMap binding hash (see
	// bindingWithLimit) unchanged, exactly like AzureInterruptionQueueURL
	// above.
	NetworkProfile string `json:"networkProfile,omitempty"`
	// NetworkOverlayCIDRs is the wireguard-mode NetworkProfile's single
	// NetworkMapping's own CIDRs (api/v1alpha1.NetworkMapping.CIDRs, as
	// compiled by internal/configapi/compile.go's network()) - the address
	// pool HandleDesiredRunnerCount allocates each new allocation's
	// lifecycle.Allocation.WireGuardOverlayAddress from. It is only ever
	// non-empty when NetworkProfile is also non-empty, and is always empty
	// for "separate" mode or no NetworkProfile at all, matching
	// NetworkProfile's own emptiness rule exactly. omitempty keeps every
	// existing deployment's fleet ConfigMap binding hash (see
	// bindingWithLimit) unchanged, exactly like NetworkProfile above.
	NetworkOverlayCIDRs []string `json:"networkOverlayCIDRs,omitempty"`
	// BudgetDailyMicros is a worst-case daily spend ceiling in USD micros, or
	// 0 for unbounded (the default, matching every existing deployment's
	// binding hash unchanged - see bindingWithLimit). Enforced independently
	// per scale set from that scale set's own admitted allocations; a
	// CapacityBudget (or mounted-config value) referenced by more than one
	// scale set is not a shared pool - each scale set enforces the same
	// ceiling against only its own spend.
	BudgetDailyMicros int64 `json:"budgetDailyMicros,omitempty"`
}

func (c Config) Validate() error {
	if err := provider.ValidateName(c.Name); err != nil {
		return err
	}
	if c.NetworkProfile != "" && len(validation.IsDNS1123Label(c.NetworkProfile)) != 0 {
		return errors.New("invalid network profile name")
	}
	if c.NetworkProfile == "" && len(c.NetworkOverlayCIDRs) != 0 {
		return errors.New("overlay CIDRs require a wireguard network profile")
	}
	if c.NetworkProfile != "" {
		if len(c.NetworkOverlayCIDRs) == 0 {
			return errors.New("wireguard network profile requires overlay CIDRs")
		}
		for _, cidr := range c.NetworkOverlayCIDRs {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil || prefix != prefix.Masked() {
				return errors.New("invalid overlay CIDR")
			}
		}
	}
	if c.Namespace == "" || c.ScaleSetID < 1 || c.MaxRunners < 1 || c.MaxRunners > 10 || c.ProvisioningSeconds < 1 || c.ProvisioningSeconds > 600 || c.MaxLifetimeSeconds < c.ProvisioningSeconds || c.MaxLifetimeSeconds > 21600 {
		return errors.New("invalid namespace, scale set, capacity or time limits")
	}
	if c.BudgetDailyMicros < 0 {
		return errors.New("budget ceiling cannot be negative")
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
	BindingVersion int                  `json:"bindingVersion,omitempty"`
	Condition      string               `json:"condition,omitempty"`
	Binding        string               `json:"binding"`
	Released       map[string]bool      `json:"released"`
	Admission      admission.State      `json:"admission"`
	Created        map[string]time.Time `json:"created"`
	// Reserved is a worst-case reservation in USD micros per allocation ID,
	// keyed and populated at the same moment as Created (see
	// HandleDesiredRunnerCount). Entries are never removed, matching
	// Created's own lifetime - spentToday sums these, filtered by Created's
	// date and each allocation's current Phase.
	Reserved map[string]int64                `json:"reserved,omitempty"`
	Pending  map[string]lifecycle.Allocation `json:"pending"`
	// RetriesUsedByRun is keyed by GitHub workflow run ID, not allocation ID -
	// a rerun keeps the same run ID but is reassigned as a brand new,
	// otherwise unrelated allocation.
	RetriesUsedByRun map[int64]int `json:"retriesUsedByRun,omitempty"`
	// PendingReruns is keyed by GitHub workflow run ID for the same reason as
	// RetriesUsedByRun above: an ambiguous RerunFailedJobs outcome must block
	// every allocation sharing that RunID from requesting another rerun,
	// whichever allocation eventually observes and reconciles it. See
	// pendingRerun and processInterruptionRetries in retry.go.
	PendingReruns map[int64]pendingRerun `json:"pendingReruns,omitempty"`
}
type Operator struct {
	// Readiness is an optional concurrency-safe observer of session/reconciliation state.
	Readiness   func(bool)
	Config      Config
	Client      kubernetes.Interface
	GitHub      *scaleset.Client
	GitHubJobs  githubJobsClient
	AWSPrices   awsPriceObserver
	AzurePrices azurePriceObserver
	GCPPrices   gcpPriceObserver
	// AzureInterruptions polls one Azure Storage Queue for confirmed spot
	// preemptions, once per Tick cycle (see pollAzureInterruptions and
	// applyAzureInterruptions in azure_interruptions.go) - never per
	// allocation, since Poll drains every currently-visible message in one
	// call. nil (the default) is a complete no-op, exactly like
	// AWSPrices/AzurePrices: no Azure allocation's observation is ever
	// affected. See docs/azure-interruption-delivery.md's "Correlation"
	// section.
	AzureInterruptions azureInterruptionObserver
	Store              *state.Kubernetes
	Controller         *lifecycle.Controller
	mu                 sync.Mutex
	paused             bool
	draining           bool
}

func New(c Config, k kubernetes.Interface, g *scaleset.Client) *Operator {
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps(c.Namespace), Owner: c.Name}
	o := &Operator{Config: c, Client: k, GitHub: g, Store: s}
	// networkPeers supplies provider.Command.NetworkPeers for every provider
	// this scale set configures: the current WireGuard peer snapshot for an
	// allocation intending wireguard-mode networking, computed from this
	// same Store's own List (the allocation list this Operator already
	// reconciles) via wireguard.Snapshot - exactly the composition
	// NetworkPeers's own doc comment describes. It is a complete no-op for
	// every allocation with NetworkProfile == "" - i.e. every allocation
	// except one compiled from a "wireguard" mode NetworkProfile (see
	// Config.NetworkProfile and internal/configapi/compile.go's network()).
	networkPeers := func(ctx context.Context, a lifecycle.Allocation) ([]wireguard.Peer, error) {
		allocations, e := s.List(ctx)
		if e != nil {
			return nil, e
		}
		return wireguard.Snapshot(a.ID, a.NetworkProfile, allocations), nil
	}
	providers := map[string]lifecycle.Provider{}
	for name, p := range c.Providers {
		providers[name] = &provider.Command{Config: p, Bootstrap: func(ctx context.Context, id string) (string, error) {
			if g == nil {
				return "", errors.New("GitHub JIT client unavailable during recovery")
			}
			r, e := g.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: id, WorkFolder: "_work"}, c.ScaleSetID)
			if e != nil {
				return "", errors.New("GitHub JIT request failed")
			}
			return r.EncodedJITConfig, nil
		}, NetworkPeers: networkPeers}
	}
	o.Controller = &lifecycle.Controller{Store: s, Providers: providers, Now: time.Now, Runners: &runnerDeregistrar{client: g}}
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
	c.BudgetDailyMicros = 0
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
	return o.readFleet(ctx, true)
}

func (o *Operator) readFleet(ctx context.Context, create bool) (*corev1.ConfigMap, fleet, error) {
	maps := o.Client.CoreV1().ConfigMaps(o.Config.Namespace)
	cm, e := maps.Get(ctx, o.Config.Name+"-fleet", metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: o.Config.Name + "-fleet", Labels: map[string]string{"runnerscout/owner": o.Config.Name}}, Data: map[string]string{"fleet": "{}"}}
		e = nil
		if create {
			cm, e = maps.Create(ctx, cm, metav1.CreateOptions{})
		}
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
	if f.Reserved == nil {
		f.Reserved = map[string]int64{}
	}
	if f.Pending == nil {
		f.Pending = map[string]lifecycle.Allocation{}
	}
	if f.RetriesUsedByRun == nil {
		f.RetriesUsedByRun = map[int64]int{}
	}
	if f.PendingReruns == nil {
		f.PendingReruns = map[int64]pendingRerun{}
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
		// Deleted means cleanup is confirmed (the cloud resource is provably
		// gone, whether from a spot interruption, a MaxLifetimeSeconds
		// expiry, or a drain before job pickup - see lifecycle.Controller.Step,
		// which only ever sets Deleted after Observe reports !Exists). The
		// admission slot must release regardless of whether a job ever
		// completed: Completed and cleanup are orthogonal, and gating
		// release on both left every non-Completed terminal allocation
		// (interrupted, expired, drained) permanently consuming a slot.
		if a.Phase == lifecycle.Deleted && !f.Released[a.ID] {
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
	if n > 0 && o.Config.BudgetDailyMicros > 0 {
		unit := reservationMicros(o.Config.Requirements.MaxPriceMicros, o.Config.MaxLifetimeSeconds)
		remaining := o.Config.BudgetDailyMicros - spentToday(f, allocs, time.Now())
		affordable := 0
		if unit > 0 && remaining > 0 {
			affordable = int(remaining / unit)
		}
		if affordable < n {
			f.Admission.Admitted -= n - affordable
			n = affordable
			if n == 0 {
				f.Condition = "BudgetExhausted"
				return active, o.saveFleet(ctx, cm, f)
			}
		}
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
	if e == nil {
		catalog = o.refreshAWSPrices(ctx, catalog)
		catalog = o.refreshAzurePrices(ctx, catalog)
		catalog = o.refreshGCPPrices(ctx, catalog)
	}
	if n > 0 {
		if _, e = placement.Choose(time.Now(), o.Config.Requirements, catalog, nil); e != nil {
			return refuse("CatalogNotAdmissible")
		}
	}
	// A wireguard-mode NetworkProfile needs a unique overlay address per new
	// allocation, assigned once here (never reassigned - see
	// lifecycle.Allocation.WireGuardOverlayAddress's own doc comment) and
	// checkpointed as an ordinary field on the Pending record itself, no
	// differently from NetworkProfile immediately below it. All n addresses
	// for this batch are computed up front, before any allocation record is
	// created, so that an exhausted pool refuses the whole batch through the
	// same admission-failure path CatalogUnavailable/CatalogNotAdmissible
	// already use above - never a partially admitted batch, never a silent
	// skip. overlayTaken starts from every currently active allocation
	// sharing this NetworkProfile (from the Store, via allocs already listed
	// above) plus every not-yet-materialized entry in f.Pending (a previous
	// HandleDesiredRunnerCount call may have assigned one and not yet had
	// Tick move it into the Store) - the same two sources
	// wireguard.Snapshot/HandleDesiredRunnerCount's own "represented" active
	// count above already treats as the complete picture of what currently
	// exists. Concurrent double-assignment across two overlapping calls to
	// this method is prevented the same way every other mutation in this
	// function already is: the single in-process o.mu lock, the Lease that
	// keeps at most one leader Operator running this method at all, and
	// saveFleet's own ConfigMap resourceVersion compare-and-swap, which
	// fails the whole batch (this loop's addresses included) rather than
	// partially commit if a concurrent writer raced it regardless.
	var overlayAddresses []string
	if n > 0 && o.Config.NetworkProfile != "" {
		overlayTaken := map[string]bool{}
		for _, a := range allocs {
			if a.NetworkProfile == o.Config.NetworkProfile && a.Phase != lifecycle.Deleted && a.Phase != lifecycle.TimedOut && a.WireGuardOverlayAddress != "" {
				overlayTaken[a.WireGuardOverlayAddress] = true
			}
		}
		for _, a := range f.Pending {
			if a.NetworkProfile == o.Config.NetworkProfile && a.WireGuardOverlayAddress != "" {
				overlayTaken[a.WireGuardOverlayAddress] = true
			}
		}
		overlayAddresses = make([]string, 0, n)
		for range n {
			addr, addrErr := wireguard.NextOverlayAddress(o.Config.NetworkOverlayCIDRs, overlayTaken)
			if addrErr != nil {
				return refuse("OverlayAddressPoolExhausted")
			}
			overlayTaken[addr] = true
			overlayAddresses = append(overlayAddresses, addr)
		}
	}
	f.Condition = "DemandObserved"
	for i := range n {
		id := "rs-" + uuid.NewString()
		now := time.Now()
		f.Created[id] = now
		if o.Config.BudgetDailyMicros > 0 {
			f.Reserved[id] = reservationMicros(o.Config.Requirements.MaxPriceMicros, o.Config.MaxLifetimeSeconds)
		}
		a := lifecycle.Allocation{ID: id, Phase: lifecycle.Pending, Deadline: now.Add(time.Duration(o.Config.ProvisioningSeconds) * time.Second), MaxAttempts: 3, Catalog: catalog, Requirements: o.Config.Requirements, NetworkProfile: o.Config.NetworkProfile}
		if i < len(overlayAddresses) {
			a.WireGuardOverlayAddress = overlayAddresses[i]
		}
		f.Pending[id] = a
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
	// Each allocation runs exactly one job; capture its identity once and
	// never let a later, unrelated start event overwrite it.
	if a.RunID == 0 {
		a.RunID, a.Owner, a.Repo, a.ScaleSetJobID = j.WorkflowRunID, j.OwnerName, j.RepositoryName, j.JobID
	}
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

const (
	// tickStepBudget is the per-call context budget for phases that only
	// ever call Observe (Creating, Running) - no real-cloud evidence any
	// of them need more.
	tickStepBudget = 30 * time.Second
	// tickCreateOrDeleteBudget is the per-call budget for Pending (about
	// to call Create) and Deleting (about to call Delete) - see the call
	// site's own comment for the evidence this needs to be minutes, not
	// seconds.
	tickCreateOrDeleteBudget = 5 * time.Minute
	// tickStepConcurrency bounds how many allocations' Step calls run at
	// once in a single Tick - generous headroom above MaxRunners's own
	// hard cap of 10, since every allocation in the Store (not just the
	// currently active ones) gets a goroutine, most of which are
	// near-instant no-ops (Deleted/TimedOut phases return immediately).
	tickStepConcurrency = 20
	// terminalRetention bounds how long a Deleted/TimedOut allocation's
	// ConfigMap record survives past its own TerminalAt before Tick prunes
	// it - otherwise every terminal record accumulates forever, and
	// updateAdmission's own o.Store.List(ctx) (see HandleDesiredRunnerCount)
	// pays an ever-growing per-tick Kubernetes API cost for records that are
	// all provably inert. A conservative 24h keeps steady-state Store size
	// bounded to roughly one deployment-day's worth of churn.
	terminalRetention = 24 * time.Hour
)

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
	// Exactly one poll per Tick cycle, threaded down to every azure-kind
	// Command in Controller.Providers before any allocation this cycle is
	// stepped - never per-allocation. See pollAzureInterruptions and
	// applyAzureInterruptions's own doc comments for why.
	o.applyAzureInterruptions(o.pollAzureInterruptions(ctx))
	var failures []error
	// Each allocation's Step call runs on its own goroutine, bounded by
	// tickStepConcurrency, rather than sequentially: real cloud create/
	// delete calls (see the budget selection below) can genuinely take
	// minutes, and a burst of admissions (up to MaxRunners, all newly
	// Pending in the same Tick - see the f.Pending migration above) would
	// otherwise serialize behind one another, holding o.mu for their sum
	// rather than their max. o.mu itself is still held for the whole Tick
	// call as before - this only shortens how long that hold typically
	// lasts, it does not change what Tick is exclusive with (see issue
	// #144's own "why this hasn't just been bumped to a bigger number"
	// section for the full reasoning, including why o.Controller.Cooldowns
	// updates below are deferred until every goroutine has joined: nothing
	// may write that shared map while any goroutine could still be reading
	// it via placement.Choose inside Step).
	type stepOutcome struct {
		err       error
		cooldowns map[string]time.Time
	}
	results := make([]stepOutcome, len(allocs))
	sem := make(chan struct{}, tickStepConcurrency)
	var wg sync.WaitGroup
	for i, a := range allocs {
		created, ok := f.Created[a.ID]
		if !ok {
			// Recorded into failures and joined below, not returned
			// directly: a bare return here would strand every goroutine
			// this loop already spawned for earlier allocations and
			// release o.mu (deferred at the top of Tick) while they're
			// still running, letting a subsequent Tick race them.
			failures = append(failures, errors.New("allocation has no durable lifetime origin"))
			continue
		}
		if !a.Retire && (o.draining || !time.Now().Before(created.Add(time.Duration(o.Config.MaxLifetimeSeconds)*time.Second))) {
			a.Retire = true
			if _, e = o.Store.Save(ctx, a, a.Revision); e != nil {
				failures = append(failures, e)
				continue
			}
		}
		// Pending (about to call Create) and Deleting (about to call
		// Delete) are the phases real cloud dispatches have shown can
		// genuinely exceed 30s (see internal/provider/azure.go's
		// createAzure and internal/provider/gcp_sdk.go's createGCP/
		// deleteGCP for the documented evidence). Creating/Running only
		// ever call Observe, which has no such evidence and stays tight
		// so a stuck one can't hold a concurrency slot for minutes.
		budget := tickStepBudget
		if a.Phase == lifecycle.Pending || a.Phase == lifecycle.Deleting {
			budget = tickCreateOrDeleteBudget
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string, budget time.Duration) {
			defer wg.Done()
			defer func() { <-sem }()
			call, cancel := context.WithTimeout(ctx, budget)
			stepErr := o.Controller.Step(call, id)
			cancel()
			outcome := stepOutcome{err: stepErr}
			if updated, loadErr := o.Store.Load(ctx, id); loadErr == nil && len(updated.RejectedAt) > 0 {
				outcome.cooldowns = make(map[string]time.Time, len(updated.RejectedAt))
				for pool, at := range updated.RejectedAt {
					outcome.cooldowns[pool] = at.Add(5 * time.Minute)
				}
			}
			results[i] = outcome
		}(i, a.ID, budget)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			failures = append(failures, r.err)
		}
		for pool, until := range r.cooldowns {
			if until.After(o.Controller.Cooldowns[pool]) {
				o.Controller.Cooldowns[pool] = until
			}
		}
	}
	if e := o.processInterruptionRetries(ctx); e != nil {
		failures = append(failures, e)
	}
	if e := o.pruneTerminalAllocations(ctx, f, allocs); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}

// pruneTerminalAllocations deletes each Deleted/TimedOut allocation's
// ConfigMap record once it is past terminalRetention. allocs is the pre-Step
// snapshot Tick already loaded: Step never changes an already-terminal
// allocation (Controller.Step no-ops for Deleted/TimedOut), so it remains an
// accurate view of every candidate's Phase and TerminalAt. A Deleted record
// is only pruned once f.Released[id] is true - the same fleet-level flag
// HandleDesiredRunnerCount sets the first time it decrements Admitted for
// this ID - so a record is never removed from the Store before that one-time
// admission accounting has had a chance to observe it via Store.List.
// TimedOut carries no such dependency: it is excluded from the active count
// immediately, and its Admitted slot is released only by Reconcile's own
// zero-demand cohort reset, never by anything keyed off the record's
// continued existence.
func (o *Operator) pruneTerminalAllocations(ctx context.Context, f fleet, allocs []lifecycle.Allocation) error {
	var failures []error
	for _, a := range allocs {
		if a.TerminalAt.IsZero() || time.Since(a.TerminalAt) < terminalRetention {
			continue
		}
		if a.Phase != lifecycle.TimedOut && !(a.Phase == lifecycle.Deleted && f.Released[a.ID]) {
			continue
		}
		if e := o.Store.Delete(ctx, a.ID); e != nil {
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
