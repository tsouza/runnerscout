// Package v1alpha1 defines the experimental namespaced RunnerScout configuration API.
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const Group = "runnerscout.io"
const Version = "v1alpha1"

// LocalReference cannot name another namespace. Controllers must resolve it in
// the namespace of the referring resource.
type LocalReference struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

type SecretKeyReference struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
}

// ConfigurationStatus contains references and redacted conditions, never credentials.
type ConfigurationStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.kind != 'aws' || (has(self.accountID) && self.accountID.matches('^[0-9]{12}$') && has(self.securityGroup) && size(self.securityGroup) > 0)",message="AWS requires expected account and security group"
// +kubebuilder:validation:XValidation:rule="self.kind != 'azure' || (has(self.subscription) && has(self.resourceGroup) && has(self.securityGroup) && has(self.sshPublicKey))",message="Azure requires subscription, resource group, network security group and SSH public key"
// +kubebuilder:validation:XValidation:rule="self.kind != 'gcp' || (has(self.project) && size(self.project) > 0)",message="GCP requires project"
type ProviderConnection struct {
	// +kubebuilder:validation:Enum=aws;azure;gcp
	Kind          string `json:"kind"`
	AccountID     string `json:"accountID,omitempty"`
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resourceGroup,omitempty"`
	SSHPublicKey  string `json:"sshPublicKey,omitempty"`
	Project       string `json:"project,omitempty"`
	Profile       string `json:"profile,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Subnet        string `json:"subnet"`
	SecurityGroup string `json:"securityGroup,omitempty"`
}

type ProviderConfigSpec struct {
	Connection ProviderConnection `json:"connection"`
	// Environment keys are restricted to provider authentication configuration.
	// Secret values are resolved separately at the execution boundary.
	CredentialEnvironment map[string]SecretKeyReference `json:"credentialEnvironment,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProviderConfigSpec  `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

type Resources struct {
	// +kubebuilder:validation:Minimum=1
	CPU int `json:"cpu"`
	// +kubebuilder:validation:Minimum=1
	MemoryMiB int `json:"memoryMiB"`
	// +kubebuilder:validation:Enum=amd64;arm64
	Architecture string `json:"architecture"`
	Vendor       string `json:"vendor,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	Capabilities []string `json:"capabilities,omitempty"`
}

type PlacementPolicy struct {
	// +kubebuilder:validation:Enum=lowest-price
	Policy string `json:"policy"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Regions []string `json:"regions"`
	// +kubebuilder:validation:Minimum=1
	MaxPriceMicros int64 `json:"maxPriceMicros"`
	AllowOnDemand  bool  `json:"allowOnDemand"`
}

type RetryPolicy struct {
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3
	MaxRetries                 int  `json:"maxRetries,omitempty"`
	AcknowledgeRepeatedEffects bool `json:"acknowledgeRepeatedEffects,omitempty"`
}

type RunnerClassSpec struct {
	Resources Resources       `json:"resources"`
	Placement PlacementPolicy `json:"placement"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Providers  []LocalReference `json:"providers"`
	CatalogRef LocalReference   `json:"catalogRef"`
	NetworkRef *LocalReference  `json:"networkRef,omitempty"`
	Retry      RetryPolicy      `json:"retry"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type RunnerClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RunnerClassSpec     `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.mode != 'app' || (has(self.appClientID) && size(self.appClientID) > 0 && has(self.appInstallationID))",message="App authentication requires client and installation identity"
// +kubebuilder:validation:XValidation:rule="self.mode != 'pat' || (!has(self.appClientID) && !has(self.appInstallationID))",message="PAT authentication excludes App settings"
type GitHubAuthentication struct {
	// +kubebuilder:validation:Enum=pat;app
	Mode        string             `json:"mode"`
	SecretRef   SecretKeyReference `json:"secretRef"`
	AppClientID string             `json:"appClientID,omitempty"`
	// +kubebuilder:validation:Minimum=1
	AppInstallationID int64 `json:"appInstallationID,omitempty"`
}

type GitHubScaleSet struct {
	URL string `json:"url"`
	// +kubebuilder:validation:Minimum=1
	ScaleSetID int                  `json:"scaleSetID"`
	Auth       GitHubAuthentication `json:"auth"`
}

// +kubebuilder:validation:XValidation:rule="self.maxLifetimeSeconds >= self.provisioningSeconds",message="lifetime must cover provisioning deadline"
type RunnerScaleSetSpec struct {
	RunnerClassRef LocalReference `json:"runnerClassRef"`
	GitHub         GitHubScaleSet `json:"github"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	MaxRunners int `json:"maxRunners"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=600
	ProvisioningSeconds int `json:"provisioningSeconds"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=21600
	MaxLifetimeSeconds int  `json:"maxLifetimeSeconds"`
	Suspend            bool `json:"suspend,omitempty"`
	// BudgetRef bounds worst-case daily spend for this scale set. A
	// CapacityBudget referenced by more than one RunnerScaleSet is not a
	// shared pool: each referencing scale set enforces the same ceiling
	// independently against only its own admitted allocations.
	BudgetRef *LocalReference `json:"budgetRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type RunnerScaleSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RunnerScaleSetSpec  `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

type Offering struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Zone     string `json:"zone"`
	Machine  string `json:"machine"`
	Image    string `json:"image"`
	// +kubebuilder:validation:Minimum=1
	CPU int `json:"cpu"`
	// +kubebuilder:validation:Minimum=1
	MemoryMiB int `json:"memoryMiB"`
	// +kubebuilder:validation:Enum=amd64;arm64
	Architecture string   `json:"architecture"`
	Vendor       string   `json:"vendor,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Spot         bool     `json:"spot"`
	// +kubebuilder:validation:Minimum=0
	PriceMicros int64 `json:"priceMicros"`
	// +kubebuilder:validation:Enum=USD
	Currency   string      `json:"currency"`
	ObservedAt metav1.Time `json:"observedAt"`
}

type CapacityCatalogSpec struct {
	// +kubebuilder:validation:MaxItems=1000
	Offerings []Offering      `json:"offerings"`
	Complete  map[string]bool `json:"complete"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type CapacityCatalog struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CapacityCatalogSpec `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

type NetworkMapping struct {
	ProviderRef LocalReference `json:"providerRef"`
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`
	// +kubebuilder:validation:MinLength=1
	NetworkID string `json:"networkID"`
	// +kubebuilder:validation:MinLength=1
	SubnetID string `json:"subnetID"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	CIDRs []string `json:"cidrs"`
}

// NetworkProfile describes explicit operator-managed networking. Enabling an
// overlay never authorizes the controller to create a paid gateway.
type NetworkProfileSpec struct {
	// +kubebuilder:validation:Enum=separate;wireguard
	Mode string `json:"mode"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Mappings        []NetworkMapping    `json:"mappings"`
	AllowedServices []string            `json:"allowedServices,omitempty"`
	EnrollmentRef   *SecretKeyReference `json:"enrollmentRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type NetworkProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NetworkProfileSpec  `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

// CapacityBudgetSpec holds only the worst-case daily spend ceiling. Running
// spend is never persisted here: every RunnerScaleSet that references a
// CapacityBudget recomputes its own spend from its own admitted allocations
// on every reconcile pass.
type CapacityBudgetSpec struct {
	// +kubebuilder:validation:Minimum=1
	DailyBudgetMicros int64 `json:"dailyBudgetMicros"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type CapacityBudget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CapacityBudgetSpec  `json:"spec"`
	Status            ConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

// +kubebuilder:object:root=true
type RunnerClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerClass `json:"items"`
}

// +kubebuilder:object:root=true
type RunnerScaleSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerScaleSet `json:"items"`
}

// +kubebuilder:object:root=true
type CapacityCatalogList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityCatalog `json:"items"`
}

// +kubebuilder:object:root=true
type NetworkProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkProfile `json:"items"`
}

// +kubebuilder:object:root=true
type CapacityBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityBudget `json:"items"`
}
