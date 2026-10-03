package local

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/frauniki/netbox-groundtruth-agent/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestCollectFixture(t *testing.T) {
	cfg := config.Default().Collect
	cfg.Root = "../../../testdata/rootfs"
	s, err := New(cfg).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.CollectedAt = time.Time{}
	got, _ := json.MarshalIndent(s, "", "  ")
	const golden = "../../../testdata/snapshot.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("snapshot differs from %s (run go test -update to accept):\n%s", golden, got)
	}
}

func TestMemoryModuleSize(t *testing.T) {
	mk := func(size uint16, ext uint32) smbiosStructure {
		f := make([]byte, 0x22)
		f[0], f[1] = 17, 0x22
		f[0x0C], f[0x0D] = byte(size), byte(size>>8)
		f[0x1C], f[0x1D], f[0x1E], f[0x1F] = byte(ext), byte(ext>>8), byte(ext>>16), byte(ext>>24)
		return smbiosStructure{Type: 17, Formatted: f}
	}
	for _, tc := range []struct {
		size uint16
		ext  uint32
		want uint64
		ok   bool
	}{
		{0, 0, 0, false},                   // empty slot
		{0xFFFF, 0, 0, false},              // unknown
		{8192, 0, 8 << 30, true},           // MB
		{0x8000 | 512, 0, 512 << 10, true}, // KB granularity
		{0x7FFF, 131072, 128 << 30, true},  // extended size
	} {
		m, ok := memoryModule(mk(tc.size, tc.ext))
		if ok != tc.ok || m.SizeBytes != tc.want {
			t.Errorf("size %#x ext %d: got %d,%v want %d,%v", tc.size, tc.ext, m.SizeBytes, ok, tc.want, tc.ok)
		}
	}
}

func TestParseSMBIOSRejectsTruncated(t *testing.T) {
	if _, err := parseSMBIOS([]byte{17, 0x40, 0, 0}); err == nil {
		t.Error("expected error for structure longer than table")
	}
}
