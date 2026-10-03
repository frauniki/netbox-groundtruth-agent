// Command groundtruth-agent collects hardware facts of the machine it runs on
// and records them in NetBox.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/planner"
	"github.com/frauniki/netbox-groundtruth-agent/internal/sink"
	"github.com/frauniki/netbox-groundtruth-agent/internal/sink/dryrun"
	nbsink "github.com/frauniki/netbox-groundtruth-agent/internal/sink/netbox"
	"github.com/frauniki/netbox-groundtruth-agent/internal/source/local"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK           = 0 // nothing to change
	exitError        = 1
	exitChanges      = 2 // plan: changes pending; apply: changes written
	exitUnregistered = 3 // no device with the observed serial in NetBox
	exitDuplicate    = 4 // several devices share the observed serial
	exitAborted      = 5 // a safety limit was exceeded; nothing written
)

const usage = `Usage: groundtruth-agent <command> [flags]

Commands:
  collect   Print the observed hardware facts as JSON. Does not contact NetBox.
  plan      Show what would change in NetBox (dry run).
  apply     Write the changes to NetBox. Requires -confirm.
  version   Print the version.

Flags:
  -config string   path to the YAML configuration file
  -json            plan/apply: print the plan as JSON instead of text
  -confirm         apply: actually write to NetBox

Exit codes: 0 no changes, 1 error, 2 changes planned/applied,
3 device not registered, 4 duplicate serial, 5 aborted by a safety limit.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitError
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	cfgPath := fs.String("config", "", "")
	asJSON := fs.Bool("json", false, "")
	confirm := fs.Bool("confirm", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}

	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version)
		return exitOK
	case "collect", "plan", "apply":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return exitError
	}

	cfg, err := config.Load(*cfgPath)
	log := newLogger(stderr, cfg.Log.Format)
	if err != nil {
		log.Error("load config", "error", err)
		return exitError
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	r := &report{Command: cmd, Started: time.Now()}
	code := execute(ctx, cmd, cfg, *asJSON, *confirm, stdout, r)
	r.ExitCode = code
	r.log(log)
	if cfg.Metrics.Textfile != "" {
		if err := r.writeMetrics(cfg.Metrics.Textfile); err != nil {
			log.Error("write metrics", "error", err)
		}
	}
	return code
}

func execute(ctx context.Context, cmd string, cfg config.Config, asJSON, confirm bool, stdout io.Writer, r *report) int {
	if cmd == "apply" && !confirm {
		r.Error = "apply writes to NetBox; pass -confirm to proceed (use plan for a dry run)"
		return exitError
	}
	snap, err := local.New(cfg.Collect).Collect(ctx)
	if err != nil {
		r.Error = err.Error()
		return exitError
	}
	r.Warnings = append(r.Warnings, snap.Warnings...)
	if cmd == "collect" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snap); err != nil {
			r.Error = err.Error()
			return exitError
		}
		return exitOK
	}

	token, err := cfg.NetBox.Token()
	if err != nil {
		r.Error = err.Error()
		return exitError
	}
	client, err := netbox.New(cfg.NetBox, token)
	if err == nil {
		err = client.Init(ctx)
	}
	if err != nil {
		r.Error = err.Error()
		return exitError
	}
	plan, err := planner.Build(ctx, client, snap, cfg.Sync)
	if err != nil {
		r.Error = err.Error()
		return exitError
	}
	r.fromPlan(plan)

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(plan)
	} else {
		_, _ = dryrun.Sink{W: stdout}.Apply(ctx, plan)
	}

	switch plan.Status {
	case planner.StatusUnregistered:
		return exitUnregistered
	case planner.StatusDuplicate:
		return exitDuplicate
	}
	if err := plan.CheckLimits(cfg.Sync); err != nil {
		r.Error = "aborted, nothing written: " + err.Error()
		return exitAborted
	}
	if cmd == "apply" {
		var s sink.Sink = nbsink.Sink{Client: client}
		r.Applied, err = s.Apply(ctx, plan)
		if err != nil {
			r.Error = err.Error()
			return exitError
		}
	}
	if len(plan.Changes) > 0 {
		return exitChanges
	}
	return exitOK
}

func newLogger(w io.Writer, format string) *slog.Logger {
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, nil))
	}
	return slog.New(slog.NewJSONHandler(w, nil))
}

// report is the per-run summary logged at the end of every run.
type report struct {
	Command  string
	Started  time.Time
	Status   planner.Status
	Serial   string
	DeviceID int
	Created  int
	Updated  int
	Deleted  int
	Applied  int
	Warnings []string
	Notes    []string
	Error    string
	ExitCode int
}

func (r *report) fromPlan(p *planner.Plan) {
	r.Status, r.Serial, r.DeviceID = p.Status, p.Serial, p.DeviceID
	r.Created = p.Count(planner.ActionCreate)
	r.Updated = p.Count(planner.ActionUpdate)
	r.Deleted = p.Count(planner.ActionDelete)
	r.Warnings = append(r.Warnings, p.Warnings...)
	r.Notes = p.Notes
}

func (r *report) log(log *slog.Logger) {
	attrs := []any{
		"command", r.Command, "exit_code", r.ExitCode, "duration_ms", time.Since(r.Started).Milliseconds(),
		"warnings", nonNil(r.Warnings),
	}
	if r.Command != "collect" {
		attrs = append(attrs, "status", r.Status, "serial", r.Serial, "device_id", r.DeviceID,
			"create", r.Created, "update", r.Updated, "delete", r.Deleted, "applied", r.Applied, "notes", nonNil(r.Notes))
	}
	if r.Error != "" {
		log.Error("run finished", append(attrs, "error", r.Error)...)
		return
	}
	log.Info("run finished", attrs...)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeMetrics writes a node_exporter textfile collector file atomically.
func (r *report) writeMetrics(path string) error {
	success := 0
	if r.Error == "" {
		success = 1
	}
	l := fmt.Sprintf(`command=%q`, r.Command)
	body := fmt.Sprintf(`# HELP groundtruth_agent_last_run_timestamp_seconds Time the last run finished.
# TYPE groundtruth_agent_last_run_timestamp_seconds gauge
groundtruth_agent_last_run_timestamp_seconds{%[1]s} %[2]d
# HELP groundtruth_agent_last_run_success Whether the last run finished without error.
# TYPE groundtruth_agent_last_run_success gauge
groundtruth_agent_last_run_success{%[1]s} %[3]d
# HELP groundtruth_agent_last_run_exit_code Exit code of the last run.
# TYPE groundtruth_agent_last_run_exit_code gauge
groundtruth_agent_last_run_exit_code{%[1]s} %[4]d
# HELP groundtruth_agent_last_run_changes Changes planned by the last run.
# TYPE groundtruth_agent_last_run_changes gauge
groundtruth_agent_last_run_changes{%[1]s,action="create"} %[5]d
groundtruth_agent_last_run_changes{%[1]s,action="update"} %[6]d
groundtruth_agent_last_run_changes{%[1]s,action="delete"} %[7]d
# HELP groundtruth_agent_last_run_applied Changes written to NetBox by the last run.
# TYPE groundtruth_agent_last_run_applied gauge
groundtruth_agent_last_run_applied{%[1]s} %[8]d
`, l, time.Now().Unix(), success, r.ExitCode, r.Created, r.Updated, r.Deleted, r.Applied)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".groundtruth-agent-*.prom")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(body)
	err = errors.Join(err, tmp.Chmod(0o644), tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
