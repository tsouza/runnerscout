// Package azureevents parses Azure Event Grid HealthResources events - the
// only officially documented mechanism for a confirmed Azure spot VM
// interruption, delivered push-based as a
// Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated
// CloudEvents payload (see
// https://learn.microsoft.com/en-us/azure/event-grid/event-schema-health-resources).
// It never creates or subscribes to any Event Grid or Storage Queue
// resource, and never imports internal/operator, internal/provider or
// internal/lifecycle itself - it only turns a received payload into a
// resource ID and a preemption verdict, mirroring internal/githubjobs and
// internal/prices's own single-purpose, dependency-free style. The delivery
// transport is internal/azurequeue.Client, polled once per Tick cycle by
// internal/operator (internal/operator/azure_interruptions.go); resource-ID
// correlation against a RunnerScout allocation happens in
// internal/provider's own azureConfirmedPreemption.
package azureevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// resourceAnnotatedEventType is the CloudEvents "type" value for a Resource
// Health annotation. The sibling
// Microsoft.ResourceNotifications.HealthResources.AvailabilityStatusChanged
// event type carries no annotationName at all and is rejected.
const resourceAnnotatedEventType = "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated"

// resourceAnnotationResourceInfoType is the documented data.resourceInfo.type
// value for a ResourceAnnotated event, distinct from
// "Microsoft.ResourceHealth/availabilityStatuses" used by
// AvailabilityStatusChanged events. Checking it too means a payload can't
// pass by getting the top-level type right while the nested envelope
// disagrees.
const resourceAnnotationResourceInfoType = "Microsoft.ResourceHealth/resourceAnnotations"

// preemptedAnnotationName is Azure's own documented annotation name for a
// confirmed spot/low-priority VM preemption (Resource Health virtual
// machine Health Annotations: "VirtualMachinePreempted" - Context:
// Platform Initiated, Category: Unplanned, ImpactType: Informational). Any
// other annotation name is a legitimate, different health event and must
// report preempted=false, never an error.
const preemptedAnnotationName = "VirtualMachinePreempted"

// armResourceID matches this codebase's ARM ID convention (see
// internal/provider/azure.go's azureID): a leading
// /subscriptions/<sub>/resourceGroups/<rg>/providers/<namespace>/<type>/<name>
// path, so a parsed ID can later be correlated against an allocation's own
// resource ID.
var armResourceID = regexp.MustCompile(`^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/[^/]+/[^/]+/[^/]+$`)

type cloudEvent struct {
	Type string     `json:"type"`
	Data *eventData `json:"data"`
}

type eventData struct {
	ResourceInfo *resourceInfo `json:"resourceInfo"`
}

type resourceInfo struct {
	Type       string                `json:"type"`
	Properties *annotationProperties `json:"properties"`
}

type annotationProperties struct {
	TargetResourceID string `json:"targetResourceId"`
	AnnotationName   string `json:"annotationName"`
	Reason           string `json:"reason"`
	Context          string `json:"context"`
	Category         string `json:"category"`
}

// ParsePreemptionEvent decodes a
// Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated
// CloudEvents payload and reports the annotated VM's ARM resource ID and
// whether the annotation is a confirmed spot preemption.
//
// It fails closed: any malformed, incomplete or ambiguous payload - invalid
// JSON, the wrong event type, a missing or malformed resource ID, a missing
// annotation name, or an inconsistent nested resourceInfo.type - returns an
// error rather than silently defaulting preempted to false. A well-formed
// event whose annotation name is simply not "VirtualMachinePreempted" is a
// legitimate, different health event and returns preempted=false with no
// error.
func ParsePreemptionEvent(data []byte) (resourceID string, preempted bool, err error) {
	var event cloudEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return "", false, fmt.Errorf("azureevents: malformed CloudEvents payload: %w", err)
	}
	if event.Type != resourceAnnotatedEventType {
		return "", false, fmt.Errorf("azureevents: unsupported or missing event type %q", event.Type)
	}
	if event.Data == nil || event.Data.ResourceInfo == nil || event.Data.ResourceInfo.Properties == nil {
		return "", false, errors.New("azureevents: event data incomplete")
	}
	info := event.Data.ResourceInfo
	if info.Type != resourceAnnotationResourceInfoType {
		return "", false, fmt.Errorf("azureevents: unexpected resourceInfo type %q", info.Type)
	}
	props := info.Properties
	if props.AnnotationName == "" {
		return "", false, errors.New("azureevents: annotation name missing")
	}
	if props.Reason == "" || props.Context == "" || props.Category == "" {
		return "", false, errors.New("azureevents: annotation context incomplete")
	}
	if props.TargetResourceID == "" {
		return "", false, errors.New("azureevents: target resource id missing")
	}
	if !armResourceID.MatchString(props.TargetResourceID) {
		return "", false, fmt.Errorf("azureevents: target resource id %q is not a valid ARM resource ID", props.TargetResourceID)
	}
	return props.TargetResourceID, props.AnnotationName == preemptedAnnotationName, nil
}
