// Package testutil supplies stateful protocol fixtures for controller tests.
package testutil

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const AWSInstanceID = "i-0123456789abcdef0"
const AWSVolumeID = "vol-0123456789abcdef0"
const AWSInterfaceID = "eni-0123456789abcdef0"

// AWSPrivateIP is the fixture instance's private IP, as AWS itself would
// assign it from the requested subnet's address range and return
// synchronously in the same RunInstances response the real createAWS
// already parses.
const AWSPrivateIP = "10.60.5.42"

type AWS struct {
	Server                                            *httptest.Server
	mu                                                sync.Mutex
	requests                                          []url.Values
	instance, terminated, volume, network             bool
	loseCreate, capacity, spotInterrupted             bool
	account, allocation, zone, subnet, machine, image string
	owners                                            map[string]string
}

func NewAWS(t *testing.T) *AWS {
	t.Helper()
	fixture := &AWS{account: "000000000000", owners: map[string]string{}}
	fixture.Server = httptest.NewTLSServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(fixture.Server.Close)
	return fixture
}
func (f *AWS) LoseNextCreate() { f.mu.Lock(); defer f.mu.Unlock(); f.loseCreate = true }
func (f *AWS) RejectCapacity() { f.mu.Lock(); defer f.mu.Unlock(); f.capacity = true }

// SpotInterrupt models the settled state some time after AWS itself (not any
// TerminateInstances call the controller made) terminates the instance with
// the definitive spot interruption reason code; its delete-on-termination
// volume and network interface have since been removed by AWS as well.
func (f *AWS) SpotInterrupt() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated, f.spotInterrupted, f.volume, f.network = true, true, false, false
}
func (f *AWS) SetAccount(account string) { f.mu.Lock(); defer f.mu.Unlock(); f.account = account }
func (f *AWS) Requests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]url.Values, len(f.requests))
	for i, r := range f.requests {
		result[i] = url.Values{}
		for key, values := range r {
			result[i][key] = append([]string{}, values...)
		}
	}
	return result
}
func (f *AWS) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Form)
	action := r.Form.Get("Action")
	w.Header().Set("Content-Type", "text/xml")
	write := func(body string) {
		fmt.Fprintf(w, `<%sResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId>%s</%sResponse>`, action, body, action)
	}
	failure := func(code string) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>fixture</Message></Error></Errors><RequestID>fixture</RequestID></Response>`, code)
	}
	switch action {
	case "GetCallerIdentity":
		fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>%s</Account><Arn>arn:aws:iam::%s:user/fixture</Arn><UserId>fixture</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>fixture</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, f.account, f.account)
	case "DescribeImages":
		write(`<imagesSet><item><imageId>ami-test</imageId><imageState>available</imageState><architecture>x86_64</architecture><rootDeviceType>ebs</rootDeviceType><rootDeviceName>/dev/sda1</rootDeviceName><blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><snapshotId>snap-0123456789abcdef0</snapshotId><volumeSize>8</volumeSize><volumeType>gp3</volumeType></ebs></item></blockDeviceMapping></item></imagesSet>`)
	case "RunInstances":
		if f.capacity {
			failure("InsufficientInstanceCapacity")
			return
		}
		f.instance, f.volume, f.network = true, true, true
		f.allocation = r.Form.Get("ClientToken")
		f.zone = r.Form.Get("Placement.AvailabilityZone")
		f.subnet = r.Form.Get("NetworkInterface.1.SubnetId")
		f.machine = r.Form.Get("InstanceType")
		f.image = r.Form.Get("ImageId")
		for i := 1; i <= 4; i++ {
			prefix := fmt.Sprintf("TagSpecification.%d.", i)
			kind := r.Form.Get(prefix + "ResourceType")
			for j := 1; j <= 4; j++ {
				tag := fmt.Sprintf("%sTag.%d.", prefix, j)
				if r.Form.Get(tag+"Key") == "runnerscout-owner" {
					f.owners[kind] = r.Form.Get(tag + "Value")
				}
			}
		}
		if f.loseCreate {
			f.loseCreate = false
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		write(`<ownerId>` + f.account + `</ownerId><instancesSet>` + f.instanceXML() + `</instancesSet>`)
	case "DescribeInstances":
		id := r.Form.Get("InstanceId.1")
		if id != "" && (!f.instance || id != AWSInstanceID) {
			failure("InvalidInstanceID.NotFound")
			return
		}
		if !f.instance {
			write(`<reservationSet/>`)
			return
		}
		write(`<reservationSet><item><ownerId>` + f.account + `</ownerId><instancesSet>` + f.instanceXML() + `</instancesSet></item></reservationSet>`)
	case "DescribeVolumes":
		id := r.Form.Get("VolumeId.1")
		if id != "" && (!f.volume || id != AWSVolumeID) {
			failure("InvalidVolume.NotFound")
			return
		}
		if !f.volume {
			write(`<volumeSet/>`)
			return
		}
		state, attachment := "available", ""
		if !f.terminated {
			state = "in-use"
			attachment = `<item><volumeId>` + AWSVolumeID + `</volumeId><instanceId>` + AWSInstanceID + `</instanceId><status>attached</status></item>`
		}
		write(`<volumeSet><item><volumeId>` + AWSVolumeID + `</volumeId><availabilityZone>` + f.zone + `</availabilityZone><status>` + state + `</status><tagSet>` + f.tags("volume") + `</tagSet><attachmentSet>` + attachment + `</attachmentSet></item></volumeSet>`)
	case "DescribeNetworkInterfaces":
		id := r.Form.Get("NetworkInterfaceId.1")
		if id != "" && (!f.network || id != AWSInterfaceID) {
			failure("InvalidNetworkInterfaceID.NotFound")
			return
		}
		if !f.network {
			write(`<networkInterfaceSet/>`)
			return
		}
		state, attachment := "available", ""
		if !f.terminated {
			state = "in-use"
			attachment = `<attachment><instanceId>` + AWSInstanceID + `</instanceId><deleteOnTermination>true</deleteOnTermination></attachment>`
		}
		write(`<networkInterfaceSet><item><networkInterfaceId>` + AWSInterfaceID + `</networkInterfaceId><ownerId>` + f.account + `</ownerId><availabilityZone>` + f.zone + `</availabilityZone><subnetId>` + f.subnet + `</subnetId><status>` + state + `</status><tagSet>` + f.tags("network-interface") + `</tagSet>` + attachment + `</item></networkInterfaceSet>`)
	case "TerminateInstances":
		if !f.instance || r.Form.Get("InstanceId.1") != AWSInstanceID {
			failure("InvalidInstanceID.NotFound")
			return
		}
		f.terminated = true
		write(`<instancesSet><item><instanceId>` + AWSInstanceID + `</instanceId><currentState><code>48</code><name>terminated</name></currentState><previousState><code>16</code><name>running</name></previousState></item></instancesSet>`)
	case "DeleteVolume":
		if !f.volume || r.Form.Get("VolumeId") != AWSVolumeID {
			failure("InvalidVolume.NotFound")
			return
		}
		if !f.terminated {
			failure("VolumeInUse")
			return
		}
		f.volume = false
		write(`<return>true</return>`)
	case "DeleteNetworkInterface":
		if !f.network || r.Form.Get("NetworkInterfaceId") != AWSInterfaceID {
			failure("InvalidNetworkInterfaceID.NotFound")
			return
		}
		if !f.terminated {
			failure("InvalidParameterValue")
			return
		}
		f.network = false
		write(`<return>true</return>`)
	default:
		failure("UnsupportedOperation")
	}
}
func (f *AWS) tags(kind string) string {
	if f.owners[kind] == "" {
		return ""
	}
	return `<item><key>runnerscout-owner</key><value>` + f.owners[kind] + `</value></item><item><key>runnerscout-operation</key><value>` + f.allocation + `</value></item>`
}
func (f *AWS) instanceXML() string {
	state, code := "running", "16"
	if f.terminated {
		state, code = "terminated", "48"
	}
	stateReason := ""
	if f.spotInterrupted {
		stateReason = `<stateReason><code>Server.SpotInstanceTermination</code><message>interrupted</message></stateReason>`
	}
	return strings.Join([]string{`<item><instanceId>`, AWSInstanceID, `</instanceId><imageId>`, f.image, `</imageId><instanceType>`, f.machine, `</instanceType><clientToken>`, f.allocation, `</clientToken><subnetId>`, f.subnet, `</subnetId><placement><availabilityZone>`, f.zone, `</availabilityZone></placement><instanceState><code>`, code, `</code><name>`, state, `</name></instanceState>`, stateReason, `<tagSet>`, f.tags("instance"), `</tagSet><rootDeviceName>/dev/sda1</rootDeviceName><blockDeviceMapping><item><deviceName>/dev/sda1</deviceName><ebs><volumeId>`, AWSVolumeID, `</volumeId><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping><networkInterfaceSet><item><networkInterfaceId>`, AWSInterfaceID, `</networkInterfaceId><subnetId>`, f.subnet, `</subnetId><privateIpAddress>`, AWSPrivateIP, `</privateIpAddress></item></networkInterfaceSet></item>`}, "")
}
