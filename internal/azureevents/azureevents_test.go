package azureevents

import "testing"

const wellFormedPreempted = `{
  "id": "8945cf9b-e220-496e-ab4f-f3a239318995",
  "source": "/subscriptions/11111111-1111-1111-1111-111111111111",
  "subject": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1",
  "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated",
  "specversion": "1.0",
  "time": "2023-07-24T19:20:37.9245071Z",
  "data": {
    "resourceInfo": {
      "id": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1/providers/Microsoft.ResourceHealth/resourceAnnotations/evt1",
      "name": "evt1",
      "type": "Microsoft.ResourceHealth/resourceAnnotations",
      "properties": {
        "targetResourceId": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1",
        "targetResourceType": "Microsoft.Compute/virtualMachines",
        "occurredTime": "2023-07-24T19:20:37.9245071Z",
        "annotationName": "VirtualMachinePreempted",
        "reason": "Spot eviction",
        "summary": "This virtual machine was preempted because Azure needed the capacity back.",
        "context": "Platform Initiated",
        "category": "Unplanned"
      }
    },
    "operationalInfo": {
      "resourceEventTime": "2023-07-24T19:20:37.9245071Z"
    },
    "apiVersion": "2022-08-01"
  }
}`

const wellFormedDeallocation = `{
  "id": "8945cf9b-e220-496e-ab4f-f3a239318995",
  "source": "/subscriptions/11111111-1111-1111-1111-111111111111",
  "subject": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1",
  "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated",
  "specversion": "1.0",
  "time": "2023-07-24T19:20:37.9245071Z",
  "data": {
    "resourceInfo": {
      "id": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1/providers/Microsoft.ResourceHealth/resourceAnnotations/evt1",
      "name": "evt1",
      "type": "Microsoft.ResourceHealth/resourceAnnotations",
      "properties": {
        "targetResourceId": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1",
        "targetResourceType": "Microsoft.Compute/virtualMachines",
        "occurredTime": "2023-07-24T19:20:37.9245071Z",
        "annotationName": "VirtualMachineDeallocationInitiated",
        "reason": "Stopping and deallocating",
        "summary": "This virtual machine is stopped and deallocated as requested by an authorized user or process.",
        "context": "Customer Initiated",
        "category": "Not Applicable"
      }
    },
    "operationalInfo": {
      "resourceEventTime": "2023-07-24T19:20:37.9245071Z"
    },
    "apiVersion": "2022-08-01"
  }
}`

func TestParsePreemptionEvent_VirtualMachinePreempted(t *testing.T) {
	resourceID, preempted, err := ParsePreemptionEvent([]byte(wellFormedPreempted))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !preempted {
		t.Fatalf("expected preempted=true")
	}
	const want = "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1"
	if resourceID != want {
		t.Fatalf("resourceID = %q, want %q", resourceID, want)
	}
}

func TestParsePreemptionEvent_DifferentAnnotation(t *testing.T) {
	resourceID, preempted, err := ParsePreemptionEvent([]byte(wellFormedDeallocation))
	if err != nil {
		t.Fatalf("unexpected error for legitimate non-preemption health event: %v", err)
	}
	if preempted {
		t.Fatalf("expected preempted=false for a VirtualMachineDeallocationInitiated annotation")
	}
	const want = "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm1"
	if resourceID != want {
		t.Fatalf("resourceID = %q, want %q", resourceID, want)
	}
}

func TestParsePreemptionEvent_MalformedPayloadsMustError(t *testing.T) {
	cases := map[string]string{
		"invalid JSON": `{not json`,

		"wrong event type entirely": `{
			"id": "1", "type": "Microsoft.Storage.BlobCreated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/resourceAnnotations",
					"properties": {
						"targetResourceId": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1",
						"annotationName": "VirtualMachinePreempted",
						"reason": "r", "context": "Platform Initiated", "category": "Unplanned"
					}
				}
			}
		}`,

		"AvailabilityStatusChanged event masquerading with the same top-level type omitted": `{
			"id": "1", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/availabilityStatuses",
					"properties": {
						"targetResourceId": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1",
						"previousAvailabilityState": "Unavailable",
						"availabilityState": "Available"
					}
				}
			}
		}`,

		"missing resourceId": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/resourceAnnotations",
					"properties": {
						"annotationName": "VirtualMachinePreempted",
						"reason": "r", "context": "Platform Initiated", "category": "Unplanned"
					}
				}
			}
		}`,

		"missing annotationName": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/resourceAnnotations",
					"properties": {
						"targetResourceId": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1",
						"reason": "r", "context": "Platform Initiated", "category": "Unplanned"
					}
				}
			}
		}`,

		"missing data entirely": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0"
		}`,

		"missing resourceInfo": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {"apiVersion": "2022-08-01"}
		}`,

		"missing properties": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {"resourceInfo": {"type": "Microsoft.ResourceHealth/resourceAnnotations"}}
		}`,

		"mismatched resourceInfo type": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/availabilityStatuses",
					"properties": {
						"targetResourceId": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1",
						"annotationName": "VirtualMachinePreempted",
						"reason": "r", "context": "Platform Initiated", "category": "Unplanned"
					}
				}
			}
		}`,

		"malformed targetResourceId shape": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/resourceAnnotations",
					"properties": {
						"targetResourceId": "vm1",
						"annotationName": "VirtualMachinePreempted",
						"reason": "r", "context": "Platform Initiated", "category": "Unplanned"
					}
				}
			}
		}`,

		"missing context and category": `{
			"id": "1", "type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated", "specversion": "1.0",
			"data": {
				"resourceInfo": {
					"type": "Microsoft.ResourceHealth/resourceAnnotations",
					"properties": {
						"targetResourceId": "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm1",
						"annotationName": "VirtualMachinePreempted",
						"reason": "r"
					}
				}
			}
		}`,

		"empty payload": ``,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, preempted, err := ParsePreemptionEvent([]byte(payload))
			if err == nil {
				t.Fatalf("expected an error for %q, got preempted=%v with no error", name, preempted)
			}
		})
	}
}
