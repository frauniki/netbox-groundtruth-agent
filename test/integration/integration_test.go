//go:build integration

// Package integration runs the agent against a real, freshly started NetBox.
// Start one with ./test/integration/run.sh, which sets NETBOX_URL and
// NETBOX_TOKEN and runs this test. Never point it at a NetBox holding real data.
package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
	nbsink "github.com/frauniki/netbox-groundtruth-agent/internal/sink/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
	"github.com/frauniki/netbox-groundtruth-agent/internal/source/local"
)

const serial = "EXAMPLE-SN-0001" // from testdata/rootfs

type obj = map[string]any

func TestAgainstNetBox(t *testing.T) {
	ctx := context.Background()
	nbURL, token := os.Getenv("NETBOX_URL"), os.Getenv("NETBOX_TOKEN")
	if nbURL == "" || token == "" {
		t.Fatal("NETBOX_URL and NETBOX_TOKEN must be set (see run.sh)")
	}
	c, err := netbox.New(config.NetBox{URL: nbURL}, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("NetBox %s, MAC address objects: %v", c.Version, c.MACObjects())

	if devs, err := c.DevicesBySerial(ctx, serial); err != nil || len(devs) > 0 {
		t.Fatalf("needs a fresh NetBox without a device with serial %s (err %v)", serial, err)
	}

	// Prerequisites an operator creates by hand.
	importCustomFields(t, c, "../../examples/custom-fields.yaml")
	create(t, c, "/api/extras/tags/", obj{"name": "managed-by-groundtruth", "slug": "managed-by-groundtruth"})
	mfr := create(t, c, "/api/dcim/manufacturers/", obj{"name": "Example Corp", "slug": "example-corp"})
	dt := create(t, c, "/api/dcim/device-types/", obj{"manufacturer": mfr, "model": "Example Server X100", "slug": "example-server-x100"})
	site := create(t, c, "/api/dcim/sites/", obj{"name": "Test Site", "slug": "test-site"})
	role := create(t, c, "/api/dcim/device-roles/", obj{"name": "Server", "slug": "server", "color": "9e9e9e"})
	dev := create(t, c, "/api/dcim/devices/", obj{"name": "server-01", "serial": serial, "device_type": dt, "role": role, "site": site})
	create(t, c, "/api/dcim/interfaces/", obj{"device": dev, "name": "eno1", "type": "25gbase-x-sfp28"})
	eno2 := create(t, c, "/api/dcim/interfaces/", obj{"device": dev, "name": "eno2", "type": "25gbase-x-sfp28"})
	if c.MACObjects() {
		mac := create(t, c, "/api/dcim/mac-addresses/", obj{"mac_address": "00:00:5E:00:53:02", "assigned_object_type": "dcim.interface", "assigned_object_id": eno2})
		do(t, c, http.MethodPatch, fmt.Sprintf("/api/dcim/interfaces/%d/", eno2), obj{"primary_mac_address": mac}, nil)
	} else {
		do(t, c, http.MethodPatch, fmt.Sprintf("/api/dcim/interfaces/%d/", eno2), obj{"mac_address": "00:00:5E:00:53:02"}, nil)
	}
	manual := create(t, c, "/api/dcim/inventory-items/", obj{"device": dev, "name": "Hand-made RAID card", "serial": "RAID0001"})
	stale := create(t, c, "/api/dcim/inventory-items/", obj{"device": dev, "name": "Disk gone", "serial": "GONE0001",
		"tags": []obj{{"slug": "managed-by-groundtruth"}}})
	create(t, c, "/api/ipam/ip-addresses/", obj{"address": "192.0.2.10/24", "assigned_object_type": "dcim.interface", "assigned_object_id": eno2})

	collect := config.Default().Collect
	collect.Root = "../../testdata/rootfs"
	snap, err := local.New(collect).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the IP report, which the fixture cannot collect.
	snap.IPAddresses = []string{"192.0.2.10/24", "198.51.100.7/24"}
	snap.Incomplete = slices.DeleteFunc(snap.Incomplete, func(s string) bool { return s == snapshot.SectionIPAddresses })

	cfg := config.Default().Sync
	p, err := planner.Build(ctx, c, snap, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) > 0 {
		t.Errorf("warnings: %v", p.Warnings)
	}
	want := map[string]int{
		"update/device": 1, "create/mac_address": 1, "create/inventory_item": 9, "delete/inventory_item": 1,
	}
	got := map[string]int{}
	for _, ch := range p.Changes {
		got[ch.Action+"/"+ch.Object]++
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("plan: got %v, want %v\n%+v", got, want, p.Changes)
	}
	if !slices.ContainsFunc(p.Notes, func(n string) bool { return strings.Contains(n, "198.51.100.7") }) {
		t.Errorf("unregistered IP not reported: %v", p.Notes)
	}
	if err := p.CheckLimits(cfg); err != nil {
		t.Fatal(err)
	}
	if n, err := (nbsink.Sink{Client: c}).Apply(ctx, p); err != nil {
		t.Fatalf("apply stopped after %d changes: %v", n, err)
	}

	// NetBox now holds what was observed.
	var d struct {
		CustomFields obj `json:"custom_fields"`
	}
	do(t, c, http.MethodGet, fmt.Sprintf("/api/dcim/devices/%d/", dev), nil, &d)
	for k, v := range map[string]any{"cpu_threads": float64(8), "memory_total_gb": float64(80), "gpu_count": float64(1), "bios_version": "1.2.3"} {
		if d.CustomFields[k] != v {
			t.Errorf("custom field %s = %v, want %v", k, d.CustomFields[k], v)
		}
	}
	if d.CustomFields["last_collected"] == nil {
		t.Error("last_collected not written")
	}
	ifaces, err := c.Interfaces(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifaces {
		if i.Name == "eno1" && !slices.Contains(i.MACs(), "00:00:5E:00:53:01") {
			t.Errorf("eno1 MACs = %v", i.MACs())
		}
	}
	items, err := c.InventoryItems(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, it := range items {
		ids = append(ids, it.ID)
		if it.ID != manual && (!it.Discovered || !it.HasTag("managed-by-groundtruth")) {
			t.Errorf("item %q: discovered=%v tags=%v", it.Name, it.Discovered, it.Tags)
		}
	}
	if !slices.Contains(ids, manual) || slices.Contains(ids, stale) || len(items) != 10 {
		t.Errorf("inventory after apply: %d items %v (manual %d must stay, stale %d must go)", len(items), ids, manual, stale)
	}

	// A second run changes nothing.
	again, err := planner.Build(ctx, c, snap, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Errorf("second run plans changes: %+v", again.Changes)
	}
}

// importCustomFields creates the custom fields of the example file, which
// also checks that its field names are accepted by this NetBox version.
func importCustomFields(t *testing.T, c *netbox.Client, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var cf obj
		if err := dec.Decode(&cf); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		create(t, c, "/api/extras/custom-fields/", cf)
	}
}

func create(t *testing.T, c *netbox.Client, path string, body obj) int {
	t.Helper()
	var out struct{ ID int }
	do(t, c, http.MethodPost, path, body, &out)
	return out.ID
}

func do(t *testing.T, c *netbox.Client, method, path string, body, out any) {
	t.Helper()
	if err := c.Do(context.Background(), method, path, body, out); err != nil {
		t.Fatal(err)
	}
}
