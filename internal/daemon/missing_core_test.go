package daemon

import (
	"daed-next/internal/compiler"
	"daed-next/internal/model"
	"path/filepath"
	"testing"
)

func TestMissingCoreDoesNotSimulateRunningService(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "missing-dae"), filepath.Join(dir, "config.dae"), false)
	if m.mockMode {
		t.Fatal("missing core enabled simulated mode")
	}
	cfg := compiler.RenderConfig(model.ProductionDefaults())
	if ok, _ := m.Validate(cfg); ok {
		t.Fatal("missing core accepted configuration without real validation")
	}
}
