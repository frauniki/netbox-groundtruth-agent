// Package snapshot defines the observed hardware facts of one machine.
// It is deliberately independent of NetBox so that sources and sinks can
// evolve separately.
package snapshot

import (
	"slices"
	"time"
)

// Section names used in Snapshot.Incomplete.
const (
	SectionSystem        = "system"
	SectionCPU           = "cpu"
	SectionMemoryModules = "memory_modules"
	SectionDisks         = "disks"
	SectionNICs          = "nics"
	SectionGPUs          = "gpus"
	SectionIPAddresses   = "ip_addresses"
)

// Snapshot is everything a Source observed about one machine.
type Snapshot struct {
	CollectedAt time.Time `json:"collected_at"`
	Hostname    string    `json:"hostname,omitempty"`
	System      System    `json:"system"`
	OS          OS        `json:"os"`
	CPU         CPU       `json:"cpu"`
	Memory      Memory    `json:"memory"`
	Disks       []Disk    `json:"disks"`
	NICs        []NIC     `json:"nics"`
	GPUs        []GPU     `json:"gpus"`
	// IPAddresses are the global unicast addresses seen on the host, in CIDR form.
	IPAddresses []string `json:"ip_addresses"`
	// Incomplete lists sections that could not be fully collected. Consumers
	// must not treat missing entries of these sections as "removed".
	Incomplete []string `json:"incomplete,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

type System struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	BoardSerial  string `json:"board_serial,omitempty"`
	BIOSVersion  string `json:"bios_version,omitempty"`
}

// DeviceSerial is the serial used to identify the machine: the system serial,
// or the board serial when the system serial is unavailable.
func (s System) DeviceSerial() string {
	if s.Serial != "" {
		return s.Serial
	}
	return s.BoardSerial
}

type OS struct {
	Name          string `json:"name,omitempty"`
	Version       string `json:"version,omitempty"`
	PrettyName    string `json:"pretty_name,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
}

type CPU struct {
	Model   string `json:"model,omitempty"`
	Sockets int    `json:"sockets,omitempty"`
	Cores   int    `json:"cores,omitempty"`
	Threads int    `json:"threads,omitempty"`
	// SocketIDs are the physical package IDs, one per populated socket.
	SocketIDs []int `json:"socket_ids,omitempty"`
}

type Memory struct {
	TotalBytes uint64         `json:"total_bytes,omitempty"`
	Modules    []MemoryModule `json:"modules"`
}

type MemoryModule struct {
	Locator      string `json:"locator"`
	BankLocator  string `json:"bank_locator,omitempty"`
	SizeBytes    uint64 `json:"size_bytes"`
	Type         string `json:"type,omitempty"`
	SpeedMTs     int    `json:"speed_mts,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	PartNumber   string `json:"part_number,omitempty"`
	Serial       string `json:"serial,omitempty"`
}

type Disk struct {
	Name      string `json:"name"`
	Model     string `json:"model,omitempty"`
	Serial    string `json:"serial,omitempty"`
	SizeBytes uint64 `json:"size_bytes"`
	// Transport is one of nvme, sata, sas, usb, virtio or unknown.
	Transport string `json:"transport"`
}

type NIC struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	SpeedMbps  int    `json:"speed_mbps,omitempty"`
	Driver     string `json:"driver,omitempty"`
	PCIAddress string `json:"pci_address,omitempty"`
}

type GPU struct {
	Model         string `json:"model,omitempty"`
	Serial        string `json:"serial,omitempty"`
	UUID          string `json:"uuid,omitempty"`
	PCIAddress    string `json:"pci_address"`
	VBIOSVersion  string `json:"vbios_version,omitempty"`
	DriverVersion string `json:"driver_version,omitempty"`
}

// IsIncomplete reports whether section was not fully collected.
func (s *Snapshot) IsIncomplete(section string) bool {
	return slices.Contains(s.Incomplete, section)
}
