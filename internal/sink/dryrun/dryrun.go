// Package dryrun prints a plan instead of applying it.
package dryrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
)

type Sink struct{ W io.Writer }

func (s Sink) Apply(_ context.Context, p *planner.Plan) (int, error) {
	fmt.Fprintf(s.W, "device: %s (id %d, serial %s) status: %s\n", p.DeviceName, p.DeviceID, p.Serial, p.Status)
	for _, c := range p.Changes {
		fields, _ := json.Marshal(c.Fields)
		if c.Fields == nil {
			fields = nil
		}
		fmt.Fprintf(s.W, "  %-6s %-14s %-24s %s\n", c.Action, c.Object, c.Name, fields)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(s.W, "  note: %s\n", n)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(s.W, "  warning: %s\n", w)
	}
	fmt.Fprintf(s.W, "%d to create, %d to update, %d to delete\n",
		p.Count(planner.ActionCreate), p.Count(planner.ActionUpdate), p.Count(planner.ActionDelete))
	return 0, nil
}
