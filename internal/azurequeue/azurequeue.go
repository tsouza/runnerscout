// Package azurequeue polls an Azure Storage Queue for the
// Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated
// CloudEvents payloads Event Grid delivers there, and hands each message
// body to internal/azureevents.ParsePreemptionEvent.
//
// It never creates, deletes or subscribes the queue, the Event Grid system
// topic or its event subscription - provisioning that delivery path is a
// human/install-doc responsibility, not this client's or the controller's
// (see docs/azure-interruption-delivery.md). It never imports
// internal/operator, internal/provider or internal/lifecycle, and it never
// correlates a returned resource ID against a specific RunnerScout
// allocation - mirroring internal/azureevents' own dependency-free,
// single-purpose style. Wiring a real caller that constructs a Client and
// feeds its Result values into a provider Observation remains a separate,
// later decision.
package azurequeue

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"

	"github.com/tsouza/runnerscout/internal/azureevents"
)

// defaultMaxMessages is Azure Storage Queue's own documented maximum
// messages-per-request (GetMessages numofmessages), used whenever Client's
// MaxMessages is left at its zero value.
const defaultMaxMessages = 32

// QueueClient is the subset of *azqueue.QueueClient this package calls.
// Declaring it here (rather than depending on the concrete SDK type)
// mirrors internal/operator's awsPriceObserver/azurePriceObserver
// dependency-injection shape, and lets tests substitute an in-memory fake
// with no HTTP transport at all - *azqueue.QueueClient already satisfies
// this interface structurally.
type QueueClient interface {
	DequeueMessages(ctx context.Context, o *azqueue.DequeueMessagesOptions) (azqueue.DequeueMessagesResponse, error)
	DeleteMessage(ctx context.Context, messageID, popReceipt string, o *azqueue.DeleteMessageOptions) (azqueue.DeleteMessageResponse, error)
}

// New wraps a real Azure Storage Queue in a Client, authenticating with the
// given credential (typically the same azcore.TokenCredential chain
// internal/provider.AzureSDK.credentials() already selects). queueURL is
// the full queue endpoint, e.g.
// "https://<account>.queue.core.windows.net/<queue>".
func New(queueURL string, cred azcore.TokenCredential, options *azqueue.ClientOptions) (*Client, error) {
	q, err := azqueue.NewQueueClient(queueURL, cred, options)
	if err != nil {
		return nil, err
	}
	return &Client{Queue: q}, nil
}

// Client polls one Azure Storage Queue on demand. It keeps no background
// goroutine or open connection of its own: Storage Queue's GetMessages
// operation has no long-poll or streaming mode, so - like
// internal/githubjobs and internal/prices - every Poll call is a single
// bounded synchronous request, meant to be driven by a caller's own
// interval (e.g. internal/operator.Operator.Tick's cadence), not a
// persistent session such as github.com/actions/scaleset's
// MessageSessionClient/listener.
type Client struct {
	// Queue is required. Production callers build it with New; tests
	// substitute a fake implementing QueueClient directly.
	Queue QueueClient

	// MaxMessages caps how many messages one Poll call dequeues (Storage
	// Queue's GetMessages "numofmessages" parameter). Zero means
	// defaultMaxMessages.
	MaxMessages int32

	// VisibilityTimeoutSeconds is the dequeued messages' visibility
	// timeout, in seconds. Zero leaves it unset, so the Storage Queue REST
	// API's own default (30 seconds) applies.
	VisibilityTimeoutSeconds int32
}

// Result reports the outcome of classifying one dequeued message.
type Result struct {
	// ResourceID is the annotated VM's ARM resource ID (azureevents'
	// armResourceID convention). Empty when Err is set.
	ResourceID string
	// Preempted is true only for a confirmed VirtualMachinePreempted
	// annotation. Meaningless when Err is set.
	Preempted bool
	// Err is set when this specific message could not be classified (a
	// missing message identity, or ParsePreemptionEvent's own fail-closed
	// error). A message with Err set is deliberately left in the queue -
	// see Poll's doc comment.
	Err error
}

// Poll dequeues up to MaxMessages currently-visible messages in one bounded
// request and classifies each with azureevents.ParsePreemptionEvent.
//
// A message that classifies successfully - confirmed preempted, or a
// legitimate different annotation/event - is deleted immediately: it has
// been definitively handled and must not be redelivered. A message that
// fails to classify (malformed payload, or an event type ParsePreemptionEvent
// itself does not recognize) is left in the queue undeleted, so it survives
// for investigation and eventual operator attention via its rising
// DequeueCount, exactly as azureevents.ParsePreemptionEvent's own doc
// comment insists on failing closed rather than silently discarding
// ambiguity. Deleting a successfully-classified message is best-effort: if
// the delete call itself fails, the classification is still reported,
// because Storage Queue's at-least-once redelivery makes a future duplicate
// report of the same fact harmless (see
// docs/azure-interruption-delivery.md).
//
// Poll returns an error only when the dequeue request itself fails; no
// partial results are returned in that case.
func (c *Client) Poll(ctx context.Context) ([]Result, error) {
	if c.Queue == nil {
		return nil, errors.New("azurequeue: Client.Queue is required")
	}
	max := c.MaxMessages
	if max == 0 {
		max = defaultMaxMessages
	}
	options := &azqueue.DequeueMessagesOptions{NumberOfMessages: &max}
	if c.VisibilityTimeoutSeconds != 0 {
		timeout := c.VisibilityTimeoutSeconds
		options.VisibilityTimeout = &timeout
	}
	response, err := c.Queue.DequeueMessages(ctx, options)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(response.Messages))
	for _, m := range response.Messages {
		result := c.classify(m)
		results = append(results, result)
		if result.Err != nil {
			continue
		}
		if m.MessageID == nil || m.PopReceipt == nil {
			continue
		}
		// Best-effort: see Poll's doc comment above.
		_, _ = c.Queue.DeleteMessage(ctx, *m.MessageID, *m.PopReceipt, nil)
	}
	return results, nil
}

// classify decodes and parses one dequeued message. It fails closed: an
// incomplete message identity is itself a classification failure, since
// Poll could not safely delete such a message even if it wanted to.
func (c *Client) classify(m *azqueue.DequeuedMessage) Result {
	if m == nil || m.MessageID == nil || m.PopReceipt == nil || m.MessageText == nil {
		return Result{Err: errors.New("azurequeue: dequeued message identity incomplete")}
	}
	data := decodeMessageBody(*m.MessageText)
	resourceID, preempted, err := azureevents.ParsePreemptionEvent(data)
	if err != nil {
		return Result{Err: err}
	}
	return Result{ResourceID: resourceID, Preempted: preempted}
}

// decodeMessageBody returns the CloudEvents JSON payload for a dequeued
// message's raw text. Event Grid base64-encodes message content it
// delivers to a Storage Queue (Storage Queue's classic REST API is not
// itself always safe for arbitrary bytes/control characters); this is
// tried first. If the text is not valid base64, or the decoded bytes are
// not a JSON object, the raw text is tried as-is instead, so a
// differently-configured subscription or local emulator that delivers
// unencoded payloads still parses correctly. Either way,
// azureevents.ParsePreemptionEvent still fails closed on a truly malformed
// payload - this function only chooses which bytes to hand it.
func decodeMessageBody(text string) []byte {
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil && looksLikeJSONObject(decoded) {
		return decoded
	}
	return []byte(text)
}

func looksLikeJSONObject(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}
