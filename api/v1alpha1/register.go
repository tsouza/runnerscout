package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}

// CRDKinds is the canonical list of this API group's CRD kinds - the single
// source of truth for "how many/which CRDs exist" that AddToScheme below and
// any test asserting a complete CRD set (in Go or otherwise) should derive
// its expected count from, instead of a separately hand-maintained literal.
// A CRD count duplicated as an independent literal in multiple places has
// already drifted silently once when a CRD was added.
var CRDKinds = []string{"ProviderConfig", "RunnerClass", "RunnerScaleSet", "CapacityCatalog", "NetworkProfile", "CapacityBudget"}

func AddToScheme(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&ProviderConfig{}, &ProviderConfigList{},
		&RunnerClass{}, &RunnerClassList{},
		&RunnerScaleSet{}, &RunnerScaleSetList{},
		&CapacityCatalog{}, &CapacityCatalogList{},
		&NetworkProfile{}, &NetworkProfileList{},
		&CapacityBudget{}, &CapacityBudgetList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
