package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"daed-next/internal/daemon"
)

// Keep only the upgrading browser's hashed session across a panel restart.
type panelSession struct {
	TokenHash string
	AuthHash  string
	Expires   time.Time
	ResumeBy  time.Time
	Version   string
}

func (s *Server) panelSessionPath() string {
	return filepath.Join(filepath.Dir(s.statePath), ".panel-upgrade-session")
}

func (s *Server) savePanelSession(token, version string) error {
	s.mu.RLock()
	session := panelSession{
		TokenHash: sha256Hex(token),
		AuthHash:  sha256Hex(s.state.Auth.Username + "\x00" + s.state.Auth.PasswordHash),
		Expires:   s.sessions[sha256Hex(token)],
		ResumeBy:  time.Now().Add(5 * time.Minute),
		Version:   version,
	}
	s.mu.RUnlock()
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return daemon.WritePrivateFile(s.panelSessionPath(), data)
}

func (s *Server) restorePanelSession() {
	path := s.panelSessionPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if os.Remove(path) != nil {
		return
	}
	var session panelSession
	now := time.Now()
	if json.Unmarshal(data, &session) != nil || len(session.TokenHash) != 64 ||
		!s.state.Auth.Enabled || session.Version != daemon.PanelVersion ||
		session.AuthHash != sha256Hex(s.state.Auth.Username+"\x00"+s.state.Auth.PasswordHash) ||
		!session.Expires.After(now) || !session.ResumeBy.After(now) {
		return
	}
	s.sessions[session.TokenHash] = session.Expires
}
