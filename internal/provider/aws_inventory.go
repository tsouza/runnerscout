package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	smithymiddleware "github.com/aws/smithy-go/middleware"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

type awsInventory struct {
	instance   *types.Instance
	volumes    map[string]types.Volume
	interfaces map[string]types.NetworkInterface
}

func awsOwned(tags []types.Tag, owner, allocation string) bool {
	values := map[string]string{}
	for _, tag := range tags {
		key := aws.ToString(tag.Key)
		if _, duplicate := values[key]; duplicate {
			return false
		}
		values[key] = aws.ToString(tag.Value)
	}
	return values["runnerscout-owner"] == owner && values["runnerscout-operation"] == allocation
}

func awsOwnershipFilters(owner, allocation string) []types.Filter {
	return []types.Filter{
		{Name: aws.String("tag:runnerscout-owner"), Values: []string{owner}},
		{Name: aws.String("tag:runnerscout-operation"), Values: []string{allocation}},
	}
}

func awsNext(token *string, seen map[string]bool) (bool, error) {
	value := aws.ToString(token)
	if value == "" {
		return false, nil
	}
	if seen[value] || len(seen) >= 100 {
		return false, errors.New("AWS inventory pagination invalid or excessive")
	}
	seen[value] = true
	return true, nil
}

func (p *Command) awsInventory(ctx context.Context, client *ec2.Client, a lifecycle.Allocation) (awsInventory, error) {
	for _, resource := range a.Resources {
		if resource.Kind != "aws-volume" && resource.Kind != "aws-network-interface" {
			return awsInventory{}, errors.New("unsupported AWS dependency kind")
		}
	}
	result := awsInventory{volumes: map[string]types.Volume{}, interfaces: map[string]types.NetworkInterface{}}
	request := &ec2.DescribeInstancesInput{Filters: []types.Filter{{Name: aws.String("client-token"), Values: []string{a.ID}}}}
	if a.ResourceID != "" {
		request.InstanceIds = []string{a.ResourceID}
		request.Filters = nil
	}
	seen := map[string]bool{}
	for {
		page, err := client.DescribeInstances(ctx, request)
		if a.ResourceID != "" && awsMissing(err, "InvalidInstanceID.NotFound") {
			break
		}
		if err != nil || page == nil || !awsResponseKnown(page.ResultMetadata) {
			return result, errors.New("AWS instance inventory unavailable")
		}
		if a.ResourceID != "" && len(page.Reservations) == 0 {
			return result, errors.New("AWS instance response omitted requested identity")
		}
		for _, reservation := range page.Reservations {
			if aws.ToString(reservation.OwnerId) != p.Config.AccountID {
				return result, errors.New("AWS reservation account mismatch")
			}
			for _, instance := range reservation.Instances {
				if result.instance != nil || !p.awsInstanceOwned(a, instance) {
					return result, errors.New("AWS instance ownership or identity unconfirmed")
				}
				copy := instance
				result.instance = &copy
			}
		}
		more, err := awsNext(page.NextToken, seen)
		if err != nil {
			return result, err
		}
		if !more {
			break
		}
		request.NextToken = page.NextToken
	}
	if result.instance != nil && a.ResourceID != "" && a.ResourceID != aws.ToString(result.instance.InstanceId) {
		return result, errors.New("AWS durable instance identity changed")
	}
	volumeRequest := &ec2.DescribeVolumesInput{Filters: awsOwnershipFilters(p.Config.Owner, a.ID)}
	seen = map[string]bool{}
	for {
		page, err := client.DescribeVolumes(ctx, volumeRequest)
		if err != nil || page == nil || !awsResponseKnown(page.ResultMetadata) {
			return result, errors.New("AWS volume inventory unavailable")
		}
		for _, volume := range page.Volumes {
			id := aws.ToString(volume.VolumeId)
			if _, duplicate := result.volumes[id]; duplicate || id == "" || !awsOwned(volume.Tags, p.Config.Owner, a.ID) || aws.ToString(volume.AvailabilityZone) != a.Offering.Zone {
				return result, errors.New("AWS volume ownership unconfirmed")
			}
			result.volumes[id] = volume
		}
		more, err := awsNext(page.NextToken, seen)
		if err != nil {
			return result, err
		}
		if !more {
			break
		}
		volumeRequest.NextToken = page.NextToken
	}
	interfaceRequest := &ec2.DescribeNetworkInterfacesInput{Filters: awsOwnershipFilters(p.Config.Owner, a.ID)}
	seen = map[string]bool{}
	for {
		page, err := client.DescribeNetworkInterfaces(ctx, interfaceRequest)
		if err != nil || page == nil || !awsResponseKnown(page.ResultMetadata) {
			return result, errors.New("AWS network interface inventory unavailable")
		}
		for _, network := range page.NetworkInterfaces {
			id := aws.ToString(network.NetworkInterfaceId)
			if _, duplicate := result.interfaces[id]; duplicate || id == "" || !awsOwned(network.TagSet, p.Config.Owner, a.ID) || aws.ToString(network.OwnerId) != p.Config.AccountID || aws.ToString(network.AvailabilityZone) != a.Offering.Zone || aws.ToString(network.SubnetId) != p.Config.Subnet {
				return result, errors.New("AWS network interface ownership unconfirmed")
			}
			result.interfaces[id] = network
		}
		more, err := awsNext(page.NextToken, seen)
		if err != nil {
			return result, err
		}
		if !more {
			break
		}
		interfaceRequest.NextToken = page.NextToken
	}
	references := append([]lifecycle.ResourceReference{}, a.Resources...)
	if result.instance != nil {
		for _, mapping := range result.instance.BlockDeviceMappings {
			if mapping.Ebs != nil {
				references = append(references, lifecycle.ResourceReference{Kind: "aws-volume", ID: aws.ToString(mapping.Ebs.VolumeId)})
			}
		}
		for _, network := range result.instance.NetworkInterfaces {
			references = append(references, lifecycle.ResourceReference{Kind: "aws-network-interface", ID: aws.ToString(network.NetworkInterfaceId)})
		}
	}
	references, _, err := lifecycle.MergeResources(nil, references)
	if err != nil {
		return result, err
	}
	for _, reference := range references {
		if err := p.awsRecordedDependency(ctx, client, a, reference, &result); err != nil {
			return result, err
		}
	}
	if err := result.validateBindings(a.ResourceID); err != nil {
		return result, err
	}
	return result, nil
}

func (p *Command) awsInstanceOwned(a lifecycle.Allocation, instance types.Instance) bool {
	if aws.ToString(instance.InstanceId) == "" || aws.ToString(instance.ClientToken) != a.ID || !awsOwned(instance.Tags, p.Config.Owner, a.ID) || instance.Placement == nil || aws.ToString(instance.Placement.AvailabilityZone) != a.Offering.Zone || instance.State == nil {
		return false
	}
	if instance.State.Name != types.InstanceStateNameTerminated && aws.ToString(instance.SubnetId) != p.Config.Subnet {
		return false
	}
	switch instance.State.Name {
	case types.InstanceStateNamePending, types.InstanceStateNameRunning, types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated, types.InstanceStateNameStopping, types.InstanceStateNameStopped:
		return true
	default:
		return false
	}
}

func (inventory awsInventory) validateBindings(instanceID string) error {
	if inventory.instance != nil {
		instanceID = aws.ToString(inventory.instance.InstanceId)
	}
	for _, volume := range inventory.volumes {
		for _, attachment := range volume.Attachments {
			if instanceID == "" || aws.ToString(attachment.InstanceId) != instanceID {
				return errors.New("AWS volume attached outside its allocation")
			}
		}
	}
	for _, network := range inventory.interfaces {
		if network.Attachment != nil && (instanceID == "" || aws.ToString(network.Attachment.InstanceId) != instanceID) {
			return errors.New("AWS network interface attached outside its allocation")
		}
	}
	if inventory.instance == nil || inventory.instance.State.Name == types.InstanceStateNameTerminated {
		return nil
	}
	for _, mapping := range inventory.instance.BlockDeviceMappings {
		if mapping.Ebs == nil {
			continue
		}
		if _, owned := inventory.volumes[aws.ToString(mapping.Ebs.VolumeId)]; !owned {
			return errors.New("AWS instance has an unowned or unobserved EBS attachment")
		}
	}
	for _, network := range inventory.instance.NetworkInterfaces {
		if _, owned := inventory.interfaces[aws.ToString(network.NetworkInterfaceId)]; !owned {
			return errors.New("AWS instance has an unowned or unobserved network attachment")
		}
	}
	return nil
}

func (inventory awsInventory) observation(a lifecycle.Allocation) (lifecycle.Observation, error) {
	id := a.ResourceID
	if inventory.instance != nil {
		id = aws.ToString(inventory.instance.InstanceId)
	}
	exists := inventory.instance != nil && inventory.instance.State.Name != types.InstanceStateNameTerminated || len(inventory.volumes) > 0 || len(inventory.interfaces) > 0
	if exists && id == "" {
		return lifecycle.Observation{}, errors.New("AWS residual resources retain an unknown instance identity")
	}
	if id != "" && (!strings.HasPrefix(id, "i-") || strings.ContainsAny(id, " /\n\r\t")) {
		return lifecycle.Observation{}, errors.New("AWS durable instance identity invalid")
	}
	references := append([]lifecycle.ResourceReference{}, a.Resources...)
	for id := range inventory.volumes {
		references = append(references, lifecycle.ResourceReference{Kind: "aws-volume", ID: id})
	}
	for id := range inventory.interfaces {
		references = append(references, lifecycle.ResourceReference{Kind: "aws-network-interface", ID: id})
	}
	references, _, err := lifecycle.MergeResources(nil, references)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	return lifecycle.Observation{Known: true, Exists: exists, ResourceID: id, Resources: references}, nil
}

func awsMissing(err error, code string) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == code
}
func awsResponseKnown(metadata smithymiddleware.Metadata) bool {
	id, ok := awsmiddleware.GetRequestIDMetadata(metadata)
	return ok && id != ""
}

// Re-read recorded identities directly. Missing discovery tags cannot erase a
// dependency; foreign tags or bindings retain it without permitting deletion.
func (p *Command) awsRecordedDependency(ctx context.Context, client *ec2.Client, a lifecycle.Allocation, reference lifecycle.ResourceReference, inventory *awsInventory) error {
	switch reference.Kind {
	case "aws-volume":
		if !strings.HasPrefix(reference.ID, "vol-") {
			return errors.New("AWS recorded volume identity invalid")
		}
		page, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{reference.ID}})
		if awsMissing(err, "InvalidVolume.NotFound") {
			delete(inventory.volumes, reference.ID)
			return nil
		}
		if err != nil || page == nil || !awsResponseKnown(page.ResultMetadata) || len(page.Volumes) != 1 {
			return errors.New("AWS recorded volume observation unavailable")
		}
		volume := page.Volumes[0]
		if aws.ToString(volume.VolumeId) != reference.ID || !awsOwned(volume.Tags, p.Config.Owner, a.ID) || aws.ToString(volume.AvailabilityZone) != a.Offering.Zone {
			return errors.New("AWS recorded volume ownership changed")
		}
		inventory.volumes[reference.ID] = volume
	case "aws-network-interface":
		if !strings.HasPrefix(reference.ID, "eni-") {
			return errors.New("AWS recorded interface identity invalid")
		}
		page, err := client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{reference.ID}})
		if awsMissing(err, "InvalidNetworkInterfaceID.NotFound") {
			delete(inventory.interfaces, reference.ID)
			return nil
		}
		if err != nil || page == nil || !awsResponseKnown(page.ResultMetadata) || len(page.NetworkInterfaces) != 1 {
			return errors.New("AWS recorded interface observation unavailable")
		}
		network := page.NetworkInterfaces[0]
		if aws.ToString(network.NetworkInterfaceId) != reference.ID || !awsOwned(network.TagSet, p.Config.Owner, a.ID) || aws.ToString(network.OwnerId) != p.Config.AccountID || aws.ToString(network.AvailabilityZone) != a.Offering.Zone || aws.ToString(network.SubnetId) != p.Config.Subnet {
			return errors.New("AWS recorded interface ownership changed")
		}
		inventory.interfaces[reference.ID] = network
	default:
		return errors.New("unsupported AWS dependency kind")
	}
	return nil
}
