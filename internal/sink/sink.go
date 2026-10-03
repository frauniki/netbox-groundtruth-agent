// Package sink defines where a plan goes: NetBox itself (netbox), the
// terminal (dryrun), or in the future NetBox Diode (diode).
package sink

import (
	"context"

	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
)

// Sink applies a plan. It returns the number of changes applied, which is
// meaningful even when an error stops it part way.
type Sink interface {
	Apply(ctx context.Context, p *planner.Plan) (applied int, err error)
}
