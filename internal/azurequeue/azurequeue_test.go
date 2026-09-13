package azurequeue

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
)

// fakeQueueClient is a minimal in-memory stand-in for *azqueue.QueueClient,
// satisfying the QueueClient interface structurally (Go interfaces are
// implicit, so the real SDK type never needs to appear in this test file).
type fakeQueueClient struct {
	messages []*azqueue.DequeuedMessage

	dequeueErr error
	deleteErr  error

	lastDequeueOptions *azqueue.DequeueMessagesOptions
	deleted            []string // messageID+":"+popReceipt of every DeleteMessage call
}

func strPtr(s string) *string { return &s }

func (f *fakeQueueClient) DequeueMessages(ctx context.Context, o *azqueue.DequeueMessagesOptions) (azqueue.DequeueMessagesResponse, error) {
	f.lastDequeueOptions = o
	if f.dequeueErr != nil {
		return azqueue.DequeueMessagesResponse{}, f.dequeueErr
	}
	return azqueue.DequeueMessagesResponse{Messages: f.messages}, nil
}

func (f *fakeQueueClient) DeleteMessage(ctx context.Context, messageID, popReceipt string, o *azqueue.DeleteMessageOptions) (azqueue.DeleteMessageResponse, error) {
	if f.deleteErr != nil {
		return azqueue.DeleteMessageResponse{}, f.deleteErr
	}
	f.deleted = append(f.deleted, messageID+":"+popReceipt)
	return azqueue.DeleteMessageResponse{}, nil
}

const preemptedResourceID = "/subscriptions/sub-1/resourceGroups/rg-1/providers/Microsoft.Compute/virtualMachines/rs-abc"

const preemptionEventJSON = `{
	"type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated",
	"data": {
		"resourceInfo": {
			"type": "Microsoft.ResourceHealth/resourceAnnotations",
			"properties": {
				"targetResourceId": "` + preemptedResourceID + `",
				"annotationName": "VirtualMachinePreempted",
				"reason": "Preempted",
				"context": "Platform Initiated",
				"category": "Unplanned"
			}
		}
	}
}`

const nonPreemptionAnnotationJSON = `{
	"type": "Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated",
	"data": {
		"resourceInfo": {
			"type": "Microsoft.ResourceHealth/resourceAnnotations",
			"properties": {
				"targetResourceId": "` + preemptedResourceID + `",
				"annotationName": "VirtualMachineDeallocationInitiated",
				"reason": "Stopping and deallocating",
				"context": "Customer Initiated",
				"category": "Not Applicable"
			}
		}
	}
}`

func message(id, popReceipt, text string) *azqueue.DequeuedMessage {
	return &azqueue.DequeuedMessage{MessageID: strPtr(id), PopReceipt: strPtr(popReceipt), MessageText: strPtr(text)}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestPollReturnsConfirmedPreemptionAndDeletesMessage(t *testing.T) {
	fake := &fakeQueueClient{messages: []*azqueue.DequeuedMessage{message("m1", "pop1", b64(preemptionEventJSON))}}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("expected no per-message error, got %v", results[0].Err)
	}
	if !results[0].Preempted {
		t.Fatalf("expected Preempted=true")
	}
	if results[0].ResourceID != preemptedResourceID {
		t.Fatalf("expected resource ID %q, got %q", preemptedResourceID, results[0].ResourceID)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "m1:pop1" {
		t.Fatalf("expected message m1/pop1 to be deleted, got %v", fake.deleted)
	}
}

func TestPollDeletesNonPreemptionAnnotationButReportsNotPreempted(t *testing.T) {
	fake := &fakeQueueClient{messages: []*azqueue.DequeuedMessage{message("m1", "pop1", b64(nonPreemptionAnnotationJSON))}}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Preempted {
		t.Fatalf("expected one successfully-classified non-preemption result, got %+v", results)
	}
	if len(fake.deleted) != 1 {
		t.Fatalf("expected the classified message to be deleted, got %v", fake.deleted)
	}
}

func TestPollLeavesUnparsableMessageUndeletedAndReportsError(t *testing.T) {
	fake := &fakeQueueClient{messages: []*azqueue.DequeuedMessage{message("m1", "pop1", b64(`{"type":"not-a-real-event"}`))}}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("expected one result carrying a parse error, got %+v", results)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("expected the unparsable message to remain in the queue, got deletions %v", fake.deleted)
	}
}

func TestPollAcceptsRawJSONWithoutBase64Encoding(t *testing.T) {
	// Defensive fallback: if the message body is not valid base64 (or does
	// not round-trip to a JSON object), it is tried as raw JSON directly,
	// in case a subscription or emulator is configured to deliver
	// unencoded CloudEvents payloads.
	fake := &fakeQueueClient{messages: []*azqueue.DequeuedMessage{message("m1", "pop1", preemptionEventJSON)}}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || !results[0].Preempted {
		t.Fatalf("expected raw JSON fallback to classify successfully, got %+v", results)
	}
}

func TestPollPropagatesDequeueFailure(t *testing.T) {
	fake := &fakeQueueClient{dequeueErr: errors.New("boom")}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err == nil {
		t.Fatalf("expected Poll to propagate the dequeue error")
	}
	if results != nil {
		t.Fatalf("expected no results on a dequeue failure, got %+v", results)
	}
}

func TestPollDefaultsMaxMessagesAndVisibilityTimeoutWhenUnset(t *testing.T) {
	fake := &fakeQueueClient{}
	c := &Client{Queue: fake}

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if fake.lastDequeueOptions == nil || fake.lastDequeueOptions.NumberOfMessages == nil || *fake.lastDequeueOptions.NumberOfMessages != 32 {
		t.Fatalf("expected default NumberOfMessages=32, got %+v", fake.lastDequeueOptions)
	}
	if fake.lastDequeueOptions.VisibilityTimeout != nil {
		t.Fatalf("expected no explicit VisibilityTimeout by default, got %v", *fake.lastDequeueOptions.VisibilityTimeout)
	}
}

func TestPollHonorsConfiguredMaxMessagesAndVisibilityTimeout(t *testing.T) {
	fake := &fakeQueueClient{}
	c := &Client{Queue: fake, MaxMessages: 5, VisibilityTimeoutSeconds: 60}

	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if fake.lastDequeueOptions == nil || fake.lastDequeueOptions.NumberOfMessages == nil || *fake.lastDequeueOptions.NumberOfMessages != 5 {
		t.Fatalf("expected configured NumberOfMessages=5, got %+v", fake.lastDequeueOptions)
	}
	if fake.lastDequeueOptions.VisibilityTimeout == nil || *fake.lastDequeueOptions.VisibilityTimeout != 60 {
		t.Fatalf("expected configured VisibilityTimeout=60, got %+v", fake.lastDequeueOptions)
	}
}

func TestPollDeleteFailureDoesNotFailBatch(t *testing.T) {
	// A best-effort delete: Storage Queue's own at-least-once redelivery
	// makes a failed delete safe to ignore here (a duplicate future
	// confirmation of the same preemption is harmless - see
	// docs/azure-interruption-delivery.md), so Poll still reports the
	// successful classification rather than discarding it.
	fake := &fakeQueueClient{
		messages:  []*azqueue.DequeuedMessage{message("m1", "pop1", b64(preemptionEventJSON))},
		deleteErr: errors.New("delete unavailable"),
	}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || !results[0].Preempted {
		t.Fatalf("expected successful classification despite delete failure, got %+v", results)
	}
}

func TestPollSkipsMessageWithoutID(t *testing.T) {
	fake := &fakeQueueClient{messages: []*azqueue.DequeuedMessage{{MessageText: strPtr(b64(preemptionEventJSON))}}}
	c := &Client{Queue: fake}

	results, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("expected an incomplete message to report an error, got %+v", results)
	}
}
