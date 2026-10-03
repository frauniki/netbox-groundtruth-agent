// Package config loads the agent's YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	NetBox  NetBox  `yaml:"netbox"`
	Collect Collect `yaml:"collect"`
	Sync    Sync    `yaml:"sync"`
	Log     Log     `yaml:"log"`
	Metrics Metrics `yaml:"metrics"`
}

type NetBox struct {
	URL string `yaml:"url"`
	// TokenFile is read first; TokenEnv is used when TokenFile is empty.
	TokenFile string `yaml:"token_file"`
	TokenEnv  string `yaml:"token_env"`
	// CAFile is a PEM bundle used instead of the system roots.
	CAFile  string        `yaml:"ca_file"`
	Timeout time.Duration `yaml:"timeout"`
	// Headers are added to every request. Values are expanded with ${ENV}.
	Headers map[string]string `yaml:"headers"`
	// Auth is "direct" (default) or "iap".
	Auth string `yaml:"auth"`
	IAP  IAP    `yaml:"iap"`
}

type IAP struct {
	// Audience is the OAuth client ID of the IAP-protected resource.
	Audience string `yaml:"audience"`
	// CredentialsFile is a service account key. Empty means Application
	// Default Credentials (e.g. the GCE metadata server).
	CredentialsFile string `yaml:"credentials_file"`
}

type Collect struct {
	// Root is prepended to /sys, /proc, /etc paths. Only useful for testing.
	Root  string      `yaml:"root"`
	Disks DiskFilter  `yaml:"disks"`
	NICs  NICFilter   `yaml:"nics"`
	GPUs  GPUSettings `yaml:"gpus"`
}

type DiskFilter struct {
	// ExcludeNames are glob patterns matched against the kernel name (sda, nvme0n1).
	ExcludeNames      []string `yaml:"exclude_names"`
	ExcludeRemovable  bool     `yaml:"exclude_removable"`
	ExcludeTransports []string `yaml:"exclude_transports"`
	// ExcludeModels are glob patterns matched against the model, e.g. RAID logical drives.
	ExcludeModels []string `yaml:"exclude_models"`
}

type NICFilter struct {
	// ExcludeNames are glob patterns. Virtual interfaces and SR-IOV VFs are always excluded.
	ExcludeNames []string `yaml:"exclude_names"`
	// ExcludeUSB skips USB NICs, such as the host side of a BMC's USB network.
	ExcludeUSB bool `yaml:"exclude_usb"`
}

type GPUSettings struct {
	// PCIVendors are PCI vendor IDs (hex, no 0x) whose display controllers count as GPUs.
	PCIVendors []string `yaml:"pci_vendors"`
}

type Sync struct {
	CreateInterfaces bool `yaml:"create_interfaces"`
	Inventory        bool `yaml:"inventory"`
	// OwnerTag is the slug of the tag marking inventory items owned by the agent.
	OwnerTag string `yaml:"owner_tag"`
	// MaxChanges aborts the run when the plan has more changes. 0 disables the check.
	MaxChanges int `yaml:"max_changes"`
	// MaxDeletes aborts the run when the plan has more deletions. 0 disables the check.
	MaxDeletes int `yaml:"max_deletes"`
	// CustomFields maps agent fields to NetBox custom field names. An empty name disables the field.
	CustomFields map[string]string `yaml:"custom_fields"`
	// InterfaceTypes maps link speed in Mbps to a NetBox interface type.
	InterfaceTypes       map[int]string `yaml:"interface_types"`
	DefaultInterfaceType string         `yaml:"default_interface_type"`
}

type Log struct {
	// Format is "json" (default) or "text".
	Format string `yaml:"format"`
}

type Metrics struct {
	// Textfile is a path for the node_exporter textfile collector. Empty disables it.
	Textfile string `yaml:"textfile"`
}

// Agent field names usable as keys of Sync.CustomFields.
var CustomFieldKeys = []string{
	"cpu_model", "cpu_sockets", "cpu_cores", "cpu_threads", "memory_total_gb",
	"gpu_model", "gpu_count", "bios_version", "os_version", "last_collected",
}

func Default() Config {
	cf := map[string]string{}
	for _, k := range CustomFieldKeys {
		cf[k] = k
	}
	return Config{
		NetBox: NetBox{TokenEnv: "NETBOX_TOKEN", Timeout: 30 * time.Second, Auth: "direct"},
		Collect: Collect{
			Root: "/",
			Disks: DiskFilter{
				ExcludeNames:      []string{"loop*", "ram*", "zram*", "sr*", "fd*", "dm-*", "md*", "nbd*", "rbd*"},
				ExcludeRemovable:  true,
				ExcludeTransports: []string{"usb"},
			},
			NICs: NICFilter{ExcludeUSB: true},
			GPUs: GPUSettings{PCIVendors: []string{"10de"}},
		},
		Sync: Sync{
			Inventory:  true,
			OwnerTag:   "managed-by-groundtruth",
			MaxChanges: 100,
			MaxDeletes: 10,
			// ponytail: speed-only guess; media (copper vs optics) is not visible in sysfs.
			InterfaceTypes: map[int]string{
				100: "100base-tx", 1000: "1000base-t", 2500: "2.5gbase-t", 5000: "5gbase-t",
				10000: "10gbase-x-sfpp", 25000: "25gbase-x-sfp28", 40000: "40gbase-x-qsfpp",
				50000: "50gbase-x-sfp56", 100000: "100gbase-x-qsfp28", 200000: "200gbase-x-qsfp56",
				400000: "400gbase-x-qsfpdd",
			},
			DefaultInterfaceType: "other",
			CustomFields:         cf,
		},
		Log: Log{Format: "json"},
	}
}

// Load reads path on top of Default. Maps are merged key by key, so a
// partial custom_fields or interface_types section only overrides what it names.
func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	for k := range c.Sync.CustomFields {
		if !slices.Contains(CustomFieldKeys, k) {
			return c, fmt.Errorf("sync.custom_fields: unknown key %q", k)
		}
	}
	for k, v := range c.NetBox.Headers {
		c.NetBox.Headers[k] = os.ExpandEnv(v)
	}
	return c, nil
}

// Token returns the NetBox API token from TokenFile or TokenEnv.
func (n NetBox) Token() (string, error) {
	if n.TokenFile != "" {
		b, err := os.ReadFile(n.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if t := os.Getenv(n.TokenEnv); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("no NetBox token: set netbox.token_file or the %s environment variable", n.TokenEnv)
}
