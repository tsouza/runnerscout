package lifecycle

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
)

// MergeResources retains every previously observed dependency. Missing discovery
// results never remove obligations; the provider must observe each recorded ID.
func MergeResources(current, observed []ResourceReference) ([]ResourceReference, bool, error) {
	set := make(map[ResourceReference]bool)
	identities := make(map[[2]string]string)
	for _, group := range [][]ResourceReference{current, observed} {
		for _, resource := range group {
			if resource.Kind == "" || len(resource.Kind) > 64 || strings.Trim(resource.Kind, "abcdefghijklmnopqrstuvwxyz0123456789-./") != "" || resource.ID == "" || len(resource.ID) > 2048 || strings.TrimSpace(resource.ID) != resource.ID || strings.ContainsAny(resource.ID, "\x00\r\n\t") || len(resource.UID) > 256 || strings.TrimSpace(resource.UID) != resource.UID || strings.ContainsAny(resource.UID, "\x00\r\n\t") {
				return nil, false, errors.New("invalid cloud dependency reference")
			}
			key := [2]string{resource.Kind, resource.ID}
			if uid, exists := identities[key]; exists && uid != resource.UID {
				return nil, false, errors.New("cloud dependency generation changed or discarded")
			}
			identities[key] = resource.UID
			set[resource] = true
			if len(set) > 64 {
				return nil, false, errors.New("cloud dependency reference limit exceeded")
			}
		}
	}
	merged := make([]ResourceReference, 0, len(set))
	for resource := range set {
		merged = append(merged, resource)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Kind != merged[j].Kind {
			return merged[i].Kind < merged[j].Kind
		}
		return merged[i].ID < merged[j].ID
	})
	return merged, !slices.Equal(current, merged), nil
}

func (c *Controller) rememberResources(ctx context.Context, allocation *Allocation, resources []ResourceReference) error {
	merged, changed, err := MergeResources(allocation.Resources, resources)
	if err != nil || !changed {
		return err
	}
	next := *allocation
	next.Resources = merged
	saved, err := c.Store.Save(ctx, next, allocation.Revision)
	if err != nil {
		return err
	}
	*allocation = saved
	return nil
}
