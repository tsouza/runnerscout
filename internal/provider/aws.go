package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

func (p *Command) createAWS(ctx context.Context, a lifecycle.Allocation, script string) (lifecycle.Creation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if a.ResourceID != "" || len(a.Resources) > 0 {
		return lifecycle.Creation{}, errors.New("AWS create requires an unoccupied allocation")
	}
	client, err := p.AWS.session(ctx, p.Config, a.Offering.Region)
	if err != nil {
		return lifecycle.Creation{}, err
	}
	inventory, err := p.awsInventory(ctx, client, a)
	if err != nil || inventory.instance != nil || len(inventory.volumes) > 0 || len(inventory.interfaces) > 0 {
		return lifecycle.Creation{}, errors.New("AWS allocation inventory is unknown or occupied")
	}
	images, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{a.Offering.Image}})
	if err != nil || images == nil || len(images.Images) != 1 || aws.ToString(images.Images[0].ImageId) != a.Offering.Image {
		return lifecycle.Creation{}, lifecycle.ErrNoEffect
	}
	image := images.Images[0]
	architecture := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[a.Offering.Architecture]
	if architecture == "" || string(image.Architecture) != architecture || image.State != "available" || image.Platform != "" || image.RootDeviceType != "ebs" || aws.ToString(image.RootDeviceName) == "" || len(image.ProductCodes) > 0 {
		return lifecycle.Creation{}, lifecycle.ErrNoEffect
	}
	mappings := make([]types.BlockDeviceMapping, 0, len(image.BlockDeviceMappings))
	expected := map[string]bool{}
	for _, mapping := range image.BlockDeviceMappings {
		if mapping.Ebs != nil {
			if aws.ToString(mapping.DeviceName) == "" || expected[aws.ToString(mapping.DeviceName)] {
				return lifecycle.Creation{}, lifecycle.ErrNoEffect
			}
			disk := *mapping.Ebs
			disk.DeleteOnTermination = aws.Bool(true)
			disk.Encrypted = aws.Bool(true)
			mapping.Ebs = &disk
			expected[aws.ToString(mapping.DeviceName)] = true
		}
		mappings = append(mappings, mapping)
	}
	if !expected[aws.ToString(image.RootDeviceName)] {
		return lifecycle.Creation{}, lifecycle.ErrNoEffect
	}
	tags := []types.Tag{{Key: aws.String("runnerscout-owner"), Value: aws.String(p.Config.Owner)}, {Key: aws.String("runnerscout-operation"), Value: aws.String(a.ID)}}
	input := &ec2.RunInstancesInput{
		ImageId: aws.String(a.Offering.Image), InstanceInitiatedShutdownBehavior: "terminate", InstanceType: types.InstanceType(a.Offering.Machine), MinCount: aws.Int32(1), MaxCount: aws.Int32(1), ClientToken: aws.String(a.ID),
		Placement: &types.Placement{AvailabilityZone: aws.String(a.Offering.Zone)}, UserData: aws.String(base64.StdEncoding.EncodeToString([]byte(script))), BlockDeviceMappings: mappings,
		NetworkInterfaces: []types.InstanceNetworkInterfaceSpecification{{DeviceIndex: aws.Int32(0), SubnetId: aws.String(p.Config.Subnet), Groups: []string{p.Config.SecurityGroup}, AssociatePublicIpAddress: aws.Bool(false), AssociateCarrierIpAddress: aws.Bool(false), DeleteOnTermination: aws.Bool(true), Ipv6AddressCount: aws.Int32(0)}},
		MetadataOptions:   &types.InstanceMetadataOptionsRequest{HttpEndpoint: "enabled", HttpTokens: "required", HttpPutResponseHopLimit: aws.Int32(1), HttpProtocolIpv6: "disabled", InstanceMetadataTags: "disabled"},
		TagSpecifications: []types.TagSpecification{{ResourceType: "instance", Tags: tags}, {ResourceType: "volume", Tags: tags}, {ResourceType: "network-interface", Tags: tags}},
	}
	if a.Offering.Spot {
		input.InstanceMarketOptions = &types.InstanceMarketOptionsRequest{MarketType: "spot", SpotOptions: &types.SpotMarketOptions{SpotInstanceType: "one-time", InstanceInterruptionBehavior: "terminate"}}
	}
	response, err := client.RunInstances(ctx, input)
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "InsufficientInstanceCapacity" {
			return lifecycle.Creation{}, lifecycle.ErrCapacity
		}
		return lifecycle.Creation{}, errors.New("AWS create commitment unknown")
	}
	if response == nil || aws.ToString(response.OwnerId) != p.Config.AccountID || len(response.Instances) != 1 || !p.awsInstanceOwned(a, response.Instances[0]) {
		return lifecycle.Creation{}, errors.New("AWS create response ownership unconfirmed")
	}
	instance := response.Instances[0]
	receipt := lifecycle.Creation{ResourceID: aws.ToString(instance.InstanceId)}
	observed := map[string]bool{}
	for _, mapping := range instance.BlockDeviceMappings {
		if mapping.Ebs == nil {
			continue
		}
		name, id := aws.ToString(mapping.DeviceName), aws.ToString(mapping.Ebs.VolumeId)
		if id != "" {
			receipt.Resources = append(receipt.Resources, lifecycle.ResourceReference{Kind: "aws-volume", ID: id})
		}
		observed[name] = true
	}
	for _, network := range instance.NetworkInterfaces {
		if id := aws.ToString(network.NetworkInterfaceId); id != "" {
			receipt.Resources = append(receipt.Resources, lifecycle.ResourceReference{Kind: "aws-network-interface", ID: id})
		}
	}
	receipt.Resources, _, err = lifecycle.MergeResources(nil, receipt.Resources)
	if err != nil {
		return receipt, err
	}
	if len(instance.NetworkInterfaces) != 1 || len(observed) != len(expected) || len(receipt.Resources) != len(expected)+1 {
		return receipt, errors.New("AWS creation dependencies incomplete")
	}
	for name := range expected {
		if !observed[name] {
			return receipt, errors.New("AWS creation disk binding incomplete")
		}
	}
	return receipt, nil
}

func (p *Command) observeAWS(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := p.AWS.session(ctx, p.Config, a.Offering.Region)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	inventory, err := p.awsInventory(ctx, client, a)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	return inventory.observation(a)
}

func (p *Command) deleteAWS(ctx context.Context, a lifecycle.Allocation) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := p.AWS.session(ctx, p.Config, a.Offering.Region)
	if err != nil {
		return err
	}
	inventory, err := p.awsInventory(ctx, client, a)
	if err != nil {
		return err
	}
	observed, err := inventory.observation(a)
	if err != nil {
		return err
	}
	if _, changed, err := lifecycle.MergeResources(a.Resources, observed.Resources); err != nil || changed {
		return errors.New("AWS dependencies require a durable checkpoint before deletion")
	}
	if !observed.Exists {
		return nil
	}
	if inventory.instance != nil && inventory.instance.State.Name != "terminated" {
		if inventory.instance.State.Name == "shutting-down" {
			return nil
		}
		response, err := client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{observed.ResourceID}})
		if err != nil || response == nil || len(response.TerminatingInstances) != 1 {
			return errors.New("AWS termination commitment unknown")
		}
		change := response.TerminatingInstances[0]
		if aws.ToString(change.InstanceId) != observed.ResourceID || change.CurrentState == nil || change.CurrentState.Name != "shutting-down" && change.CurrentState.Name != "terminated" {
			return errors.New("AWS termination identity or state unconfirmed")
		}
		return nil
	}
	volumes := make([]string, 0, len(inventory.volumes))
	for id := range inventory.volumes {
		volumes = append(volumes, id)
	}
	sort.Strings(volumes)
	for _, id := range volumes {
		volume := inventory.volumes[id]
		if len(volume.Attachments) > 0 {
			continue
		}
		switch volume.State {
		case "available":
			_, err := client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(id)})
			if err != nil && !awsMissing(err, "InvalidVolume.NotFound") {
				return errors.New("AWS volume deletion commitment unknown")
			}
			return nil
		case "creating", "in-use", "deleting", "deleted":
		default:
			return errors.New("AWS volume state unknown")
		}
	}
	interfaces := make([]string, 0, len(inventory.interfaces))
	for id := range inventory.interfaces {
		interfaces = append(interfaces, id)
	}
	sort.Strings(interfaces)
	for _, id := range interfaces {
		network := inventory.interfaces[id]
		if network.Attachment != nil {
			continue
		}
		switch network.Status {
		case "available":
			_, err := client.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: aws.String(id)})
			if err != nil && !awsMissing(err, "InvalidNetworkInterfaceID.NotFound") {
				return errors.New("AWS network interface deletion commitment unknown")
			}
			return nil
		case "in-use":
		default:
			return errors.New("AWS network interface state unknown")
		}
	}
	return nil
}
