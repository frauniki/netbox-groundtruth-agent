package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
	"github.com/frauniki/netbox-groundtruth-agent/internal/netbox/netboxtest"
)

func TestCommands(t *testing.T) {
	srv := netboxtest.New(t, "4.2.0")
	cf := netboxtest.Obj{}
	for _, k := range config.CustomFieldKeys {
		cf[k] = nil
	}
	dev := srv.Add("devices", netboxtest.Obj{"name": "server-01", "serial": "EXAMPLE-SN-0001", "custom_fields": cf})
	srv.Add("tags", netboxtest.Obj{"name": "managed", "slug": "managed-by-groundtruth"})

	dir := t.TempDir()
	metrics := filepath.Join(dir, "agent.prom")
	write := func(extra string) string {
		p := filepath.Join(dir, "config.yaml")
		cfg := fmt.Sprintf("netbox:\n  url: %s\n  token_env: TEST_NETBOX_TOKEN\ncollect:\n  root: ../../testdata/rootfs\nmetrics:\n  textfile: %s\n%s", srv.URL, metrics, extra)
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("TEST_NETBOX_TOKEN", "0123456789abcdef")
	runCmd := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := run(args, &out, &errOut)
		return code, out.String() + errOut.String()
	}

	cfgPath := write("")
	if code, out := runCmd("collect", "-config", cfgPath); code != exitOK || !strings.Contains(out, "EXAMPLE-SN-0001") {
		t.Fatalf("collect: %d\n%s", code, out)
	}
	if code, out := runCmd("apply", "-config", cfgPath); code != exitError || len(srv.Writes) != 0 {
		t.Fatalf("apply without -confirm: %d, writes %v\n%s", code, srv.Writes, out)
	}

	// Too many stale items: plan reports the abort, apply writes nothing.
	for i := range 11 {
		srv.Add("inventory-items", netboxtest.Obj{"device": dev, "name": fmt.Sprintf("Disk gone%d", i), "tags": []string{"managed-by-groundtruth"}})
	}
	if code, out := runCmd("plan", "-config", cfgPath); code != exitAborted {
		t.Fatalf("plan over limit: %d\n%s", code, out)
	}
	if code, out := runCmd("apply", "-confirm", "-config", cfgPath); code != exitAborted || len(srv.Writes) != 0 {
		t.Fatalf("apply over limit: %d, writes %v\n%s", code, srv.Writes, out)
	}

	cfgPath = write("sync:\n  max_deletes: 20\n")
	if code, out := runCmd("plan", "-config", cfgPath); code != exitChanges {
		t.Fatalf("plan: %d\n%s", code, out)
	}
	if code, out := runCmd("apply", "-confirm", "-config", cfgPath); code != exitChanges {
		t.Fatalf("apply: %d\n%s", code, out)
	}
	if code, out := runCmd("plan", "-config", cfgPath); code != exitOK {
		t.Fatalf("plan after apply: %d\n%s", code, out)
	}
	prom, err := os.ReadFile(metrics)
	if err != nil || !strings.Contains(string(prom), `groundtruth_agent_last_run_success{command="plan"} 1`) {
		t.Errorf("metrics file: %v\n%s", err, prom)
	}

	srv.Add("devices", netboxtest.Obj{"name": "server-02", "serial": "EXAMPLE-SN-0001", "custom_fields": cf})
	if code, _ := runCmd("plan", "-config", cfgPath); code != exitDuplicate {
		t.Errorf("duplicate: got %d", code)
	}
}
