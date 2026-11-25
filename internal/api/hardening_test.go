package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCorruptStateFailsClosedWithoutOverwritingFiles(t *testing.T) {
	s, _, statePath, cfgPath := newTestServer(t)
	original, _ := os.ReadFile(cfgPath)
	for _, invalid := range []string{"{broken", "{}", "null"} {
		if err := os.WriteFile(statePath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		err := s.loadOrInitState()
		if err == nil {
			t.Fatal("invalid state accepted")
		}
		after, _ := os.ReadFile(statePath)
		cfgAfter, _ := os.ReadFile(cfgPath)
		if string(after) != invalid || string(cfgAfter) != string(original) {
			t.Fatal("recovery overwrote original files")
		}
		s.initErr = err
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/bootstrap", nil))
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if s.loadOrInitState() == nil {
		t.Fatal("unreadable state accepted")
	}
}

func TestStalePageCannotOverwriteNewState(t *testing.T) {
	s, h, _, cfgPath := newTestServer(t)
	old := s.state.Clone()
	newer := old.Clone()
	newer.Global.LogLevel = "error"
	first := doJSON(t, h, "POST", "/api/v1/state", map[string]any{"state": newer}, nil)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	cfg, _ := os.ReadFile(cfgPath)
	second := doJSON(t, h, "POST", "/api/v1/state", map[string]any{"state": old}, nil)
	if second.Code != 409 {
		t.Fatal(second.Code, second.Body.String())
	}
	after, _ := os.ReadFile(cfgPath)
	if string(cfg) != string(after) || s.state.Global.LogLevel != "error" {
		t.Fatal("stale page changed config")
	}
	staleImport := doJSON(t, h, "POST", "/api/v1/config/import", map[string]any{"configDae": string(cfg)}, map[string]string{"If-Match": old.Revision})
	if staleImport.Code != 409 {
		t.Fatal(staleImport.Code)
	}
	current := s.state.Clone()
	third := doJSON(t, h, "POST", "/api/v1/state", map[string]any{"state": current}, nil)
	if third.Code != 200 {
		t.Fatal(third.Code, third.Body.String())
	}
}

func TestPasswordMigrationAndLoginLimit(t *testing.T) {
	s, h, statePath, _ := newTestServer(t)
	s.state.Auth.Enabled = true
	s.state.Auth.Username = "admin"
	s.state.Auth.PasswordHash = sha256Hex("correct")
	login := func(password string) int {
		return doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": password}, nil).Code
	}
	if login("wrong") != 401 {
		t.Fatal("wrong password accepted")
	}
	if login("correct") != 200 {
		t.Fatal("legacy login failed")
	}
	if !strings.HasPrefix(s.state.Auth.PasswordHash, "pbkdf2-sha256$") || !verifyPassword("correct", s.state.Auth.PasswordHash) {
		t.Fatal("migration failed")
	}
	disk, _ := os.ReadFile(statePath)
	if !strings.Contains(string(disk), "pbkdf2-sha256$") {
		t.Fatal("migration not persisted")
	}
	for i := 0; i < 3; i++ {
		if login("wrong") != 401 {
			t.Fatal("early throttle")
		}
	}
	if login("correct") != 429 {
		t.Fatal("limit bypassed")
	}
	s.mu.Lock()
	s.loginWindow = time.Now().Add(-2 * time.Minute)
	s.mu.Unlock()
	if login("correct") != 200 {
		t.Fatal("limit did not expire")
	}
	a, _ := hashPassword("same")
	b, _ := hashPassword("same")
	if a == b || verifyPassword("wrong", a) || verifyPassword("same", "pbkdf2-sha256$999999999999$x$x") {
		t.Fatal("salt/verification failure")
	}
}

func TestFailedPersistencePreservesRevision(t *testing.T) {
	s, _, statePath, _ := newTestServer(t)
	old := s.state.Revision
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if s.persistStateUnlocked() == nil || s.state.Revision != old {
		t.Fatal("revision changed on failure")
	}
}

func TestProbeAndSnapshotReturnRevision(t *testing.T) {
	s, h, _, _ := newTestServer(t)
	// Empty probe target avoids network; snapshots exercise partial-state responses.
	s.mu.Lock()
	s.state.Nodes = nil
	s.mu.Unlock()
	for _, path := range []string{"/api/v1/nodes/probe", "/api/v1/snapshots"} {
		old := s.state.Revision
		res := doJSON(t, h, "POST", path, map[string]any{"name": "test"}, map[string]string{"If-Match": old})
		var body map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		if res.Code != 200 || body["revision"] != s.state.Revision || s.state.Revision == old {
			t.Fatal(path, res.Code, res.Body.String())
		}
	}
}

func TestDeleteSnapshotPersistsAndPreservesConfig(t *testing.T) {
	s, h, statePath, cfgPath := newTestServer(t)
	doJSON(t, h, "POST", "/api/v1/snapshots", map[string]string{"name": "delete me"}, nil)
	id := s.state.Snapshots[0].ID
	cfg, _ := os.ReadFile(cfgPath)
	if r := doJSON(t, h, "POST", "/api/v1/snapshots/delete", map[string]string{}, nil); r.Code != 400 {
		t.Fatal(r.Code)
	}
	if r := doJSON(t, h, "POST", "/api/v1/snapshots/delete", map[string]string{"id": "missing"}, nil); r.Code != 404 {
		t.Fatal(r.Code)
	}
	old := s.state.Revision
	r := doJSON(t, h, "POST", "/api/v1/snapshots/delete", map[string]string{"id": id}, map[string]string{"If-Match": old})
	if r.Code != 200 || len(s.state.Snapshots) != 0 || old == s.state.Revision {
		t.Fatal(r.Code, r.Body.String())
	}
	after, _ := os.ReadFile(cfgPath)
	disk, _ := os.ReadFile(statePath)
	if string(cfg) != string(after) || strings.Contains(string(disk), "delete me") {
		t.Fatal("deletion changed config or did not persist")
	}
	if r := doJSON(t, h, "POST", "/api/v1/snapshots/delete", map[string]string{"id": id}, nil); r.Code != 404 {
		t.Fatal(r.Code)
	}
	doJSON(t, h, "POST", "/api/v1/snapshots", map[string]string{"name": "keep on failure"}, nil)
	id = s.state.Snapshots[0].ID
	os.Remove(statePath)
	os.Mkdir(statePath, 0700)
	r = doJSON(t, h, "POST", "/api/v1/snapshots/delete", map[string]string{"id": id}, nil)
	if r.Code != 500 || len(s.state.Snapshots) != 1 {
		t.Fatal("failed deletion not rolled back", r.Code)
	}
}

func TestReleaseDNSRebindingAndSubscriptionProxyGuard(t *testing.T) {
	_, h, _, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "http://attacker.example/api/v1/bootstrap", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("DNS rebinding host accepted", w.Code)
	}
	client := newSafeSubscriptionClient()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy bypasses SSRF destination checks")
	}
	req = httptest.NewRequest("GET", "http://192.168.100.1:2023/api/v1/bootstrap", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, "missing sensitive response cache guard")
	}
}

func TestProductionInitialStateContainsNoDemoCredentials(t *testing.T) {
	s := &Server{}
	state := s.initialState()
	if len(state.Nodes) != 0 || len(state.Subscriptions) != 0 || len(state.Groups) != 2 || state.Routing.Fallback != "proxy" {
		t.Fatal("production initialized with demo traffic")
	}
}

func TestLoginBusyDoesNotQueuePasswordWork(t *testing.T) {
	s, h, _, _ := newTestServer(t)
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	r := doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "x"}, nil)
	if r.Code != 429 {
		t.Fatal("login work queued", r.Code)
	}
}
