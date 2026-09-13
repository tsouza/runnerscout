package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const spotPriceHistoryFixture = `<DescribeSpotPriceHistoryResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><spotPriceHistorySet><item><instanceType>m5.large</instanceType><productDescription>Linux/UNIX</productDescription><spotPrice>0.083400000</spotPrice><timestamp>2026-09-13T12:00:00.000Z</timestamp><availabilityZone>us-east-1a</availabilityZone></item></spotPriceHistorySet></DescribeSpotPriceHistoryResponse>`

func TestAWSSpotPricesObservesUsingSameCredentialScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("Action") != "DescribeSpotPriceHistory" {
			t.Errorf("unexpected AWS operation %q", r.Form.Get("Action"))
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, spotPriceHistoryFixture)
	}))
	defer server.Close()
	sdk := &AWSSDK{scope: &awsCredentialScope{values: map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}, client: server.Client(), endpoint: server.URL}}
	quote, err := sdk.SpotPrices().Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large")
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 83400 || quote.Currency != "USD" {
		t.Fatalf("unexpected quote: %+v", quote)
	}
}

func TestAWSSpotPricesNeverRunsAmbientOrSSOFallback(t *testing.T) {
	sdk := &AWSSDK{scope: &awsCredentialScope{}} // no explicit credential source configured
	if _, err := sdk.SpotPrices().Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error when no explicit AWS credentials are configured")
	}
}

func TestAWSSpotPricesRequiresConfiguredSDK(t *testing.T) {
	var observer *AWSSpotPrices
	if _, err := observer.Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error for a nil observer")
	}
	if _, err := (&AWSSDK{}).SpotPrices().Observe(context.Background(), "us-east-1", "us-east-1a", "m5.large"); err == nil {
		t.Fatal("expected error when the SDK has no credential scope")
	}
}
