// Package netbox applies a plan through the NetBox REST API.
package netbox

import (
	"context"
	"fmt"
	"maps"
	"net/http"

	nb "github.com/frauniki/netbox-groundtruth-agent/internal/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
)

type Sink struct{ Client *nb.Client }

// Apply performs the changes in order and stops at the first error.
// Re-running converges, since the next plan is computed from scratch.
func (s Sink) Apply(ctx context.Context, p *planner.Plan) (int, error) {
	if p.Status != planner.StatusOK {
		return 0, nil
	}
	for i, c := range p.Changes {
		if err := s.apply(ctx, c); err != nil {
			return i, fmt.Errorf("%s %s %q: %w", c.Action, c.Object, c.Name, err)
		}
	}
	if len(p.Touch) > 0 {
		if err := s.Client.Do(ctx, http.MethodPatch, devicePath(p.DeviceID), map[string]any{"custom_fields": p.Touch}, nil); err != nil {
			return len(p.Changes), fmt.Errorf("update last-collected time: %w", err)
		}
	}
	return len(p.Changes), nil
}

func (s Sink) apply(ctx context.Context, c planner.Change) error {
	switch c.Object + "/" + c.Action {
	case planner.ObjectDevice + "/" + planner.ActionUpdate:
		return s.Client.Do(ctx, http.MethodPatch, devicePath(c.ID), map[string]any{"custom_fields": c.Fields}, nil)
	case planner.ObjectInterface + "/" + planner.ActionCreate:
		fields := maps.Clone(c.Fields)
		mac, _ := fields["mac_address"].(string)
		if s.Client.MACObjects() {
			delete(fields, "mac_address")
		}
		var created struct{ ID int }
		if err := s.Client.Do(ctx, http.MethodPost, "/api/dcim/interfaces/", fields, &created); err != nil {
			return err
		}
		if s.Client.MACObjects() && mac != "" {
			return s.setMAC(ctx, created.ID, mac)
		}
		return nil
	case planner.ObjectMACAddress + "/" + planner.ActionCreate:
		mac, _ := c.Fields["mac_address"].(string)
		return s.setMAC(ctx, c.ID, mac)
	case planner.ObjectInventoryItem + "/" + planner.ActionCreate:
		return s.Client.Do(ctx, http.MethodPost, "/api/dcim/inventory-items/", c.Fields, nil)
	case planner.ObjectInventoryItem + "/" + planner.ActionUpdate:
		return s.Client.Do(ctx, http.MethodPatch, fmt.Sprintf("/api/dcim/inventory-items/%d/", c.ID), c.Fields, nil)
	case planner.ObjectInventoryItem + "/" + planner.ActionDelete:
		return s.Client.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/dcim/inventory-items/%d/", c.ID), nil, nil)
	}
	return fmt.Errorf("unsupported change")
}

// setMAC records mac on an interface. NetBox 4.2+ keeps MAC addresses as
// separate objects: create one assigned to the interface, then make it the
// primary MAC. Older releases store it on the interface itself.
func (s Sink) setMAC(ctx context.Context, ifaceID int, mac string) error {
	path := fmt.Sprintf("/api/dcim/interfaces/%d/", ifaceID)
	if !s.Client.MACObjects() {
		return s.Client.Do(ctx, http.MethodPatch, path, map[string]any{"mac_address": mac}, nil)
	}
	var created struct{ ID int }
	err := s.Client.Do(ctx, http.MethodPost, "/api/dcim/mac-addresses/", map[string]any{
		"mac_address": mac, "assigned_object_type": "dcim.interface", "assigned_object_id": ifaceID,
	}, &created)
	if err != nil {
		return err
	}
	return s.Client.Do(ctx, http.MethodPatch, path, map[string]any{"primary_mac_address": created.ID}, nil)
}

func devicePath(id int) string { return fmt.Sprintf("/api/dcim/devices/%d/", id) }
