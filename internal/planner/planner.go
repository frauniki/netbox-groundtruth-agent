// Package planner compares a Snapshot with what NetBox currently holds and
// produces a Plan. It only reads from NetBox.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

type Status string

const (
	StatusOK           Status = "ok"
	StatusUnregistered Status = "unregistered"
	StatusDuplicate    Status = "duplicate"
)

const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"

	ObjectDevice        = "device"
	ObjectInterface     = "interface"
	ObjectMACAddress    = "mac_address" // ID is the interface the MAC is assigned to
	ObjectInventoryItem = "inventory_item"
)

type Change struct {
	Action string         `json:"action"`
	Object string         `json:"object"`
	ID     int            `json:"id,omitempty"`
	Name   string         `json:"name"`
	Fields map[string]any `json:"fields,omitempty"`
}

type Plan struct {
	Status     Status   `json:"status"`
	Serial     string   `json:"serial"`
	DeviceID   int      `json:"device_id,omitempty"`
	DeviceName string   `json:"device_name,omitempty"`
	Changes    []Change `json:"changes"`
	// Touch holds device custom fields written on every apply that are not
	// counted as changes (the last-collected timestamp).
	Touch map[string]any `json:"touch,omitempty"`
	// Notes are differences that are reported but never written.
	Notes    []string `json:"notes,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (p *Plan) Count(action string) int {
	n := 0
	for _, c := range p.Changes {
		if c.Action == action {
			n++
		}
	}
	return n
}

// CheckLimits returns an error if the plan exceeds the configured limits.
func (p *Plan) CheckLimits(cfg config.Sync) error {
	if cfg.MaxDeletes > 0 && p.Count(ActionDelete) > cfg.MaxDeletes {
		return fmt.Errorf("plan deletes %d objects, more than sync.max_deletes=%d", p.Count(ActionDelete), cfg.MaxDeletes)
	}
	if cfg.MaxChanges > 0 && len(p.Changes) > cfg.MaxChanges {
		return fmt.Errorf("plan has %d changes, more than sync.max_changes=%d", len(p.Changes), cfg.MaxChanges)
	}
	return nil
}

func (p *Plan) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func (p *Plan) note(format string, args ...any) {
	p.Notes = append(p.Notes, fmt.Sprintf(format, args...))
}

// Build reads NetBox and computes the changes needed to record s. Any read
// error aborts planning: a partial view of NetBox must never become a plan.
func Build(ctx context.Context, nb *netbox.Client, s *snapshot.Snapshot, cfg config.Sync) (*Plan, error) {
	p := &Plan{Serial: s.System.DeviceSerial(), Changes: []Change{}}
	if p.Serial == "" {
		return nil, fmt.Errorf("no serial number observed; cannot identify the device")
	}
	devs, err := nb.DevicesBySerial(ctx, p.Serial)
	if err != nil {
		return nil, err
	}
	switch len(devs) {
	case 0:
		p.Status = StatusUnregistered
		return p, nil
	case 1:
		p.Status = StatusOK
	default:
		p.Status = StatusDuplicate
		for _, d := range devs {
			p.note("device %d (%s) has serial %s", d.ID, d.Name, p.Serial)
		}
		return p, nil
	}
	dev := devs[0]
	p.DeviceID, p.DeviceName = dev.ID, dev.Name

	checkDeviceType(p, dev, s.System)
	customFields(p, dev, s, cfg)

	ifaces, err := nb.Interfaces(ctx, dev.ID)
	if err != nil {
		return nil, err
	}
	interfaces(p, dev.ID, ifaces, s.NICs, cfg)

	if cfg.Inventory {
		tagID, err := nb.TagID(ctx, cfg.OwnerTag)
		if err != nil {
			return nil, err
		}
		if tagID == 0 {
			p.warn("owner tag %q does not exist in NetBox; inventory items skipped", cfg.OwnerTag)
		} else {
			items, err := nb.InventoryItems(ctx, dev.ID)
			if err != nil {
				return nil, err
			}
			inventory(p, dev.ID, tagID, items, s, cfg.OwnerTag)
		}
	}

	if !s.IsIncomplete(snapshot.SectionIPAddresses) {
		ips, err := nb.IPAddresses(ctx, dev.ID)
		if err != nil {
			return nil, err
		}
		ipAddresses(p, ips, s.IPAddresses)
	}
	// Deletes first, so a replaced part frees its name before the new one is created.
	order := map[string]int{ActionDelete: 0, ActionUpdate: 1, ActionCreate: 2}
	slices.SortStableFunc(p.Changes, func(a, b Change) int { return order[a.Action] - order[b.Action] })
	return p, nil
}

func checkDeviceType(p *Plan, dev netbox.Device, sys snapshot.System) {
	mfr, model := dev.DeviceType.Manufacturer.Name, dev.DeviceType.Model
	if sys.Manufacturer != "" && !looselyEqual(mfr, sys.Manufacturer) {
		p.note("manufacturer differs: NetBox %q, observed %q", mfr, sys.Manufacturer)
	}
	if sys.Product != "" && !looselyEqual(model, sys.Product) {
		p.note("device type differs: NetBox %q, observed %q", model, sys.Product)
	}
}

// looselyEqual tolerates naming differences such as "Dell" vs "Dell Inc.".
func looselyEqual(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a != "" && (strings.Contains(a, b) || strings.Contains(b, a))
}

// timestampLayout is ISO 8601, accepted by both text and datetime custom fields.
const timestampLayout = "2006-01-02T15:04:05-07:00"

func customFields(p *Plan, dev netbox.Device, s *snapshot.Snapshot, cfg config.Sync) {
	values := map[string]any{}
	set := func(key string, v any, ok bool) {
		if ok {
			values[key] = v
		}
	}
	set("cpu_model", s.CPU.Model, s.CPU.Model != "")
	set("cpu_sockets", s.CPU.Sockets, s.CPU.Sockets > 0)
	set("cpu_cores", s.CPU.Cores, s.CPU.Cores > 0)
	set("cpu_threads", s.CPU.Threads, s.CPU.Threads > 0)
	set("memory_total_gb", (s.Memory.TotalBytes+(1<<29))>>30, s.Memory.TotalBytes > 0)
	var models []string
	for _, g := range s.GPUs {
		if g.Model != "" && !slices.Contains(models, g.Model) {
			models = append(models, g.Model)
		}
	}
	set("gpu_model", strings.Join(models, ", "), len(models) > 0)
	set("gpu_count", len(s.GPUs), !s.IsIncomplete(snapshot.SectionGPUs))
	set("bios_version", s.System.BIOSVersion, s.System.BIOSVersion != "")
	osv := s.OS.PrettyName
	if s.OS.KernelVersion != "" {
		osv = strings.TrimSpace(osv + " (kernel " + s.OS.KernelVersion + ")")
	}
	set("os_version", osv, s.OS.PrettyName != "")

	changed := map[string]any{}
	for _, key := range config.CustomFieldKeys {
		name := cfg.CustomFields[key]
		if name == "" {
			continue
		}
		if _, defined := dev.CustomFields[name]; !defined {
			p.warn("custom field %q is not defined for devices in NetBox; %s not written", name, key)
			continue
		}
		if key == "last_collected" {
			p.Touch = map[string]any{name: s.CollectedAt.UTC().Format(timestampLayout)}
			continue
		}
		v, ok := values[key]
		if ok && !sameJSON(dev.CustomFields[name], v) {
			changed[name] = v
		}
	}
	if len(changed) > 0 {
		p.Changes = append(p.Changes, Change{Action: ActionUpdate, Object: ObjectDevice, ID: dev.ID, Name: dev.Name, Fields: changed})
	}
}

// sameJSON compares a value decoded from NetBox with a local one.
func sameJSON(remote, local any) bool {
	b, _ := json.Marshal(local)
	var l any
	_ = json.Unmarshal(b, &l)
	return reflect.DeepEqual(remote, l)
}

func interfaces(p *Plan, deviceID int, ifaces []netbox.Interface, nics []snapshot.NIC, cfg config.Sync) {
	for _, nic := range nics {
		if nic.MAC == "" {
			continue
		}
		byMAC := slices.IndexFunc(ifaces, func(i netbox.Interface) bool { return slices.Contains(i.MACs(), nic.MAC) })
		if byMAC >= 0 {
			if ifaces[byMAC].Name != nic.Name {
				p.note("NIC %s (%s) is interface %q in NetBox", nic.Name, nic.MAC, ifaces[byMAC].Name)
			}
			continue
		}
		byName := slices.IndexFunc(ifaces, func(i netbox.Interface) bool { return i.Name == nic.Name })
		switch {
		case byName >= 0 && len(ifaces[byName].MACs()) == 0:
			p.Changes = append(p.Changes, Change{Action: ActionCreate, Object: ObjectMACAddress, ID: ifaces[byName].ID,
				Name: nic.Name, Fields: map[string]any{"mac_address": nic.MAC}})
		case byName >= 0:
			p.warn("interface %s has MAC %s in NetBox but %s was observed; not changed",
				nic.Name, strings.Join(ifaces[byName].MACs(), ","), nic.MAC)
		case cfg.CreateInterfaces:
			typ, ok := cfg.InterfaceTypes[nic.SpeedMbps]
			if !ok {
				typ = cfg.DefaultInterfaceType
				p.warn("cannot infer interface type of %s (speed %d Mbps); using %q", nic.Name, nic.SpeedMbps, typ)
			}
			p.Changes = append(p.Changes, Change{Action: ActionCreate, Object: ObjectInterface, Name: nic.Name,
				Fields: map[string]any{"device": deviceID, "name": nic.Name, "type": typ, "mac_address": nic.MAC}})
		default:
			p.note("NIC %s (%s) has no interface in NetBox (sync.create_interfaces is off)", nic.Name, nic.MAC)
		}
	}
}

// item is an inventory item the agent wants to exist.
type item struct {
	section string
	name    string
	serial  string
	partID  string
	desc    string
}

// Inventory item names start with a kind prefix, which also tells which
// snapshot section an existing item came from.
var prefixes = map[string]string{
	"CPU ":    snapshot.SectionCPU,
	"Memory ": snapshot.SectionMemoryModules,
	"Disk ":   snapshot.SectionDisks,
	"GPU ":    snapshot.SectionGPUs,
	"NIC ":    snapshot.SectionNICs,
}

func sectionOf(name string) string {
	for pfx, sec := range prefixes {
		if strings.HasPrefix(name, pfx) {
			return sec
		}
	}
	return ""
}

func desiredItems(p *Plan, s *snapshot.Snapshot) []item {
	var out []item
	if s.CPU.Model != "" && s.CPU.Sockets > 0 {
		for _, id := range s.CPU.SocketIDs {
			out = append(out, item{section: snapshot.SectionCPU, name: "CPU " + strconv.Itoa(id), partID: s.CPU.Model,
				desc: fmt.Sprintf("%s, %d cores, %d threads", s.CPU.Model, s.CPU.Cores/s.CPU.Sockets, s.CPU.Threads/s.CPU.Sockets)})
		}
	}
	for _, m := range s.Memory.Modules {
		loc := m.Locator
		if loc == "" {
			loc = m.BankLocator
		}
		if loc == "" && m.Serial == "" {
			p.warn("memory module without locator or serial skipped")
			continue
		}
		out = append(out, item{section: snapshot.SectionMemoryModules, name: "Memory " + loc, serial: m.Serial, partID: m.PartNumber,
			desc: join(fmt.Sprintf("%d GiB", m.SizeBytes>>30), m.Type, speed(m.SpeedMTs), m.Manufacturer)})
	}
	for _, d := range s.Disks {
		out = append(out, item{section: snapshot.SectionDisks, name: "Disk " + d.Name, serial: d.Serial, partID: d.Model,
			desc: join(fmt.Sprintf("%.2f TB", float64(d.SizeBytes)/1e12), strings.ToUpper(d.Transport), d.Model)})
	}
	for _, g := range s.GPUs {
		out = append(out, item{section: snapshot.SectionGPUs, name: "GPU " + g.PCIAddress, serial: g.Serial, partID: g.Model,
			desc: join(g.Model, vbios(g.VBIOSVersion))})
	}
	for _, n := range s.NICs {
		out = append(out, item{section: snapshot.SectionNICs, name: "NIC " + n.Name, partID: n.Driver,
			desc: join(n.MAC, n.Driver, n.PCIAddress)})
	}
	// Enforce NetBox field lengths.
	for i := range out {
		out[i].name = truncate(out[i].name, 64)
		out[i].partID = truncate(out[i].partID, 50)
		out[i].serial = truncate(out[i].serial, 50)
		out[i].desc = truncate(out[i].desc, 200)
	}
	return out
}

func inventory(p *Plan, deviceID, tagID int, existing []netbox.InventoryItem, s *snapshot.Snapshot, tag string) {
	used := map[int]bool{}
	find := func(f func(netbox.InventoryItem) bool) int {
		return slices.IndexFunc(existing, func(e netbox.InventoryItem) bool { return !used[e.ID] && f(e) })
	}
	for _, want := range desiredItems(p, s) {
		i := -1
		if want.serial != "" {
			i = find(func(e netbox.InventoryItem) bool { return strings.EqualFold(e.Serial, want.serial) })
		}
		if i < 0 {
			i = find(func(e netbox.InventoryItem) bool {
				return e.Name == want.name && (e.Serial == "" || want.serial == "" || strings.EqualFold(e.Serial, want.serial))
			})
		}
		if i >= 0 {
			e := existing[i]
			used[e.ID] = true
			if !e.HasTag(tag) {
				continue // registered by a person: never modified
			}
			fields := map[string]any{}
			diff := func(k, have, wantV string) {
				if wantV != "" && have != wantV {
					fields[k] = wantV
				}
			}
			diff("name", e.Name, want.name)
			diff("serial", e.Serial, want.serial)
			diff("part_id", e.PartID, want.partID)
			diff("description", e.Description, want.desc)
			if !e.Discovered {
				fields["discovered"] = true
			}
			if len(fields) > 0 {
				p.Changes = append(p.Changes, Change{Action: ActionUpdate, Object: ObjectInventoryItem, ID: e.ID, Name: want.name, Fields: fields})
			}
			continue
		}
		if c := find(func(e netbox.InventoryItem) bool { return e.Name == want.name && !e.HasTag(tag) }); c >= 0 {
			p.warn("inventory item %q exists without tag %q and a different serial; not created", want.name, tag)
			continue
		}
		p.Changes = append(p.Changes, Change{Action: ActionCreate, Object: ObjectInventoryItem, Name: want.name, Fields: map[string]any{
			"device": deviceID, "name": want.name, "serial": want.serial, "part_id": want.partID,
			"description": want.desc, "discovered": true, "tags": []int{tagID},
		}})
	}
	for _, e := range existing {
		if used[e.ID] || !e.HasTag(tag) {
			continue
		}
		sec := sectionOf(e.Name)
		if sec == "" || s.IsIncomplete(sec) {
			p.note("inventory item %q not observed but kept (section %q incomplete or unknown)", e.Name, sec)
			continue
		}
		p.Changes = append(p.Changes, Change{Action: ActionDelete, Object: ObjectInventoryItem, ID: e.ID, Name: e.Name})
	}
}

func ipAddresses(p *Plan, registered []netbox.IPAddress, observed []string) {
	host := func(cidr string) string {
		if pf, err := netip.ParsePrefix(cidr); err == nil {
			return pf.Addr().String()
		}
		return cidr
	}
	var nb, seen []string
	for _, ip := range registered {
		nb = append(nb, host(ip.Address))
	}
	for _, ip := range observed {
		seen = append(seen, host(ip))
	}
	for _, ip := range seen {
		if !slices.Contains(nb, ip) {
			p.note("IP %s observed but not assigned to the device in NetBox", ip)
		}
	}
	for _, ip := range nb {
		if !slices.Contains(seen, ip) {
			p.note("IP %s assigned in NetBox but not observed", ip)
		}
	}
}

func join(parts ...string) string {
	var out []string
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

func speed(mts int) string {
	if mts == 0 {
		return ""
	}
	return fmt.Sprintf("%d MT/s", mts)
}

func vbios(v string) string {
	if v == "" {
		return ""
	}
	return "VBIOS " + v
}

// truncate shortens s to n characters, the unit NetBox uses for max_length.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
