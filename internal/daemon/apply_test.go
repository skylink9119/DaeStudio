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
