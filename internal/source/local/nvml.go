//go:build nvml && linux

package local

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

// nvmlGPUs lists NVIDIA GPUs through NVML. libnvidia-ml.so.1 is loaded at
// runtime with dlopen; it is not shipped with this program.
func nvmlGPUs() ([]snapshot.GPU, error) {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml init: %v", ret)
	}
	defer func() { _ = nvml.Shutdown() }()
	driver, _ := nvml.SystemGetDriverVersion()
	n, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml device count: %v", ret)
	}
	var out []snapshot.GPU
	for i := range n {
		d, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return nil, fmt.Errorf("nvml device %d: %v", i, ret)
		}
		g := snapshot.GPU{DriverVersion: driver}
		g.Model, _ = d.GetName()
		g.Serial, _ = d.GetSerial()
		g.UUID, _ = d.GetUUID()
		g.VBIOSVersion, _ = d.GetVbiosVersion()
		if pci, ret := d.GetPciInfo(); ret == nvml.SUCCESS {
			g.PCIAddress = pciBusID(pci.BusId)
		}
		g.Serial = clean(g.Serial)
		out = append(out, g)
	}
	return out, nil
}

// pciBusID turns NVML's "00000000:3B:00.0" into the sysfs form "0000:3b:00.0".
func pciBusID(raw [32]int8) string {
	b := make([]byte, 0, len(raw))
	for _, c := range raw {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	s := strings.ToLower(string(b))
	if len(s) > 12 {
		s = s[len(s)-12:]
	}
	return s
}
