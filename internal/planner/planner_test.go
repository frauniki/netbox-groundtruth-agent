package planner_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox/netboxtest"
	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
	nbsink "github.com/frauniki/netbox-groundtruth-agent/internal/sink/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

const tag = "managed-by-groundtruth"

// fixtureSnapshot is the snapshot collected from testdata/rootfs.
func fixtureSnapshot(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	b, err := os.ReadFile("../../testdata/snapshot.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// setup returns a fake NetBox holding the fixture machine as device and
// the client for it.
func setup(t *testing.T, version string) (*netboxtest.Server, *netbox.Client, int) {
	t.Helper()
	srv := netboxtest.New(t, version)
	cf := netboxtest.Obj{}
	for _, k := range config.CustomFieldKeys {
		cf[k] = nil
	}
	dev := srv.Add("devices", netboxtest.Obj{
		"name": "server-01", "serial": "EXAMPLE-SN-0001", "custom_fields": cf,
		"device_type": netboxtest.Obj{"model": "Server X100", "manufacturer": netboxtest.Obj{"id": 1, "name": "Example"}},
	})
	srv.Add("tags", netboxtest.Obj{"name": "Managed by groundtruth", "slug": tag})
	srv.Add("interfaces", netboxtest.Obj{"device": dev, "name": "eno1"})
	srv.Add("interfaces", netboxtest.Obj{"device": dev, "name": "eno2", "mac_address": "00:00:5E:00:53:02"})
	c, err := netbox.New(config.NetBox{URL: srv.URL}, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv, c, dev
}

func build(t *testing.T, c *netbox.Client, s *snapshot.Snapshot, cfg config.Sync) *planner.Plan {
	t.Helper()
	p, err := planner.Build(context.Background(), c, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func apply(t *testing.T, c *netbox.Client, p *planner.Plan) {
	t.Helper()
	if _, err := (nbsink.Sink{Client: c}).Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func changes(p *planner.Plan, action, object string) []planner.Change {
	var out []planner.Change
	for _, c := range p.Changes {
		if c.Action == action && c.Object == object {
			out = append(out, c)
		}
	}
	return out
}

func TestUnregistered(t *testing.T) {
	srv, c, _ := setup(t, "4.2.0")
	s := fixtureSnapshot(t)
	s.System.Serial = "NOT-IN-NETBOX"
	p := build(t, c, s, config.Default().Sync)
	if p.Status != planner.StatusUnregistered || len(p.Changes) != 0 {
		t.Fatalf("got status %s with %d changes", p.Status, len(p.Changes))
	}
	apply(t, c, p)
	if len(srv.Writes) != 0 {
		t.Errorf("unexpected writes: %v", srv.Writes)
	}
}

func TestDuplicate(t *testing.T) {
	srv, c, _ := setup(t, "4.2.0")
	srv.Add("devices", netboxtest.Obj{"name": "server-02", "serial": "example-sn-0001", "custom_fields": netboxtest.Obj{}})
	p := build(t, c, fixtureSnapshot(t), config.Default().Sync)
	if p.Status != planner.StatusDuplicate || len(p.Changes) != 0 {
		t.Fatalf("got status %s with %d changes", p.Status, len(p.Changes))
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	for _, version := range []string{"4.1.11", "4.2.0", "4.7.2"} {
		t.Run(version, func(t *testing.T) {
			srv, c, dev := setup(t, version)
			srv.MaxPageSize = 2 // exercise pagination
			cfg := config.Default().Sync
			cfg.CreateInterfaces = true
			s := fixtureSnapshot(t)
			s.NICs = append(s.NICs, snapshot.NIC{Name: "eno3", MAC: "00:00:5E:00:53:03", SpeedMbps: 25000})

			p := build(t, c, s, cfg)
			if got := changes(p, planner.ActionCreate, planner.ObjectMACAddress); len(got) != 1 || got[0].Name != "eno1" {
				t.Errorf("want MAC set on eno1 only, got %+v", got)
			}
			if got := changes(p, planner.ActionCreate, planner.ObjectInterface); len(got) != 1 || got[0].Fields["type"] != "25gbase-x-sfp28" {
				t.Errorf("want eno3 created as 25gbase-x-sfp28, got %+v", got)
			}
			// 2 CPUs, 2 DIMMs, 2 disks, 1 GPU, 3 NICs
			if got := changes(p, planner.ActionCreate, planner.ObjectInventoryItem); len(got) != 10 {
				t.Errorf("want 10 inventory items, got %d", len(got))
			}
			if p.Touch["last_collected"] == nil {
				t.Error("last_collected not touched")
			}
			apply(t, c, p)

			if srv.Version != "4.1.11" && !slices.Contains(srv.Writes, "POST /api/dcim/mac-addresses/") {
				t.Errorf("NetBox %s: MAC object not created; writes %v", version, srv.Writes)
			}
			d := srv.Objects("devices")[dev]
			if cf := d["custom_fields"].(netboxtest.Obj); cf["cpu_threads"] != float64(8) && cf["cpu_threads"] != 8 {
				t.Errorf("cpu_threads = %v", cf["cpu_threads"])
			}
			for _, it := range srv.Objects("inventory-items") {
				if it["discovered"] != true || len(it["tags"].([]netboxtest.Obj)) != 1 {
					t.Errorf("item %v: not discovered or not tagged", it["name"])
				}
			}

			second := build(t, c, s, cfg)
			if len(second.Changes) != 0 {
				t.Errorf("second run has changes: %+v", second.Changes)
			}
		})
	}
}

func TestIncompleteSectionIsNotDeleted(t *testing.T) {
	srv, c, dev := setup(t, "4.2.0")
	srv.Add("inventory-items", netboxtest.Obj{"device": dev, "name": "Memory DIMM_Z9", "serial": "OLD", "tags": []string{tag}})
	s := fixtureSnapshot(t)
	s.Memory.Modules = nil
	s.Incomplete = append(s.Incomplete, snapshot.SectionMemoryModules)
	if p := build(t, c, s, config.Default().Sync); p.Count(planner.ActionDelete) != 0 {
		t.Errorf("deleted although memory modules were not collected: %+v", p.Changes)
	}
	// Once memory modules are collected, the vanished module is removed.
	if p := build(t, c, fixtureSnapshot(t), config.Default().Sync); p.Count(planner.ActionDelete) != 1 {
		t.Errorf("want 1 delete, got %+v", p.Changes)
	}
}

func TestManualItemsAreUntouched(t *testing.T) {
	srv, c, dev := setup(t, "4.2.0")
	manual := srv.Add("inventory-items", netboxtest.Obj{"device": dev, "name": "Hand-made RAID card", "serial": "RAID0001"})
	same := srv.Add("inventory-items", netboxtest.Obj{"device": dev, "name": "Boot SSD", "serial": "NVMESERIAL0001", "part_id": "typo"})
	p := build(t, c, fixtureSnapshot(t), config.Default().Sync)
	for _, ch := range p.Changes {
		if ch.ID == manual || ch.ID == same || ch.Name == "Disk nvme0n1" {
			t.Errorf("change touches a hand-registered item: %+v", ch)
		}
	}
}

func TestDeleteLimit(t *testing.T) {
	srv, c, dev := setup(t, "4.2.0")
	for i := range 11 {
		srv.Add("inventory-items", netboxtest.Obj{"device": dev, "name": "Disk gone" + string(rune('a'+i)), "tags": []string{tag}})
	}
	cfg := config.Default().Sync
	p := build(t, c, fixtureSnapshot(t), cfg)
	if err := p.CheckLimits(cfg); err == nil || !strings.Contains(err.Error(), "max_deletes") {
		t.Errorf("want max_deletes error, got %v", err)
	}
	cfg.MaxDeletes, cfg.MaxChanges = 0, 5
	if err := p.CheckLimits(cfg); err == nil || !strings.Contains(err.Error(), "max_changes") {
		t.Errorf("want max_changes error, got %v", err)
	}
}

func TestNetBoxErrorStopsPlanning(t *testing.T) {
	srv, c, _ := setup(t, "4.2.0")
	srv.Fail = "/api/dcim/inventory-items/"
	if _, err := planner.Build(context.Background(), c, fixtureSnapshot(t), config.Default().Sync); err == nil {
		t.Fatal("want error when NetBox fails")
	}
}

func TestUndefinedCustomFieldIsSkipped(t *testing.T) {
	_, c, _ := setup(t, "4.2.0")
	cfg := config.Default().Sync
	cfg.CustomFields = map[string]string{"cpu_model": "hw_cpu"}
	p := build(t, c, fixtureSnapshot(t), cfg)
	if len(changes(p, planner.ActionUpdate, planner.ObjectDevice)) != 0 {
		t.Error("wrote an undefined custom field")
	}
	if !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "hw_cpu") }) {
		t.Errorf("no warning about hw_cpu: %v", p.Warnings)
	}
}

func TestMissingOwnerTagSkipsInventory(t *testing.T) {
	_, c, _ := setup(t, "4.2.0")
	cfg := config.Default().Sync
	cfg.OwnerTag = "no-such-tag"
	p := build(t, c, fixtureSnapshot(t), cfg)
	if n := len(changes(p, planner.ActionCreate, planner.ObjectInventoryItem)); n != 0 {
		t.Errorf("created %d items without an owner tag", n)
	}
}
