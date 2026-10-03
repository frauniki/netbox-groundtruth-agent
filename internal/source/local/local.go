// Package local collects hardware facts of the machine it runs on, by reading
// sysfs, procfs and the SMBIOS table directly. It needs root to read the
// SMBIOS table and some serial numbers.
package local

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

// Source collects a Snapshot from the local machine.
type Source struct {
	cfg config.Collect
	s   *snapshot.Snapshot
}

func New(cfg config.Collect) *Source {
	if cfg.Root == "" {
		cfg.Root = "/"
	}
	return &Source{cfg: cfg}
}

func (src *Source) Collect(ctx context.Context) (*snapshot.Snapshot, error) {
	src.s = &snapshot.Snapshot{CollectedAt: time.Now().UTC(), IPAddresses: []string{}}
	s := src.s
	s.Hostname = src.read("proc/sys/kernel/hostname")
	src.system()
	src.os()
	table := src.smbiosTable()
	src.cpu(table)
	src.memory(table)
	src.disks()
	src.nics()
	src.gpus()
	if src.cfg.Root == "/" {
		src.ipAddresses()
	} else {
		s.Incomplete = append(s.Incomplete, snapshot.SectionIPAddresses)
	}
	if s.System.DeviceSerial() == "" {
		src.warn(snapshot.SectionSystem, "no system or board serial number found")
	}
	return s, ctx.Err()
}

func (src *Source) path(p string) string { return filepath.Join(src.cfg.Root, p) }

// read returns the trimmed content of a file under root, or "" on error.
func (src *Source) read(p string) string { return readFile(src.path(p)) }

// warn records a problem and marks section incomplete.
func (src *Source) warn(section, format string, args ...any) {
	src.s.Warnings = append(src.s.Warnings, section+": "+fmt.Sprintf(format, args...))
	if !src.s.IsIncomplete(section) {
		src.s.Incomplete = append(src.s.Incomplete, section)
	}
}

func (src *Source) system() {
	const dmi = "sys/class/dmi/id/"
	if _, err := os.Stat(src.path(dmi)); err != nil {
		src.warn(snapshot.SectionSystem, "DMI not available: %v", err)
		return
	}
	src.s.System = snapshot.System{
		Manufacturer: clean(src.read(dmi + "sys_vendor")),
		Product:      clean(src.read(dmi + "product_name")),
		Serial:       clean(src.read(dmi + "product_serial")),
		BoardSerial:  clean(src.read(dmi + "board_serial")),
		BIOSVersion:  clean(src.read(dmi + "bios_version")),
	}
}

func (src *Source) os() {
	o := &src.s.OS
	o.KernelVersion = src.read("proc/sys/kernel/osrelease")
	f, err := os.Open(src.path("etc/os-release"))
	if err != nil {
		src.s.Warnings = append(src.s.Warnings, "os: "+err.Error())
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		}
		switch k {
		case "NAME":
			o.Name = v
		case "VERSION_ID":
			o.Version = v
		case "PRETTY_NAME":
			o.PrettyName = v
		}
	}
}

func (src *Source) smbiosTable() []smbiosStructure {
	b, err := os.ReadFile(src.path("sys/firmware/dmi/tables/DMI"))
	if err != nil {
		src.warn(snapshot.SectionMemoryModules, "SMBIOS table not readable (running as root?): %v", err)
		return nil
	}
	t, err := parseSMBIOS(b)
	if err != nil {
		src.warn(snapshot.SectionMemoryModules, "%v", err)
	}
	return t
}

func (src *Source) cpu(table []smbiosStructure) {
	c := &src.s.CPU
	for line := range strings.Lines(src.read("proc/cpuinfo")) {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			c.Model = strings.TrimSpace(v)
			break
		}
	}
	if c.Model == "" { // arm64 has no "model name"; use SMBIOS Type 4 Processor Version.
		for _, st := range table {
			if st.Type == 4 {
				if c.Model = st.str(0x10); c.Model != "" {
					break
				}
			}
		}
	}
	dirs, _ := filepath.Glob(src.path("sys/devices/system/cpu/cpu[0-9]*"))
	sockets := map[int]bool{}
	cores := map[[2]int]bool{}
	for _, d := range dirs {
		pkg, err1 := strconv.Atoi(readFile(filepath.Join(d, "topology/physical_package_id")))
		core, err2 := strconv.Atoi(readFile(filepath.Join(d, "topology/core_id")))
		if err1 != nil || err2 != nil {
			continue // offline CPU
		}
		sockets[pkg] = true
		cores[[2]int{pkg, core}] = true
		c.Threads++
	}
	for id := range sockets {
		c.SocketIDs = append(c.SocketIDs, id)
	}
	sort.Ints(c.SocketIDs)
	c.Sockets, c.Cores = len(sockets), len(cores)
	if c.Threads == 0 || c.Model == "" {
		src.warn(snapshot.SectionCPU, "CPU model or topology not found")
	}
}

func (src *Source) memory(table []smbiosStructure) {
	m := &src.s.Memory
	m.Modules = []snapshot.MemoryModule{}
	for _, st := range table {
		if st.Type != 17 {
			continue
		}
		if mod, ok := memoryModule(st); ok {
			m.Modules = append(m.Modules, mod)
			m.TotalBytes += mod.SizeBytes
		}
	}
	if len(m.Modules) == 0 {
		src.warn(snapshot.SectionMemoryModules, "no memory modules found in SMBIOS")
		for line := range strings.Lines(src.read("proc/meminfo")) {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.ParseUint(f[1], 10, 64)
				m.TotalBytes = kb << 10
			}
		}
	}
}

func (src *Source) disks() {
	f := src.cfg.Disks
	src.s.Disks = []snapshot.Disk{}
	entries, err := os.ReadDir(src.path("sys/block"))
	if err != nil {
		src.warn(snapshot.SectionDisks, "%v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		base := src.path("sys/block/" + name)
		isNVMe := strings.HasPrefix(name, "nvme")
		// Virtual block devices (loop, dm, md, zram, ...) have no backing device.
		if _, err := os.Stat(base + "/device"); err != nil && !isNVMe {
			continue
		}
		if matchAny(f.ExcludeNames, name) || (f.ExcludeRemovable && readFile(base+"/removable") == "1") {
			continue
		}
		real, _ := filepath.EvalSymlinks(base)
		d := snapshot.Disk{Name: name, Model: readFile(base + "/device/model"), Transport: "unknown"}
		switch {
		case isNVMe:
			d.Transport = "nvme"
		case strings.Contains(real, "/usb"):
			d.Transport = "usb"
		case exists(base + "/device/sas_address"):
			d.Transport = "sas"
		case strings.Contains(real, "/ata"):
			d.Transport = "sata"
		case strings.Contains(real, "/virtio"):
			d.Transport = "virtio"
		}
		if slices.Contains(f.ExcludeTransports, d.Transport) || matchAny(f.ExcludeModels, d.Model) {
			continue
		}
		sectors, _ := strconv.ParseUint(readFile(base+"/size"), 10, 64)
		d.SizeBytes = sectors * 512 // sysfs "size" is always in 512-byte units
		d.Serial = clean(readFile(base + "/device/serial"))
		if d.Serial == "" {
			d.Serial = vpdSerial(base + "/device/vpd_pg80")
		}
		if d.Serial == "" {
			src.s.Warnings = append(src.s.Warnings, "disks: no serial for "+name)
		}
		src.s.Disks = append(src.s.Disks, d)
	}
}

// vpdSerial decodes SCSI VPD page 80h (Unit Serial Number): a 4-byte header
// whose bytes 2-3 are the page length, followed by the ASCII serial.
func vpdSerial(p string) string {
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if 4+n > len(b) {
		n = len(b) - 4
	}
	return clean(strings.Trim(string(b[4:4+n]), " \x00"))
}

func (src *Source) nics() {
	f := src.cfg.NICs
	src.s.NICs = []snapshot.NIC{}
	entries, err := os.ReadDir(src.path("sys/class/net"))
	if err != nil {
		src.warn(snapshot.SectionNICs, "%v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		base := src.path("sys/class/net/" + name)
		// Only physical Ethernet ports: they have a backing device, are not
		// SR-IOV virtual functions and have ARPHRD_ETHER (1) as type.
		if !exists(base+"/device") || exists(base+"/device/physfn") || readFile(base+"/type") != "1" {
			continue
		}
		if matchAny(f.ExcludeNames, name) {
			continue
		}
		dev, _ := filepath.EvalSymlinks(base + "/device")
		if f.ExcludeUSB && strings.Contains(dev, "/usb") {
			continue
		}
		mac := readFile(base + "/bonding_slave/perm_hwaddr") // bond members report the bond's MAC in "address"
		if mac == "" {
			mac = readFile(base + "/address")
		}
		n := snapshot.NIC{Name: name, MAC: strings.ToUpper(mac), PCIAddress: filepath.Base(dev)}
		if sp, err := strconv.Atoi(readFile(base + "/speed")); err == nil && sp > 0 {
			n.SpeedMbps = sp
		}
		if drv, err := filepath.EvalSymlinks(base + "/device/driver"); err == nil {
			n.Driver = filepath.Base(drv)
		}
		src.s.NICs = append(src.s.NICs, n)
	}
}

func (src *Source) gpus() {
	src.s.GPUs = []snapshot.GPU{}
	devs, _ := filepath.Glob(src.path("sys/bus/pci/devices/*"))
	ids := src.pciIDs()
	for _, d := range devs {
		class := strings.TrimPrefix(readFile(d+"/class"), "0x")
		vendor := strings.TrimPrefix(readFile(d+"/vendor"), "0x")
		device := strings.TrimPrefix(readFile(d+"/device"), "0x")
		if !strings.HasPrefix(class, "03") || !slices.Contains(src.cfg.GPUs.PCIVendors, vendor) {
			continue
		}
		model := ids[vendor+":"+device]
		if model == "" {
			model = vendor + ":" + device
		}
		src.s.GPUs = append(src.s.GPUs, snapshot.GPU{Model: model, PCIAddress: filepath.Base(d)})
	}
	if src.cfg.Root != "/" {
		return
	}
	found, err := nvmlGPUs()
	if err != nil {
		if len(src.s.GPUs) > 0 {
			src.s.Warnings = append(src.s.Warnings, "gpus: NVML unavailable, serial numbers not collected: "+err.Error())
		}
		return
	}
	for _, g := range found {
		i := slices.IndexFunc(src.s.GPUs, func(x snapshot.GPU) bool { return x.PCIAddress == g.PCIAddress })
		if i < 0 {
			src.s.GPUs = append(src.s.GPUs, g)
		} else {
			src.s.GPUs[i] = g
		}
	}
}

// pciIDs reads "vendor:device" -> "Vendor Device" names from the pci.ids database.
func (src *Source) pciIDs() map[string]string {
	ids := map[string]string{}
	var f *os.File
	for _, p := range []string{"usr/share/misc/pci.ids", "usr/share/hwdata/pci.ids"} {
		if fh, err := os.Open(src.path(p)); err == nil {
			f = fh
			break
		}
	}
	if f == nil {
		return ids
	}
	defer f.Close()
	var vendor, vendorName string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || line[0] == '#' || strings.HasPrefix(line, "\t\t"):
		case line[0] == '\t':
			if id, name, ok := strings.Cut(line[1:], "  "); ok && vendor != "" {
				ids[vendor+":"+id] = vendorName + " " + name
			}
		case strings.HasPrefix(line, "C "): // device classes follow; no more vendors
			return ids
		default:
			vendor, vendorName, _ = strings.Cut(line, "  ")
		}
	}
	return ids
}

func (src *Source) ipAddresses() {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		src.warn(snapshot.SectionIPAddresses, "%v", err)
		return
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
			src.s.IPAddresses = append(src.s.IPAddresses, n.String())
		}
	}
	sort.Strings(src.s.IPAddresses)
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}
