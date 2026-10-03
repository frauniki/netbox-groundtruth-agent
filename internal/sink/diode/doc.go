// Package diode will send snapshots to NetBox Diode instead of the REST API
// and implement sink.Sink.
//
// TODO(phase 4): not implemented. Diode ingests desired state (entities)
// rather than a diff, so it may consume the Snapshot directly instead of a
// planner.Plan; confirm the Diode SDK's entity model and its handling of
// deletions and ownership before implementing.
package diode
