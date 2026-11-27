package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyConfigPreservesOriginalAfterFailedValidation(t *testing.T) {
	if detectDaePID() != 0 {
		t.Skip("requires isolation from real dae processes")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dae-fake")
	err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = validate ] && [ \"$(dirname \"$3\")\" = \"$(dirname \"$0\")\" ]\n"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{daeBin: bin, configPath: filepath.Join(dir, "config.dae"), maxLogs: 20}
	const previous = "global {\n log_level: info\n}\nrouting {\n fallback: direct\n}\n"
	if err := manager.WriteConfigFile(previous); err != nil {
		t.Fatal(err)
	}
	// An unavailable validator must never publish the candidate.
	manager.daeBin = filepath.Join(dir, "does-not-exist")
	if err := manager.ApplyConfig(previous+"\n# candidate", true, true); err == nil {
		t.Fatal("expected validation failure")
	}
	got, err := os.ReadFile(manager.configPath)
	if err != nil || string(got) != previous {
		t.Fatal("previous file changed after failed validation")
	}
	manager.daeBin = bin
	if err := manager.ApplyConfig(previous+"\n# candidate", false, true); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(manager.configPath)
	if err != nil || string(got) != previous+"\n# candidate" {
		t.Fatal("valid candidate was not atomically installed")
	}
}

func TestDynamicDaeBinResolutionAfterDelayedCoreInstall(t *testing.T) {
	dir := t.TempDir()
	lateBin := filepath.Join(dir, "dae")
	cfgPath := filepath.Join(dir, "config.dae")

	// Simulate starting DaeStudio BEFORE dae core is installed (mockMode must stay false!)
	m := &Manager{
		daeBin:     lateBin,
		configPath: cfgPath,
		mockMode:   false,
		maxLogs:    50,
	}
	if m.IsCoreInstalled() || m.Stats().CoreInstalled || m.Stats().MockMode {
		t.Fatalf("expected CoreInstalled=false and MockMode=false before binary exists, got %+v", m.Stats())
	}

	// Now install fake dae binary WITHOUT restarting Manager
	if err := os.WriteFile(lateBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !m.IsCoreInstalled() || !m.Stats().CoreInstalled {
		t.Fatal("expected Manager to dynamically detect newly installed dae binary without restart")
	}
	const cfg = "global {\n  log_level: warn\n}\nrouting {\n  fallback: direct\n}\n"
	if ok, msg := m.Validate(cfg); !ok {
		t.Fatalf("expected Validate to succeed once dae binary appears: %s", msg)
	}
}
