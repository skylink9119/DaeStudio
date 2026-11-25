package api

import (
	"os"
	"strings"
	"testing"
	"time"

	"daed-next/internal/daemon"
	"daed-next/internal/model"
)

func TestPanelSessionHandoff(t *testing.T) {
	s := &Server{
		statePath: t.TempDir() + "/state.json",
		state:     model.AppState{Auth: model.AuthConfig{Enabled: true, Username: "admin", PasswordHash: "test"}},
		sessions:  map[string]time.Time{},
	}
	token := s.mintSessionLocked()
	if err := s.savePanelSession(token, daemon.PanelVersion); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.panelSessionPath())
	if err != nil || strings.Contains(string(data), token) {
		t.Fatalf("session was not stored privately: %v", err)
	}
	s.sessions = map[string]time.Time{}
	s.restorePanelSession()
	if !s.isValidSessionLocked(token, time.Now()) {
		t.Fatal("upgrade session was lost")
	}
	s.sessions = map[string]time.Time{}
	s.restorePanelSession()
	if s.isValidSessionLocked(token, time.Now()) {
		t.Fatal("handoff was reused")
	}
}
