package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOnEmptyDataDirDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	report := Run(context.Background(), Options{
		Addr:      "127.0.0.1:0",
		DataDir:   filepath.Join(dir, "data"),
		BackupDir: filepath.Join(dir, "backup"),
	})
	if len(report.Checks) == 0 {
		t.Fatal("expected checks, got none")
	}
	if !report.OK {
		t.Fatalf("empty-but-clean install must not fail: %+v", report.Checks)
	}
	if report.Summary.Warn == 0 {
		t.Fatalf("expected warnings for a never-started install")
	}
	names := map[string]bool{}
	for _, c := range report.Checks {
		names[c.Name] = true
		if c.Detail == "" {
			t.Errorf("check %q has no detail", c.Name)
		}
	}
	for _, want := range []string{"version", "data dir", "database", "address", "config", "backup"} {
		if !names[want] {
			t.Errorf("missing check %q", want)
		}
	}
}

func TestWriteTextAndJSON(t *testing.T) {
	report := Report{
		OK:      false,
		Checks:  []Check{{Name: "database", Status: StatusFail, Detail: "boom"}},
		Summary: Summary{Fail: 1},
	}
	var text bytes.Buffer
	WriteText(&text, report)
	if !strings.Contains(text.String(), "[FAIL] database") || !strings.Contains(text.String(), "1 failure") {
		t.Fatalf("unexpected text: %s", text.String())
	}
	var js bytes.Buffer
	if err := WriteJSON(&js, report); err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK || decoded.Summary.Fail != 1 || len(decoded.Checks) != 1 {
		t.Fatalf("unexpected json: %s", js.String())
	}
}

func TestCountMigrations(t *testing.T) {
	if n := countMigrations(); n < 15 {
		t.Fatalf("expected at least 15 embedded migrations, got %d", n)
	}
}

func TestProbeWritableRejectsMissingDir(t *testing.T) {
	if err := probeWritable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("probing a missing directory should fail")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:        "512 B",
		1536:       "1.5 KiB",
		5 << 20:    "5.0 MiB",
		3 << 30:    "3.0 GiB",
		1500 << 30: "1.5 TiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
