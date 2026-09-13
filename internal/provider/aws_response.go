package provider

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/middleware"
)

// EC2's SDK accepts an omitted result set as an empty list, and retrieves success
// request IDs only from headers. Verify the actual query response envelope so an
// incomplete reply cannot masquerade as absence; normalize its body request ID.
type awsEC2HTTPClient struct{ base aws.HTTPClient }

func (c *awsEC2HTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := c.base.Do(request)
	if err != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, errors.New("AWS response unavailable or excessive")
	}
	operation := middleware.GetOperationName(request.Context())
	requestID, err := awsEC2Envelope(data, operation)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	if response.Header.Get("X-Amzn-Requestid") == "" && response.Header.Get("X-Amz-RequestId") == "" {
		response.Header.Set("X-Amzn-Requestid", requestID)
	}
	return response, nil
}

func awsEC2Envelope(data []byte, operation string) (string, error) {
	required := map[string]string{
		"DescribeInstances": "reservationSet", "DescribeVolumes": "volumeSet",
		"DescribeNetworkInterfaces": "networkInterfaceSet", "DescribeImages": "imagesSet",
		"RunInstances": "instancesSet", "TerminateInstances": "instancesSet",
		"DeleteVolume": "return", "DeleteNetworkInterface": "return",
	}[operation]
	if required == "" {
		return "", errors.New("unsupported AWS response operation")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	roots := 0
	fields := map[string]int{}
	requestID := ""
	accepted := false
	invalid := func() (string, error) { return "", errors.New("AWS response envelope incomplete or invalid") }
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
				if roots != 1 || value.Name.Local != operation+"Response" || strings.TrimSuffix(value.Name.Space, "/") != "http://ec2.amazonaws.com/doc/2016-11-15" {
					return invalid()
				}
			}
			if depth == 2 {
				fields[value.Name.Local]++
				if fields[value.Name.Local] > 1 {
					return invalid()
				}
				if value.Name.Local == "requestId" || value.Name.Local == "return" {
					var text string
					if err := decoder.DecodeElement(&text, &value); err != nil {
						return invalid()
					}
					depth--
					if value.Name.Local == "requestId" {
						requestID = strings.TrimSpace(text)
					} else {
						accepted = strings.TrimSpace(text) == "true"
					}
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
	if depth != 0 || roots != 1 || requestID == "" || fields[required] != 1 || required == "return" && !accepted {
		return invalid()
	}
	return requestID, nil
}
