//go:build !(nvml && linux)

package local

import (
	"errors"

	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

func nvmlGPUs() ([]snapshot.GPU, error) {
	return nil, errors.New("built without NVML support (build tag \"nvml\")")
}
