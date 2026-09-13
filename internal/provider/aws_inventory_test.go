package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

func TestAWSRecordedVolumeSurvivesDiscoveryLoss(t *testing.T) {
	for _, mode := range []string{"present", "tag-lost", "absent", "foreign-attachment", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var direct atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "text/xml")
				errorResponse := func(code string) {
					w.WriteHeader(400)
					fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>fixture</Message></Error></Errors><RequestID>fixture</RequestID></Response>`, code)
				}
				switch r.Form.Get("Action") {
				case "GetCallerIdentity":
					fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>000000000000</Account><Arn>arn:aws:iam::000000000000:user/fixture</Arn><UserId>fixture</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
				case "DescribeInstances":
					if r.Form.Get("InstanceId.1") != "i-recorded" {
						t.Error("did not query recorded instance")
					}
					errorResponse("InvalidInstanceID.NotFound")
				case "DescribeVolumes":
					if r.Form.Get("VolumeId.1") == "" {
						// Discovery no longer returns the disk. Its saved ID is the
						// only path to independent observation and cleanup.
						fmt.Fprint(w, `<DescribeVolumesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><volumeSet/></DescribeVolumesResponse>`)
						return
					}
					direct.Add(1)
					if r.Form.Get("VolumeId.1") != "vol-recorded" {
						t.Error("queried another volume")
					}
					if mode == "absent" {
						errorResponse("InvalidVolume.NotFound")
						return
					}
					if mode == "malformed" {
						fmt.Fprint(w, `<DescribeVolumesResponse/>`)
						return
					}
					tags := `<item><key>runnerscout-owner</key><value>test</value></item><item><key>runnerscout-operation</key><value>rs-test</value></item>`
					if mode == "tag-lost" {
						tags = ""
					}
					attachment := ""
					if mode == "foreign-attachment" {
						attachment = `<item><instanceId>i-foreign</instanceId><volumeId>vol-recorded</volumeId><status>attached</status></item>`
					}
					fmt.Fprintf(w, `<DescribeVolumesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><volumeSet><item><volumeId>vol-recorded</volumeId><availabilityZone>us-east-1a</availabilityZone><status>available</status><tagSet>%s</tagSet><attachmentSet>%s</attachmentSet></item></volumeSet></DescribeVolumesResponse>`, tags, attachment)
				case "DescribeNetworkInterfaces":
					fmt.Fprint(w, `<DescribeNetworkInterfacesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><networkInterfaceSet/></DescribeNetworkInterfacesResponse>`)
				default:
					t.Error("unexpected cloud effect", r.Form.Get("Action"))
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			p := Command{Config: credentialConfig("aws")}
			sdk := &AWSSDK{scope: &awsCredentialScope{values: map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}, client: server.Client(), endpoint: server.URL}}
			client, err := sdk.session(context.Background(), p.Config, "us-east-1")
			if err != nil {
				t.Fatal(err)
			}
			a := allocation()
			a.ResourceID = "i-recorded"
			a.Resources = []lifecycle.ResourceReference{{Kind: "aws-volume", ID: "vol-recorded"}}
			inventory, err := p.awsInventory(context.Background(), client, a)
			if mode == "tag-lost" || mode == "foreign-attachment" || mode == "malformed" {
				if err == nil {
					t.Fatal("uncertain recorded volume was accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				ob, err := inventory.observation(a)
				if err != nil || !ob.Known || ob.Exists != (mode == "present") || len(ob.Resources) != 1 || ob.Resources[0].ID != "vol-recorded" {
					t.Fatal("recorded obligation was dropped or falsely classified", ob, err)
				}
			}
			if direct.Load() != 1 {
				t.Fatal("recorded volume was not independently queried", direct.Load())
			}
		})
	}
}
