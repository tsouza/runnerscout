package configapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	"github.com/tsouza/runnerscout/internal/placement"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func fixture() Snapshot {
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: "test"} }
	s := Snapshot{
		ScaleSet: api.RunnerScaleSet{ObjectMeta: meta("build"), Spec: api.RunnerScaleSetSpec{RunnerClassRef: api.LocalReference{Name: "linux"}, GitHub: api.GitHubScaleSet{URL: "https://github.com/example", ScaleSetID: 1, Auth: api.GitHubAuthentication{Mode: "pat", SecretRef: api.SecretKeyReference{Name: "github", Key: "token"}}}, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 300}},
		Class:    api.RunnerClass{ObjectMeta: meta("linux"), Spec: api.RunnerClassSpec{Resources: api.Resources{CPU: 2, MemoryMiB: 4096, Architecture: "amd64", Capabilities: []string{"docker"}}, Placement: api.PlacementPolicy{Policy: "lowest-price", Regions: []string{"us-east-1", "eastus", "us-central1"}, MaxPriceMicros: 200000, AllowOnDemand: false}, Providers: []api.LocalReference{{Name: "aws"}, {Name: "azure"}, {Name: "gcp"}}, CatalogRef: api.LocalReference{Name: "prices"}}},
		Catalog:  api.CapacityCatalog{ObjectMeta: meta("prices"), Spec: api.CapacityCatalogSpec{Complete: map[string]bool{"aws": true, "azure": true, "gcp": true}}},
		Providers: map[string]api.ProviderConfig{
			"aws":   {ObjectMeta: meta("aws"), Spec: api.ProviderConfigSpec{Connection: api.ProviderConnection{Kind: "aws", AccountID: "123456789012", Subnet: "subnet-1", SecurityGroup: "sg-1"}}},
			"azure": {ObjectMeta: meta("azure"), Spec: api.ProviderConfigSpec{Connection: api.ProviderConnection{Kind: "azure", Subscription: "subscription", ResourceGroup: "runners", Subnet: "subnet-2", SecurityGroup: "nsg-2", SSHPublicKey: "ssh-ed25519 AAAA"}}},
			"gcp":   {ObjectMeta: meta("gcp"), Spec: api.ProviderConfigSpec{Connection: api.ProviderConnection{Kind: "gcp", Project: "project", Subnet: "subnet-3"}}},
		},
	}
	return s
}

func TestCompilePreservesMulticloudConstraintsAndReferenceIsolation(t *testing.T) {
	s := fixture()
	p := s.Providers["aws"]
	p.Spec.CredentialEnvironment = map[string]api.SecretKeyReference{"AWS_ACCESS_KEY_ID": {Name: "aws-auth", Key: "access-id"}}
	s.Providers["aws"] = p
	r, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Config.Providers) != 3 || r.Config.Requirements.CPU != 2 || r.Config.Requirements.MemoryMiB != 4096 || r.Config.Requirements.AllowOnDemand || r.Config.MaxRunners != 2 {
		t.Fatal("portable constraints or admission policy changed")
	}
	if r.Credentials["aws"]["AWS_ACCESS_KEY_ID"].Name != "aws-auth" || r.Auth.SecretRef.Name != "github" {
		t.Fatal("credential references lost")
	}
	s.Class.Spec.Resources.Capabilities[0] = "changed"
	s.Catalog.Spec.Complete["aws"] = false
	delete(p.Spec.CredentialEnvironment, "AWS_ACCESS_KEY_ID")
	if r.Config.Requirements.Capabilities[0] != "docker" || !r.Config.Catalog.Complete["aws"] || len(r.Credentials["aws"]) != 1 {
		t.Fatal("resolved configuration aliases mutable source data")
	}
}

func TestCompileRejectsBrokenOrCrossNamespaceReferences(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"wrong class namespace":    func(s *Snapshot) { s.Class.Namespace = "other" },
		"wrong catalog namespace":  func(s *Snapshot) { s.Catalog.Namespace = "other" },
		"wrong provider namespace": func(s *Snapshot) { p := s.Providers["aws"]; p.Namespace = "other"; s.Providers["aws"] = p },
		"missing provider":         func(s *Snapshot) { delete(s.Providers, "aws") },
		"duplicate provider": func(s *Snapshot) {
			s.Class.Spec.Providers = append(s.Class.Spec.Providers, api.LocalReference{Name: "aws"})
		},
		"wrong class identity": func(s *Snapshot) { s.Class.Name = "different" },
		"deleting dependency":  func(s *Snapshot) { now := metav1.Now(); s.Class.DeletionTimestamp = &now },
		"invalid Secret key":   func(s *Snapshot) { s.ScaleSet.Spec.GitHub.Auth.SecretRef.Key = "../token" },
		"mixed auth":           func(s *Snapshot) { s.ScaleSet.Spec.GitHub.Auth.AppClientID = "client" },
		"incomplete App":       func(s *Snapshot) { s.ScaleSet.Spec.GitHub.Auth.Mode = "app" },
		"injected executable path": func(s *Snapshot) {
			p := s.Providers["aws"]
			p.Spec.CredentialEnvironment = map[string]api.SecretKeyReference{"PATH": {Name: "attack", Key: "path"}}
			s.Providers["aws"] = p
		},
		"foreign provider identity": func(s *Snapshot) { p := s.Providers["aws"]; p.Spec.Connection.AccountID = ""; s.Providers["aws"] = p },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			change(&s)
			if _, err := Compile(s); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestCompileDoesNotPretendUnsupportedExecutionExists(t *testing.T) {
	// Retry execution is supported for PAT authentication (see
	// TestCompileAcceptsRetryPolicyWithPATAuthentication); App-mode reuse of
	// its JWT/installation-token exchange is not implemented yet.
	s := fixture()
	s.ScaleSet.Spec.GitHub.Auth = api.GitHubAuthentication{Mode: "app", AppClientID: "app-client", AppInstallationID: 1, SecretRef: api.SecretKeyReference{Name: "github", Key: "key"}}
	s.Class.Spec.Retry = api.RetryPolicy{Enabled: true, MaxRetries: 1, AcknowledgeRepeatedEffects: true}
	if _, err := Compile(s); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("retry execution claimed for App authentication: %v", err)
	}
	// "wireguard" is a real, validated mode as of this change (see
	// TestCompileAcceptsSingleMappingWireGuardNetwork and its sibling
	// rejection tests below) - only a mode outside the CRD's own
	// +kubebuilder:validation:Enum=separate;wireguard is still unsupported at
	// this Go-level defense-in-depth check.
	s = fixture()
	s.Class.Spec.NetworkRef = &api.LocalReference{Name: "mesh"}
	s.Network = &api.NetworkProfile{ObjectMeta: metav1.ObjectMeta{Name: "mesh", Namespace: "test"}, Spec: api.NetworkProfileSpec{Mode: "shared"}}
	if _, err := Compile(s); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("overlay execution claimed: %v", err)
	}
}

// wireGuardFixture returns a Snapshot scoped to exactly one provider ("aws"),
// matching the single-NetworkMapping restriction wireguard mode enforces:
// with only one provider referenced, one mapping can honestly cover it and
// every catalog offering.
func wireGuardFixture() Snapshot {
	s := fixture()
	s.Class.Spec.Providers = []api.LocalReference{{Name: "aws"}}
	s.Class.Spec.NetworkRef = &api.LocalReference{Name: "mesh"}
	s.Network = &api.NetworkProfile{ObjectMeta: metav1.ObjectMeta{Name: "mesh", Namespace: "test"}, Spec: api.NetworkProfileSpec{Mode: "wireguard", Mappings: []api.NetworkMapping{
		{ProviderRef: api.LocalReference{Name: "aws"}, Region: "us-east-1", NetworkID: "vpc", SubnetID: "subnet-1", CIDRs: []string{"10.1.0.0/24"}},
	}}}
	return s
}

func TestCompileAcceptsSingleMappingWireGuardNetwork(t *testing.T) {
	s := wireGuardFixture()
	r, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.NetworkProfile != "mesh" {
		t.Fatalf("expected NetworkProfile %q threaded into resolved config, got %q", "mesh", r.Config.NetworkProfile)
	}
	// The overlay allocator (internal/wireguard.NextOverlayAddress, wired in
	// by internal/operator.HandleDesiredRunnerCount) needs the wireguard
	// mode NetworkProfile's single NetworkMapping's own CIDRs as its address
	// pool - see operator.Config.NetworkOverlayCIDRs's doc comment.
	if len(r.Config.NetworkOverlayCIDRs) != 1 || r.Config.NetworkOverlayCIDRs[0] != "10.1.0.0/24" {
		t.Fatalf("expected the NetworkMapping's own CIDRs threaded into NetworkOverlayCIDRs, got %v", r.Config.NetworkOverlayCIDRs)
	}
}

func TestCompileSeparateModeNeverThreadsNetworkProfile(t *testing.T) {
	base := fixture()
	base.Class.Spec.NetworkRef = &api.LocalReference{Name: "private"}
	base.Network = &api.NetworkProfile{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "test"}, Spec: api.NetworkProfileSpec{Mode: "separate", Mappings: []api.NetworkMapping{
		{ProviderRef: api.LocalReference{Name: "aws"}, Region: "us-east-1", NetworkID: "vpc", SubnetID: "subnet-1", CIDRs: []string{"10.1.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "azure"}, Region: "eastus", NetworkID: "vnet", SubnetID: "subnet-2", CIDRs: []string{"10.2.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "gcp"}, Region: "us-central1", NetworkID: "vpc", SubnetID: "subnet-3", CIDRs: []string{"10.3.0.0/24"}},
	}}}
	r, err := Compile(base)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.NetworkProfile != "" {
		t.Fatalf("separate mode must never set Allocation.NetworkProfile identity, got %q", r.Config.NetworkProfile)
	}
	if len(r.Config.NetworkOverlayCIDRs) != 0 {
		t.Fatalf("separate mode must never set overlay CIDRs, got %v", r.Config.NetworkOverlayCIDRs)
	}
}

func TestCompileRejectsMultiMappingWireGuardNetwork(t *testing.T) {
	s := fixture()
	s.Class.Spec.NetworkRef = &api.LocalReference{Name: "mesh"}
	s.Network = &api.NetworkProfile{ObjectMeta: metav1.ObjectMeta{Name: "mesh", Namespace: "test"}, Spec: api.NetworkProfileSpec{Mode: "wireguard", Mappings: []api.NetworkMapping{
		{ProviderRef: api.LocalReference{Name: "aws"}, Region: "us-east-1", NetworkID: "vpc", SubnetID: "subnet-1", CIDRs: []string{"10.1.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "azure"}, Region: "eastus", NetworkID: "vnet", SubnetID: "subnet-2", CIDRs: []string{"10.2.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "gcp"}, Region: "us-central1", NetworkID: "vpc", SubnetID: "subnet-3", CIDRs: []string{"10.3.0.0/24"}},
	}}}
	if _, err := Compile(s); err == nil {
		t.Fatal("multi-mapping wireguard profile accepted")
	}
}

func TestCompileWireGuardRejectsAllowedServices(t *testing.T) {
	s := wireGuardFixture()
	s.Network.Spec.AllowedServices = []string{"svc"}
	if _, err := Compile(s); err == nil {
		t.Fatal("wireguard AllowedServices accepted despite undefined semantics")
	}
}

func TestCompileWireGuardValidatesEnrollmentRefSecretKey(t *testing.T) {
	s := wireGuardFixture()
	s.Network.Spec.EnrollmentRef = &api.SecretKeyReference{Name: "psk", Key: "../psk"}
	if _, err := Compile(s); err == nil {
		t.Fatal("invalid EnrollmentRef Secret key accepted")
	}
	s.Network.Spec.EnrollmentRef.Key = "psk"
	if _, err := Compile(s); err != nil {
		t.Fatal("valid EnrollmentRef rejected", err)
	}
}

func TestCompileWireGuardAcceptsNilEnrollmentRef(t *testing.T) {
	s := wireGuardFixture()
	s.Network.Spec.EnrollmentRef = nil
	if _, err := Compile(s); err != nil {
		t.Fatal("nil EnrollmentRef (the default) rejected", err)
	}
}
func TestCompileAcceptsRetryPolicyWithPATAuthentication(t *testing.T) {
	s := fixture()
	s.Class.Spec.Retry = api.RetryPolicy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	r, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Config.Retry.Enabled || r.Config.Retry.MaxRetries != 2 || !r.Config.Retry.AcknowledgeRepeatedEffects {
		t.Fatal("retry policy not carried into resolved config", r.Config.Retry)
	}
}

func TestCompileStaleCatalogAllowsRecoveryButNotAdmission(t *testing.T) {
	s := fixture()
	s.Catalog.Spec.Offerings = []api.Offering{{ID: "pool", Provider: "aws", Region: "us-east-1", Zone: "us-east-1a", Machine: "m5.large", Image: "image", CPU: 2, MemoryMiB: 4096, Architecture: "amd64", Capabilities: []string{"docker"}, Spot: true, PriceMicros: 100000, Currency: "USD", ObservedAt: metav1.NewTime(time.Unix(1, 0))}}
	r, err := Compile(s)
	if err != nil {
		t.Fatalf("stale prices disabled startup cleanup: %v", err)
	}
	if _, err = placement.Choose(time.Now(), r.Config.Requirements, r.Config.Catalog, nil); err == nil {
		t.Fatal("stale catalog admitted a runner")
	}
}

func TestCompileNetworkMappingsRequireIsolationAndCoverage(t *testing.T) {
	base := fixture()
	base.Class.Spec.NetworkRef = &api.LocalReference{Name: "private"}
	base.Network = &api.NetworkProfile{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "test"}, Spec: api.NetworkProfileSpec{Mode: "separate", Mappings: []api.NetworkMapping{
		{ProviderRef: api.LocalReference{Name: "aws"}, Region: "us-east-1", NetworkID: "vpc", SubnetID: "subnet-1", CIDRs: []string{"10.1.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "azure"}, Region: "eastus", NetworkID: "vnet", SubnetID: "subnet-2", CIDRs: []string{"10.2.0.0/24"}},
		{ProviderRef: api.LocalReference{Name: "gcp"}, Region: "us-central1", NetworkID: "vpc", SubnetID: "subnet-3", CIDRs: []string{"10.3.0.0/24"}},
	}}}
	if _, err := Compile(base); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Snapshot){
		"overlap":          func(s *Snapshot) { s.Network.Spec.Mappings[1].CIDRs = []string{"10.1.0.0/25"} },
		"noncanonical":     func(s *Snapshot) { s.Network.Spec.Mappings[0].CIDRs = []string{"10.1.0.5/24"} },
		"missing provider": func(s *Snapshot) { s.Network.Spec.Mappings = s.Network.Spec.Mappings[:2] },
		"wrong subnet":     func(s *Snapshot) { s.Network.Spec.Mappings[0].SubnetID = "public" },
		"wrong namespace":  func(s *Snapshot) { s.Network.Namespace = "other" },
		"duplicate": func(s *Snapshot) {
			s.Network.Spec.Mappings = append(s.Network.Spec.Mappings, s.Network.Spec.Mappings[0])
		},
		"unmapped offering": func(s *Snapshot) { s.Catalog.Spec.Offerings = []api.Offering{{Provider: "aws", Region: "eastus"}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var s Snapshot
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			change(&s)
			if _, err := Compile(s); err == nil {
				t.Fatal("invalid network accepted")
			}
		})
	}
}

func TestCompileAzureSDKCredentialsExcludeCLICaches(t *testing.T) {
	s := fixture()
	azure := s.Providers["azure"]
	azure.Spec.CredentialEnvironment = map[string]api.SecretKeyReference{
		"AZURE_CLIENT_ID":            {Name: "azure-auth", Key: "client-id"},
		"AZURE_TENANT_ID":            {Name: "azure-auth", Key: "tenant-id"},
		"AZURE_FEDERATED_TOKEN_FILE": {Name: "azure-auth", Key: "token-path"},
	}
	s.Providers["azure"] = azure
	compiled, err := Compile(s)
	if err != nil || len(compiled.Credentials["azure"]) != 3 {
		t.Fatal(compiled, err)
	}
	azure.Spec.CredentialEnvironment["AZURE_CONFIG_DIR"] = api.SecretKeyReference{Name: "azure-auth", Key: "legacy-cache"}
	s.Providers["azure"] = azure
	if _, err = Compile(s); err == nil {
		t.Fatal("legacy CLI cache accepted by native SDK configuration")
	}
}
