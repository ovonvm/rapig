package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The user decided (2026-10-27) that the data directory is always a sibling
// "data" folder of the executable, independent of the current working
// directory. These tests pin that contract.
func TestDefaultDataDirIsNextToExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	want := filepath.Join(filepath.Dir(exe), "data")
	if got := DefaultDataDir(); got != want {
		t.Fatalf("DefaultDataDir() = %q, want %q", got, want)
	}
}

func TestLoadDefaultDataDirIgnoresWorkdir(t *testing.T) {
	t.Setenv("RAPIG_DATA_DIR", "")
	t.Setenv("RAPIG_ADDR", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != DefaultDataDir() {
		t.Fatalf("DataDir = %q, want executable-relative %q (cwd was %q)", cfg.DataDir, DefaultDataDir(), tmp)
	}
	if filepath.Base(cfg.DataDir) != "data" {
		t.Fatalf("DataDir = %q, want a directory named data", cfg.DataDir)
	}
}

func TestLoadExplicitDataDirWins(t *testing.T) {
	t.Setenv("RAPIG_DATA_DIR", "")
	cfg, err := Load([]string{"-data", "/tmp/rapig-explicit"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "/tmp/rapig-explicit" {
		t.Fatalf("DataDir = %q, want explicit flag value", cfg.DataDir)
	}
}
