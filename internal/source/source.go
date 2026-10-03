// Package source defines where snapshots come from.
//
// Phase 1 implements local (the machine the agent runs on). Phase 2 adds
// talos, which collects from Talos nodes through the Talos API.
package source

import (
	"context"

	"github.com/frauniki/netbox-groundtruth-agent/internal/snapshot"
)

// Source produces a Snapshot of one machine.
type Source interface {
	Collect(ctx context.Context) (*snapshot.Snapshot, error)
}
