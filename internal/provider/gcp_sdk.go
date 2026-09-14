package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

// GCPSDK uses provider-local credentials. Service permits an isolated HTTPS
// transport in tests; production uses the official Compute endpoint.
type GCPSDK struct{ Service *compute.Service }

func (*GCPSDK) String() string   { return "GCP SDK (credentials redacted)" }
func (*GCPSDK) GoString() string { return "GCP SDK (credentials redacted)" }

func (p *Command) gcpClient() (*compute.Service, error) {
	if p.GCP == nil || p.GCP.Service == nil {
		return nil, errors.New("GCP credential scope unavailable")
	}
	return p.GCP.Service, nil
}
func (p *Command) gcpResource(a lifecycle.Allocation, kind, name string) string {
	return "projects/" + p.Config.Project + "/zones/" + a.Offering.Zone + "/" + kind + "/" + name
}
func gcpMatches(link, resource string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	if u.IsAbs() {
		if u.Scheme != "https" || (u.Host != "www.googleapis.com" && u.Host != "compute.googleapis.com") || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
		return u.Path == "/compute/v1/"+resource
	}
	return link == resource
}
func (p *Command) gcpRequestID(a lifecycle.Allocation, action string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("runnerscout/"+p.Config.Owner+"/"+p.gcpResource(a, "instances", a.ID)+"/"+action)).String()
}
func missingGCP(err error) bool {
	var response *googleapi.Error
	return errors.As(err, &response) && response.Code == http.StatusNotFound
}

// gcpCapacityFailure reports whether a terminal operation's error definitively
// identifies zonal capacity exhaustion, never a quota, permission or transient
// failure that a different pool could not resolve.
func gcpCapacityFailure(op *compute.Operation) bool {
	if op == nil || op.Error == nil {
		return false
	}
	for _, e := range op.Error.Errors {
		if e != nil && (e.Code == "ZONE_RESOURCE_POOL_EXHAUSTED" || e.Code == "ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS") {
			return true
		}
	}
	return false
}
func (p *Command) gcpLabels(a lifecycle.Allocation) map[string]string {
	return map[string]string{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID}
}
func (p *Command) ownsGCP(a lifecycle.Allocation, labels map[string]string) bool {
	return labels["runnerscout-owner"] == p.Config.Owner && labels["runnerscout-operation"] == a.ID
}

type gcpInventory struct {
	vm   *compute.Instance
	disk *compute.Disk
}

func (p *Command) gcpInventory(ctx context.Context, a lifecycle.Allocation) (gcpInventory, error) {
	var result gcpInventory
	service, err := p.gcpClient()
	if err != nil {
		return result, err
	}
	vm, err := service.Instances.Get(p.Config.Project, a.Offering.Zone, a.ID).Context(ctx).Do()
	if err != nil && !missingGCP(err) {
		return result, errors.New("GCP VM inventory unavailable")
	}
	if err == nil {
		if vm.Name != a.ID || vm.Id == 0 || !gcpMatches(vm.SelfLink, p.gcpResource(a, "instances", a.ID)) || !p.ownsGCP(a, vm.Labels) {
			return result, errors.New("GCP VM ownership unconfirmed")
		}
		if len(vm.Disks) != 1 || vm.Disks[0] == nil || !vm.Disks[0].Boot || !vm.Disks[0].AutoDelete || !gcpMatches(vm.Disks[0].Source, p.gcpResource(a, "disks", a.ID)) {
			return result, errors.New("GCP VM disk binding changed; cleanup retained")
		}
		result.vm = vm
	}
	diskName := a.ID
	disk, err := service.Disks.Get(p.Config.Project, a.Offering.Zone, diskName).Context(ctx).Do()
	if err != nil && !missingGCP(err) {
		return result, errors.New("GCP disk inventory unavailable")
	}
	if err == nil {
		if disk.Name != diskName || disk.Id == 0 || !gcpMatches(disk.SelfLink, p.gcpResource(a, "disks", diskName)) || !p.ownsGCP(a, disk.Labels) {
			return result, errors.New("GCP disk ownership unconfirmed")
		}
		for _, user := range disk.Users {
			if !gcpMatches(user, p.gcpResource(a, "instances", a.ID)) {
				return result, errors.New("GCP disk attached outside its allocation")
			}
		}
		result.disk = disk
	}
	return result, nil
}

func (p *Command) gcpCreateTerminal(ctx context.Context, a lifecycle.Allocation) error {
	service, err := p.gcpClient()
	if err != nil {
		return err
	}
	requestID := p.gcpRequestID(a, "create")
	count := 0
	err = service.ZoneOperations.List(p.Config.Project, a.Offering.Zone).Filter("clientOperationId = \""+requestID+"\"").Pages(ctx, func(page *compute.OperationList) error {
		if page.Kind != "compute#operationList" {
			return errors.New("invalid GCP operation inventory")
		}
		for _, op := range page.Items {
			count++
			if count > 1 || op == nil || op.Name == "" || op.ClientOperationId != requestID || op.OperationType != "insert" || !gcpMatches(op.TargetLink, p.gcpResource(a, "instances", a.ID)) || op.Status != "DONE" {
				return errors.New("GCP create commitment unconfirmed")
			}
		}
		return nil
	})
	if err != nil || (count == 0 && a.ResourceID == "") {
		return errors.New("GCP create commitment unconfirmed")
	}
	return nil
}
func (p *Command) waitGCPOperation(ctx context.Context, a lifecycle.Allocation, op *compute.Operation, action, kind, name string) error {
	service, err := p.gcpClient()
	if err != nil {
		return err
	}
	operationName := ""
	expectedType := "delete"
	if action == "create" {
		expectedType = "insert"
	}
	for {
		if op == nil || op.OperationType != expectedType || op.Name == "" || (operationName != "" && op.Name != operationName) || op.ClientOperationId != p.gcpRequestID(a, action) || !gcpMatches(op.TargetLink, p.gcpResource(a, kind, name)) {
			return errors.New("GCP operation identity unconfirmed")
		}
		operationName = op.Name
		switch op.Status {
		case "DONE":
			if op.Error != nil || op.HttpErrorStatusCode != 0 {
				if action == "create" && gcpCapacityFailure(op) {
					if inventory, ierr := p.gcpInventory(ctx, a); ierr == nil && inventory.vm == nil && inventory.disk == nil {
						return lifecycle.ErrCapacity
					}
				}
				return errors.New("GCP operation failed; reconcile ownership")
			}
			return nil
		case "PENDING", "RUNNING":
		default:
			return errors.New("GCP operation state unknown")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("GCP operation commitment unknown")
		case <-timer.C:
		}
		op, err = service.ZoneOperations.Get(p.Config.Project, a.Offering.Zone, operationName).Context(ctx).Do()
		if err != nil {
			return errors.New("GCP operation observation unavailable")
		}
	}
}

func (p *Command) createGCP(ctx context.Context, a lifecycle.Allocation, script string) (lifecycle.Creation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	service, err := p.gcpClient()
	if err != nil {
		return lifecycle.Creation{}, err
	}
	inventory, err := p.gcpInventory(ctx, a)
	if err != nil || inventory.vm != nil || inventory.disk != nil {
		return lifecycle.Creation{}, errors.New("GCP create requires an unoccupied allocation identity")
	}
	machine := a.Offering.Machine
	if !strings.Contains(machine, "/") {
		machine = p.gcpResource(a, "machineTypes", machine)
	}
	subnet := p.Config.Subnet
	if !strings.Contains(subnet, "/") {
		subnet = "projects/" + p.Config.Project + "/regions/" + a.Offering.Region + "/subnetworks/" + subnet
	}
	instance := &compute.Instance{Name: a.ID, MachineType: machine, Labels: p.gcpLabels(a),
		NetworkInterfaces: []*compute.NetworkInterface{{Subnetwork: subnet, ForceSendFields: []string{"AccessConfigs"}}},
		ServiceAccounts:   []*compute.ServiceAccount{}, ForceSendFields: []string{"ServiceAccounts"},
		Disks:    []*compute.AttachedDisk{{Boot: true, AutoDelete: true, Type: "PERSISTENT", InitializeParams: &compute.AttachedDiskInitializeParams{DiskName: a.ID, SourceImage: a.Offering.Image, Labels: p.gcpLabels(a)}}},
		Metadata: &compute.Metadata{Items: []*compute.MetadataItems{{Key: "startup-script", Value: &script}}},
	}
	if a.Offering.Spot {
		instance.Scheduling = &compute.Scheduling{ProvisioningModel: "SPOT", InstanceTerminationAction: "DELETE", OnHostMaintenance: "TERMINATE", AutomaticRestart: googleapi.Bool(false), ForceSendFields: []string{"AutomaticRestart"}}
	}
	op, err := service.Instances.Insert(p.Config.Project, a.Offering.Zone, instance).RequestId(p.gcpRequestID(a, "create")).Context(ctx).Do()
	if err != nil {
		return lifecycle.Creation{}, errors.New("GCP creation commitment unknown")
	}
	if err = p.waitGCPOperation(ctx, a, op, "create", "instances", a.ID); err != nil {
		return lifecycle.Creation{}, err
	}
	receipt := lifecycle.Creation{ResourceID: a.ID}
	// Unlike AWS/Azure, GCP's create path (Instances.Insert, then polling
	// ZoneOperations.Get/List) only ever receives compute.Operation
	// responses, which carry no NetworkInterfaces field - so capturing this
	// allocation's private IP costs one genuinely new Instances.Get call,
	// made only when a.NetworkProfile != "" (see
	// lifecycle.Creation.WireGuardEndpoint's doc comment for the cost/
	// benefit reasoning). Every allocation without wireguard intent - every
	// allocation this codebase's configuration path can produce today -
	// takes none of this cost.
	if a.NetworkProfile != "" {
		receipt.WireGuardEndpoint = p.gcpCaptureWireGuardEndpoint(ctx, service, a)
	}
	return receipt, nil
}

// gcpCaptureWireGuardEndpoint performs the one additional Instances.Get
// createGCP's own comment above describes, and extracts the just-created
// VM's private IP as its WireGuardEndpoint - see
// lifecycle.Allocation.WireGuardEndpoint's doc comment for what this value
// means and wireGuardEndpoint for its exact "host:port" rendering. Best-
// effort only: any transport failure, or a NetworkInterfaces shape other
// than the single entry this codebase's own createGCP template ever
// produces, returns "" rather than an error - a failed or incomplete
// capture must never fail a creation that has already succeeded, exactly
// like AWS's own equivalent single-NIC check in createAWS.
func (p *Command) gcpCaptureWireGuardEndpoint(ctx context.Context, service *compute.Service, a lifecycle.Allocation) string {
	vm, err := service.Instances.Get(p.Config.Project, a.Offering.Zone, a.ID).Context(ctx).Do()
	if err != nil || vm == nil || len(vm.NetworkInterfaces) != 1 || vm.NetworkInterfaces[0] == nil {
		return ""
	}
	return wireGuardEndpoint(vm.NetworkInterfaces[0].NetworkIP)
}
func (p *Command) observeGCP(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	if a.ResourceID != "" && a.ResourceID != a.ID {
		return lifecycle.Observation{}, errors.New("GCP durable resource identity mismatch")
	}
	if err := p.gcpCreateTerminal(ctx, a); err != nil {
		return lifecycle.Observation{}, err
	}
	inventory, err := p.gcpInventory(ctx, a)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	exists := inventory.vm != nil || inventory.disk != nil
	interrupted := !exists && a.Offering.Spot && p.gcpConfirmedPreemption(ctx, a)
	return lifecycle.Observation{Known: true, Exists: exists, Interrupted: interrupted, ResourceID: a.ID}, nil
}

// gcpConfirmedPreemption reports whether GCP's own compute.instances.preempted
// system operation - never inferred from a bare absence - is recorded against
// this instance. Any ambiguity (an unavailable observation or a non-matching
// target) returns false.
func (p *Command) gcpConfirmedPreemption(ctx context.Context, a lifecycle.Allocation) bool {
	service, err := p.gcpClient()
	if err != nil {
		return false
	}
	found := false
	err = service.ZoneOperations.List(p.Config.Project, a.Offering.Zone).Filter(`operationType = "compute.instances.preempted"`).Pages(ctx, func(page *compute.OperationList) error {
		if page.Kind != "compute#operationList" {
			return errors.New("invalid GCP operation inventory")
		}
		for _, op := range page.Items {
			if op != nil && op.OperationType == "compute.instances.preempted" && gcpMatches(op.TargetLink, p.gcpResource(a, "instances", a.ID)) {
				found = true
			}
		}
		return nil
	})
	return err == nil && found
}
func (p *Command) deleteGCP(ctx context.Context, a lifecycle.Allocation) error {
	// 60s, not createGCP's 30s: a real dispatch found that deleting a real
	// instance (which includes detaching/deleting its PERSISTENT,
	// AutoDelete boot disk in the same async operation chain) can
	// genuinely take longer to reach DONE than creating one does - a
	// timeout here doesn't mean the delete failed (GCP keeps processing it
	// server-side regardless of whether this call is still watching), just
	// that this one call couldn't confirm completion in time, forcing an
	// avoidable extra reconciliation round-trip.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := p.gcpCreateTerminal(ctx, a); err != nil {
		return err
	}
	inventory, err := p.gcpInventory(ctx, a)
	if err != nil {
		return err
	}
	service, err := p.gcpClient()
	if err != nil {
		return err
	}
	var op *compute.Operation
	kind, name, action := "instances", a.ID, "delete-vm"
	if inventory.vm != nil {
		op, err = service.Instances.Delete(p.Config.Project, a.Offering.Zone, a.ID).RequestId(p.gcpRequestID(a, action)).Context(ctx).Do()
	} else if inventory.disk != nil {
		if len(inventory.disk.Users) != 0 {
			return errors.New("GCP disk attachment remains; cleanup retained")
		}
		kind, name, action = "disks", a.ID, "delete-disk"
		op, err = service.Disks.Delete(p.Config.Project, a.Offering.Zone, name).RequestId(p.gcpRequestID(a, action)).Context(ctx).Do()
	} else {
		return nil
	}
	if missingGCP(err) {
		return nil
	}
	if err != nil {
		return errors.New("GCP deletion commitment unknown")
	}
	return p.waitGCPOperation(ctx, a, op, action, kind, name)
}
