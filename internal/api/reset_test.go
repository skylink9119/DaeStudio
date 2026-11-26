package api

import (
	"bytes"
	"daed-next/internal/compiler"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetRequiresConfirmationAndPreservesBackup(t *testing.T) {
	s, h, statePath, configPath := newTestServer(t)
	original, _ := os.ReadFile(statePath)
	rec := doJSON(t, h, http.MethodPost, "/api/v1/settings/reset", map[string]string{}, nil)
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	after, _ := os.ReadFile(statePath)
	if !bytes.Equal(original, after) {
		t.Fatal("invalid confirmation changed state")
	}
	rec = doJSON(t, h, http.MethodPost, "/api/v1/settings/reset", map[string]string{"confirm": "RESET"}, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	s.mu.RLock()
	state := s.state.Clone()
	s.mu.RUnlock()
	if len(state.Nodes) != 0 || len(state.Groups) != 2 || len(state.Subscriptions) != 0 || state.Routing.Fallback != "proxy" {
		t.Fatal("reset defaults wrong")
	}
	if len(state.Snapshots) == 0 || state.Snapshots[0].StateJSON == "" {
		t.Fatal("missing recovery snapshot")
	}
	if err := compiler.ValidateOutbounds(state); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(statePath), "before-reset-*", "state.json"))
	if len(backups) != 1 {
		t.Fatal("missing disk backup")
	}
	info, _ := os.Stat(backups[0])
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions")
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	}
}

func TestResetPreservesLoginAndBindings(t *testing.T) {
	s, _, _, _ := newTestServer(t)
	previous := s.state.Clone()
	previous.Auth.Enabled = true
	previous.Auth.PasswordHash = "secret"
	previous.Global.WanInterface = "pppoe-wan"
	previous.Global.LanInterfaces = []string{"br-test"}
	previous.DNS.Bind = "127.0.0.1:15354"
	next := resetDefaults(previous)
	if !strings.Contains(next.DNS.RawDSL, "bind: '127.0.0.1:15354'") {
		t.Fatal("DSL listener does not match preserved listener")
	}
	if next.Auth != previous.Auth || next.Global.WanInterface != "pppoe-wan" || next.Global.LanInterfaces[0] != "br-test" || next.DNS.Bind != previous.DNS.Bind {
		t.Fatal("binding/auth changed")
	}
}
