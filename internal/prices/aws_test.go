package prices

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// fixtureAWS is a minimal EC2 query-protocol fixture for DescribeSpotPriceHistory
// only; it does not model any other EC2 operation.
type fixtureAWS struct {
	server *httptest.Server
	mu     sync.Mutex
	body   string // Raw <spotPriceHistorySet> contents for the next response.
	status int
}

func newFixtureAWS(t *testing.T) *fixtureAWS {
	t.Helper()
	f := &fixtureAWS{status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fixtureAWS) setResponse(status int, spotPriceHistorySet string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, spotPriceHistorySet
}
func (f *fixtureAWS) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Form.Get("Action") != "DescribeSpotPriceHistory" {
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(f.status)
	if f.status != http.StatusOK {
		fmt.Fprint(w, `<Response><Errors><Error><Code>fixture</Code><Message>fixture</Message></Error></Errors><RequestID>fixture</RequestID></Response>`)
		return
	}
	fmt.Fprintf(w, `<DescribeSpotPriceHistoryResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><spotPriceHistorySet>%s</spotPriceHistorySet></DescribeSpotPriceHistoryResponse>`, f.body)
}

func testClient(f *fixtureAWS) *AWSSpotClient {
	return &AWSSpotClient{
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "fixture-id", SecretAccessKey: "fixture-secret"}, nil
		}),
		HTTPClient: f.server.Client(),
		Endpoint:   f.server.URL,
	}
}

const spotItem = `<item><instanceType>m5.large</instanceType><productDescription>Linux/UNIX</productDescription><spotPrice>0.083400000</spotPrice><timestamp>2026-09-13T12:00:00.000Z</timestamp><availabilityZone>us-east-1a</availabilityZone></item>`

func TestObserveMapsSuccessfulSpotPrice(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, spotItem)
	before := time.Now()
	quote, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large")
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 83400 {
		t.Errorf("PriceMicros = %d, want 83400", quote.PriceMicros)
	}
	if quote.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", quote.Currency)
	}
	// The fixture's <timestamp> (2026-09-13T12:00:00Z) marks when this spot
	// price last changed, not when it was observed - AWS's own DescribeSpotPriceHistory
	// doc never calls it a freshness/observation timestamp, and most spot
	// pools go long stretches with an unchanged price. Using it as
	// ObservedAt would let a routinely-stale-looking timestamp mark a
	// just-fetched, fully current price as stale to placement.Choose's
	// freshness check - exactly the reasoning Azure's own Observe already
	// documents for the identical problem with effectiveStartDate.
	// ObservedAt must be this successful request's own completion time.
	if quote.ObservedAt.Before(before) || quote.ObservedAt.After(after) {
		t.Errorf("ObservedAt = %v, want between %v and %v (request time, not the price's own last-changed timestamp)", quote.ObservedAt, before, after)
	}
}

func TestObserveRejectsEmptyHistory(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, "")
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for empty spot price history")
	}
}

func TestObserveRejectsWrongPool(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, spotItem)
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1b", "m5.large"); err == nil {
		t.Fatal("expected error when the returned entry does not match the requested zone")
	}
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.xlarge"); err == nil {
		t.Fatal("expected error when the returned entry does not match the requested instance type")
	}
}

func TestObserveRejectsMissingPrice(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, `<item><instanceType>m5.large</instanceType><productDescription>Linux/UNIX</productDescription><timestamp>2026-09-13T12:00:00.000Z</timestamp><availabilityZone>us-east-1a</availabilityZone></item>`)
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for a response missing a spot price")
	}
}

func TestObserveRejectsMissingTimestamp(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, `<item><instanceType>m5.large</instanceType><productDescription>Linux/UNIX</productDescription><spotPrice>0.083400000</spotPrice><availabilityZone>us-east-1a</availabilityZone></item>`)
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for a response missing a timestamp")
	}
}

func TestObserveRejectsMultipleAmbiguousEntries(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusOK, spotItem+spotItem)
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for an ambiguous multi-entry response")
	}
}

func TestObserveRejectsNonOKStatus(t *testing.T) {
	f := newFixtureAWS(t)
	f.setResponse(http.StatusInternalServerError, "")
	if _, err := testClient(f).Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for a non-200 response")
	}
}

func TestObserveRejectsMissingCredentials(t *testing.T) {
	c := &AWSSpotClient{}
	if _, err := c.Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error when no credentials provider is configured")
	}
}

func TestObserveRejectsMissingRegionZoneOrInstanceType(t *testing.T) {
	f := newFixtureAWS(t)
	c := testClient(f)
	cases := [][3]string{{"", "us-east-1a", "m5.large"}, {"us-east-1", "", "m5.large"}, {"us-east-1", "us-east-1a", ""}}
	for _, tc := range cases {
		if _, err := c.Observe(context.Background(), tc[0], tc[1], tc[2]); err == nil {
			t.Fatalf("expected error for incomplete request %v", tc)
		}
	}
}
