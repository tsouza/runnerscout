package provider

import (
	"testing"
)

func TestAWSEnvelopeRequiresCompleteInventory(t *testing.T) {
	for _, namespace := range []string{"http://ec2.amazonaws.com/doc/2016-11-15/", "http://ec2.amazonaws.com/doc/2016-11-15"} {
		body := []byte(`<DescribeInstancesResponse xmlns="` + namespace + `"><reservationSet/><requestId>request</requestId></DescribeInstancesResponse>`)
		if id, err := awsEC2Envelope(body, "DescribeInstances"); err != nil || id != "request" {
			t.Fatal("valid EC2 namespace rejected", id, err)
		}
	}
	for _, body := range []string{
		`<DescribeInstancesResponse><reservationSet/><requestId>request</requestId></DescribeInstancesResponse>`,
		`<DescribeInstancesResponse xmlns="https://foreign.invalid/"><reservationSet/><requestId>request</requestId></DescribeInstancesResponse>`,
		`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>request</requestId></DescribeInstancesResponse>`,
		`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet/></DescribeInstancesResponse>`,
		`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet/><reservationSet/><requestId>request</requestId></DescribeInstancesResponse>`,
	} {
		if _, err := awsEC2Envelope([]byte(body), "DescribeInstances"); err == nil {
			t.Fatal("invalid inventory accepted", body)
		}
	}
}
