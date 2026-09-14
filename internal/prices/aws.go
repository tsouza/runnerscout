package prices

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	smithymiddleware "github.com/aws/smithy-go/middleware"
)

// AWSSpotClient observes EC2 Spot Instance prices via DescribeSpotPriceHistory,
// the same per-instance-type-per-AZ pricing surface EC2 already depends on -
// no separate Pricing API SDK module is needed. Credentials are supplied by
// the caller rather than resolved here, matching how AttemptJobs takes
// owner/repo per call instead of fixing them at construction: this client
// never runs its own credential process, config-file search or SSO exchange.
type AWSSpotClient struct {
	Credentials aws.CredentialsProvider
	HTTPClient  aws.HTTPClient
	Endpoint    string // Set only by local API fixtures, never from the environment.
}

func (*AWSSpotClient) String() string   { return "AWS spot price client (credentials redacted)" }
func (*AWSSpotClient) GoString() string { return "AWS spot price client (credentials redacted)" }

func (c *AWSSpotClient) client(region string) (*ec2.Client, error) {
	if c == nil || c.Credentials == nil || region == "" {
		return nil, errors.New("AWS pricing client configuration unavailable")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	config := aws.Config{Region: region, Credentials: c.Credentials, HTTPClient: &awsSpotHTTPClient{base: httpClient}, RetryMaxAttempts: 1}
	return ec2.NewFromConfig(config, func(options *ec2.Options) {
		if c.Endpoint != "" {
			options.BaseEndpoint = aws.String(c.Endpoint)
		}
	}), nil
}

// Observe returns the current Linux/UNIX Spot price for one instance type in
// one Availability Zone. It never returns a stale or synthesized price: an
// empty, ambiguous or otherwise incomplete response is always an error.
func (c *AWSSpotClient) Observe(ctx context.Context, region, zone, instanceType string) (Quote, error) {
	if zone == "" || instanceType == "" {
		return Quote{}, errors.New("AWS spot price request incomplete")
	}
	client, err := c.client(region)
	if err != nil {
		return Quote{}, err
	}
	response, err := client.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceType(instanceType)},
		AvailabilityZone:    aws.String(zone),
		ProductDescriptions: []string{string(types.RIProductDescriptionLinuxUnix)},
		StartTime:           aws.Time(time.Now()),
		MaxResults:          aws.Int32(1),
	})
	if ctx.Err() != nil {
		return Quote{}, ctx.Err()
	}
	if err != nil || response == nil || !awsSpotResponseKnown(response.ResultMetadata) {
		return Quote{}, errors.New("AWS spot price observation unavailable")
	}
	if len(response.SpotPriceHistory) != 1 {
		return Quote{}, errors.New("AWS spot price history has no entry for the requested pool")
	}
	entry := response.SpotPriceHistory[0]
	if aws.ToString(entry.AvailabilityZone) != zone || string(entry.InstanceType) != instanceType || entry.ProductDescription != types.RIProductDescriptionLinuxUnix || entry.Timestamp == nil || entry.Timestamp.IsZero() {
		return Quote{}, errors.New("AWS spot price observation malformed")
	}
	micros, err := awsSpotPriceMicros(aws.ToString(entry.SpotPrice))
	if err != nil {
		return Quote{}, err
	}
	// entry.Timestamp marks when this spot price last changed, not when it
	// was observed - most spot pools go long stretches with an unchanged
	// price, so using it here would let a routinely-stale-looking
	// timestamp mark a just-fetched, fully current price as stale to
	// placement.Choose's freshness check. See Observe's own doc comment;
	// Azure's Observe documents the identical reasoning for
	// effectiveStartDate. ObservedAt is therefore this successful
	// request's own completion time, not entry.Timestamp - which is still
	// validated as non-zero above, since that check is a genuine
	// malformed-response guard, independent of which timestamp gets used.
	return Quote{PriceMicros: micros, Currency: "USD", ObservedAt: time.Now()}, nil
}

func awsSpotResponseKnown(metadata smithymiddleware.Metadata) bool {
	id, ok := awsmiddleware.GetRequestIDMetadata(metadata)
	return ok && id != ""
}

// AWS spot prices carry far fewer significant digits than a float64 can
// represent exactly, so parsing to float64 before scaling to micros never
// loses precision.
func awsSpotPriceMicros(value string) (int64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("AWS spot price value invalid")
	}
	return int64(math.Round(parsed * 1e6)), nil
}

// EC2's SDK retrieves success request IDs only from headers, and accepts an
// omitted result set as an empty list. Verify the actual query response
// envelope so an incomplete reply cannot masquerade as an empty history, and
// normalize its body request ID onto the header the SDK reads.
type awsSpotHTTPClient struct{ base aws.HTTPClient }

func (c *awsSpotHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.base.Do(request)
	if err != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("AWS spot price response unavailable or excessive")
	}
	requestID, err := awsSpotPriceEnvelope(data)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	if response.Header.Get("X-Amzn-Requestid") == "" && response.Header.Get("X-Amz-RequestId") == "" {
		response.Header.Set("X-Amzn-Requestid", requestID)
	}
	return response, nil
}

// DescribeSpotPriceHistory's XML envelope follows the same EC2 query-protocol
// shape as every other operation: exactly one root spotPriceHistorySet field
// (present but empty when there is no match) and exactly one requestId.
func awsSpotPriceEnvelope(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	fields := map[string]int{}
	requestID := ""
	invalid := func() (string, error) {
		return "", errors.New("AWS spot price response envelope incomplete or invalid")
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid()
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if roots != 1 || value.Name.Local != "DescribeSpotPriceHistoryResponse" || strings.TrimSuffix(value.Name.Space, "/") != "http://ec2.amazonaws.com/doc/2016-11-15" {
					return invalid()
				}
			}
			if depth == 2 {
				fields[value.Name.Local]++
				if fields[value.Name.Local] > 1 {
					return invalid()
				}
				if value.Name.Local == "requestId" {
					var text string
					if err := decoder.DecodeElement(&text, &value); err != nil {
						return invalid()
					}
					depth--
					requestID = strings.TrimSpace(text)
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return invalid()
			}
		}
	}
	if depth != 0 || roots != 1 || requestID == "" || fields["spotPriceHistorySet"] != 1 {
		return invalid()
	}
	return requestID, nil
}
