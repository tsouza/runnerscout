// Package configapi resolves namespaced configuration into the existing runtime
// contract. A successful compile performs no GitHub, Kubernetes or cloud effects.
package configapi

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	"github.com/tsouza/runnerscout/internal/operator"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrUnsupported = errors.New("configuration requires an unimplemented runtime capability")

// Snapshot is the full dependency set for one scale set. Maps are indexed by
// Kubernetes object name, not by a user-supplied alias or a cross-namespace path.
type Snapshot struct {
	ScaleSet  api.RunnerScaleSet
	Class     api.RunnerClass
	Catalog   api.CapacityCatalog
	Providers map[string]api.ProviderConfig
	Network   *api.NetworkProfile
}

type Resolved struct {
	Config      operator.Config
	Auth        api.GitHubAuthentication
	Credentials map[string]map[string]api.SecretKeyReference
	Suspend     bool
}

func object(meta metav1.ObjectMeta, namespace, name string) error {
	if meta.Namespace != namespace || meta.Name != name || len(validation.IsDNS1123Label(name)) != 0 || len(validation.IsDNS1123Label(namespace)) != 0 {
		return errors.New("invalid or cross-namespace object reference")
	}
	if meta.DeletionTimestamp != nil {
		return errors.New("configuration dependency is being deleted")
	}
	return nil
}

func secret(ref api.SecretKeyReference) error {
	if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || len(validation.IsConfigMapKey(ref.Key)) != 0 || ref.Key == "" {
		return errors.New("invalid Secret key reference")
	}
	return nil
}

// Compile keeps price freshness out of startup validation: stale catalogs must
// stop admissions, never the cleanup of already committed allocations.
func Compile(s Snapshot) (Resolved, error) {
	var result Resolved
	ns := s.ScaleSet.Namespace
	if err := object(s.ScaleSet.ObjectMeta, ns, s.ScaleSet.Name); err != nil {
		return result, err
	}
	if err := object(s.Class.ObjectMeta, ns, s.ScaleSet.Spec.RunnerClassRef.Name); err != nil {
		return result, err
	}
	if err := object(s.Catalog.ObjectMeta, ns, s.Class.Spec.CatalogRef.Name); err != nil {
		return result, err
	}
	if err := secret(s.ScaleSet.Spec.GitHub.Auth.SecretRef); err != nil {
		return result, err
	}
	auth := s.ScaleSet.Spec.GitHub.Auth
	switch auth.Mode {
	case "pat":
		if auth.AppClientID != "" || auth.AppInstallationID != 0 {
			return result, errors.New("PAT authentication cannot contain App settings")
		}
	case "app":
		if auth.AppClientID == "" || auth.AppInstallationID <= 0 {
			return result, errors.New("App authentication requires client and installation identity")
		}
	default:
		return result, errors.New("unsupported GitHub authentication mode")
	}
	if s.Class.Spec.Retry.Enabled {
		return result, fmt.Errorf("%w: retry execution", ErrUnsupported)
	}
	if s.Class.Spec.Retry.MaxRetries != 0 || s.Class.Spec.Retry.AcknowledgeRepeatedEffects {
		return result, errors.New("disabled retries cannot contain active retry settings")
	}
	providers := make(map[string]provider.Config)
	credentials := make(map[string]map[string]api.SecretKeyReference)
	names := make([]string, 0, len(s.Class.Spec.Providers))
	for _, ref := range s.Class.Spec.Providers {
		p, ok := s.Providers[ref.Name]
		if !ok {
			return result, errors.New("referenced provider is missing")
		}
		if _, duplicate := providers[ref.Name]; duplicate {
			return result, errors.New("duplicate provider reference")
		}
		if err := object(p.ObjectMeta, ns, ref.Name); err != nil {
			return result, err
		}
		c := p.Spec.Connection
		providers[ref.Name] = provider.Config{Kind: c.Kind, AccountID: c.AccountID, Subscription: c.Subscription, ResourceGroup: c.ResourceGroup, SSHPublicKey: c.SSHPublicKey, Project: c.Project, Profile: c.Profile, Subnet: c.Subnet, SecurityGroup: c.SecurityGroup, Owner: s.ScaleSet.Name}
		credentials[ref.Name] = make(map[string]api.SecretKeyReference)
		for name, ref := range p.Spec.CredentialEnvironment {
			if !credentialVariable(c.Kind, name) {
				return result, errors.New("provider credential environment variable is not allowed")
			}
			if err := secret(ref); err != nil {
				return result, err
			}
			credentials[p.Name][name] = ref
		}
		names = append(names, ref.Name)
	}
	slices.Sort(names)
	if err := network(s, providers); err != nil {
		return result, err
	}
	r, p, limits := s.Class.Spec.Resources, s.Class.Spec.Placement, s.ScaleSet.Spec
	cfg := operator.Config{Name: s.ScaleSet.Name, Namespace: ns, GitHubURL: limits.GitHub.URL, ScaleSetID: limits.GitHub.ScaleSetID, MaxRunners: limits.MaxRunners, ProvisioningSeconds: limits.ProvisioningSeconds, MaxLifetimeSeconds: limits.MaxLifetimeSeconds, Providers: providers,
		Requirements: placement.Requirements{CPU: r.CPU, MemoryMiB: r.MemoryMiB, Architecture: r.Architecture, Vendor: r.Vendor, Capabilities: slices.Clone(r.Capabilities), Providers: names, Regions: slices.Clone(p.Regions), MaxPriceMicros: p.MaxPriceMicros, AllowOnDemand: p.AllowOnDemand, Policy: p.Policy}}
	cfg.Catalog.Complete = make(map[string]bool)
	for name, complete := range s.Catalog.Spec.Complete {
		cfg.Catalog.Complete[name] = complete
	}
	for _, o := range s.Catalog.Spec.Offerings {
		cfg.Catalog.Offerings = append(cfg.Catalog.Offerings, placement.Offering{ID: o.ID, Provider: o.Provider, Region: o.Region, Zone: o.Zone, Machine: o.Machine, Image: o.Image, CPU: o.CPU, MemoryMiB: o.MemoryMiB, Architecture: o.Architecture, Vendor: o.Vendor, Capabilities: slices.Clone(o.Capabilities), Spot: o.Spot, PriceMicros: o.PriceMicros, Currency: o.Currency, ObservedAt: o.ObservedAt.Time})
	}
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	return Resolved{Config: cfg, Auth: auth, Credentials: credentials, Suspend: limits.Suspend}, nil
}

func credentialVariable(kind, name string) bool {
	allowed := map[string][]string{
		"aws":   {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN"},
		"azure": {"AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_CLIENT_SECRET", "AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_CLIENT_CERTIFICATE_PASSWORD", "AZURE_CLIENT_SEND_CERTIFICATE_CHAIN"},
		"gcp":   {"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"},
	}
	return slices.Contains(allowed[kind], name)
}

func network(s Snapshot, providers map[string]provider.Config) error {
	if s.Class.Spec.NetworkRef == nil {
		if s.Network != nil {
			return errors.New("unreferenced network configuration")
		}
		return nil
	}
	if s.Network == nil {
		return errors.New("referenced network configuration is missing")
	}
	n := s.Network
	if err := object(n.ObjectMeta, s.ScaleSet.Namespace, s.Class.Spec.NetworkRef.Name); err != nil {
		return err
	}
	if n.Spec.Mode != "separate" {
		return fmt.Errorf("%w: shared network integration", ErrUnsupported)
	}
	if n.Spec.EnrollmentRef != nil || len(n.Spec.AllowedServices) != 0 {
		return errors.New("separate networking cannot enroll peers or imply overlay access rules")
	}
	seen := make(map[string]bool)
	var prefixes []netip.Prefix
	for _, mapping := range n.Spec.Mappings {
		p, ok := providers[mapping.ProviderRef.Name]
		key := mapping.ProviderRef.Name + "/" + mapping.Region
		if !ok || seen[key] || mapping.NetworkID == "" || mapping.SubnetID != p.Subnet || !slices.Contains(s.Class.Spec.Placement.Regions, mapping.Region) || len(mapping.CIDRs) == 0 {
			return errors.New("invalid or duplicate provider network mapping")
		}
		seen[key] = true
		for _, cidr := range mapping.CIDRs {
			prefix, err := netip.ParsePrefix(cidr)
			if err != nil || prefix != prefix.Masked() {
				return errors.New("network CIDR must be a canonical prefix")
			}
			for _, previous := range prefixes {
				if prefix.Overlaps(previous) {
					return errors.New("overlapping network CIDRs")
				}
			}
			prefixes = append(prefixes, prefix)
		}
	}
	for name := range providers {
		covered := false
		for _, mapping := range n.Spec.Mappings {
			covered = covered || mapping.ProviderRef.Name == name
		}
		if !covered {
			return errors.New("network profile does not cover every allowed provider")
		}
	}
	for _, offering := range s.Catalog.Spec.Offerings {
		if _, allowed := providers[offering.Provider]; allowed && slices.Contains(s.Class.Spec.Placement.Regions, offering.Region) && !seen[offering.Provider+"/"+offering.Region] {
			return errors.New("catalog offering lacks a network mapping")
		}
	}
	return nil
}
