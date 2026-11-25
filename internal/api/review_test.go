package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"daed-next/internal/daemon"
	"daed-next/internal/subparser"
)

func newTestServer(t *testing.T) (*Server, http.Handler, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.dae")
	statePath := filepath.Join(dir, "state.json")
	mgr := daemon.NewManager("dae-nonexistent", cfgPath, true)
	webFS := fstest.MapFS{
		"web/index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>ok</title>")},
	}
	srv := NewServer(statePath, "dae-nonexistent", true, mgr, webFS)
	return srv, srv.Handler(), statePath, cfgPath
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "http://192.168.100.1:2023"+path, &buf)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCSRFGuardBlocksFormAndCrossOrigin(t *testing.T) {
	_, h, _, _ := newTestServer(t)

	// 1. Form-encoded POST must be blocked (403)
	reqForm := httptest.NewRequest(http.MethodPost, "http://192.168.100.1:2023/api/v1/service/action", strings.NewReader(`{"action":"stop"}`))
	reqForm.Header.Set("Content-Type", "text/plain")
	recForm := httptest.NewRecorder()
	h.ServeHTTP(recForm, reqForm)
	if recForm.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for text/plain POST, got %d", recForm.Code)
	}

	// 2. Cross-origin Origin header must be blocked (403)
	recCross := doJSON(t, h, http.MethodPost, "/api/v1/service/action", map[string]any{"action": "stop"}, map[string]string{
		"Origin": "http://evil.example.com",
	})
	if recCross.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", recCross.Code)
	}

	// 3. Same-origin JSON POST must succeed (200)
	recSame := doJSON(t, h, http.MethodPost, "/api/v1/service/action", map[string]any{"action": "validate"}, map[string]string{
		"Origin": "http://192.168.100.1:2023",
	})
	if recSame.Code != http.StatusOK {
		t.Fatalf("expected 200 for same-origin JSON POST, got %d: %s", recSame.Code, recSame.Body.String())
	}
}

func TestSessionExpiryAndAuthUpdateRevocation(t *testing.T) {
	srv, h, _, _ := newTestServer(t)

	// Mint a pre-auth token while auth is disabled
	loginRec := doJSON(t, h, http.MethodPost, "/api/v1/auth/login", map[string]string{}, nil)
	var loginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)
	if loginResp.Token == "" {
		t.Fatal("expected token from login")
	}

	// Enable auth with password -> must revoke pre-auth token and return new admin token
	authRec := doJSON(t, h, http.MethodPost, "/api/v1/auth/update", map[string]any{
		"enabled":  true,
		"username": "admin",
		"password": "StrongPassword123",
	}, nil)
	if authRec.Code != http.StatusOK {
		t.Fatalf("auth update failed: %d %s", authRec.Code, authRec.Body.String())
	}
	var authResp struct {
		Token string `json:"token"`
		Auth  struct {
			PasswordHash string `json:"passwordHash"`
		} `json:"auth"`
	}
	_ = json.Unmarshal(authRec.Body.Bytes(), &authResp)
	if authResp.Auth.PasswordHash != "" {
		t.Fatal("passwordHash must never be leaked to client")
	}

	// Old pre-auth token must now be rejected (401)
	oldBoot := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, map[string]string{
		"Authorization": "Bearer " + loginResp.Token,
	})
	if oldBoot.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked token, got %d", oldBoot.Code)
	}

	// New admin token works
	newBoot := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, map[string]string{
		"Authorization": "Bearer " + authResp.Token,
	})
	if newBoot.Code != http.StatusOK {
		t.Fatalf("expected 200 for new token, got %d", newBoot.Code)
	}

	// Force-expire the new token and verify 401
	srv.mu.Lock()
	srv.sessions[authResp.Token] = time.Now().Add(-time.Minute)
	srv.mu.Unlock()
	expBoot := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, map[string]string{
		"Authorization": "Bearer " + authResp.Token,
	})
	if expBoot.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired session token, got %d", expBoot.Code)
	}
}

func TestConfigRenderIsReadOnlyAndSnapshotIsLossless(t *testing.T) {
	_, h, statePath, cfgPath := newTestServer(t)

	origCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	origStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	bootRec := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, nil)
	var boot struct {
		State map[string]any `json:"state"`
	}
	_ = json.Unmarshal(bootRec.Body.Bytes(), &boot)

	// Mutate state in memory for preview only
	boot.State["global"].(map[string]any)["logLevel"] = "debug"
	renderRec := doJSON(t, h, http.MethodPost, "/api/v1/config/render", map[string]any{"state": boot.State}, nil)
	if renderRec.Code != http.StatusOK || !strings.Contains(renderRec.Body.String(), "log_level: debug") {
		t.Fatalf("expected rendered preview with log_level: debug, got %d %s", renderRec.Code, renderRec.Body.String())
	}

	// Verify neither config.dae nor state.json on disk changed!
	afterCfg, _ := os.ReadFile(cfgPath)
	afterState, _ := os.ReadFile(statePath)
	if !bytes.Equal(origCfg, afterCfg) || !bytes.Equal(origStateBytes, afterState) {
		t.Fatal("/api/v1/config/render must not mutate config.dae or state.json on disk")
	}

	// Create snapshot, modify state via import, then restore snapshot losslessly
	snapRec := doJSON(t, h, http.MethodPost, "/api/v1/snapshots", map[string]any{"name": "lossless-snap"}, nil)
	var snapResp struct {
		Snapshots []struct {
			ID string `json:"id"`
		} `json:"snapshots"`
	}
	_ = json.Unmarshal(snapRec.Body.Bytes(), &snapResp)
	if len(snapResp.Snapshots) == 0 {
		t.Fatal("expected snapshot created")
	}

	restoreRec := doJSON(t, h, http.MethodPost, "/api/v1/snapshots/restore", map[string]any{
		"id": snapResp.Snapshots[0].ID,
	}, nil)
	if restoreRec.Code != http.StatusOK || !strings.Contains(restoreRec.Body.String(), `"ok":true`) {
		t.Fatalf("snapshot restore failed: %s", restoreRec.Body.String())
	}
}

func TestSubparserTrailingPipeAndAllExcluded(t *testing.T) {
	raw := strings.Join([]string{
		"trojan://pass@hk.example.com:443?sni=hk.example.com#%E9%A6%99%E6%B8%AF%2001",
		"trojan://pass@info.example.com:443?sni=info.example.com#%E5%89%A9%E4%BD%99%E6%B5%81%E9%87%8F%20100GB",
	}, "\n")

	// Trailing '|' must NOT match everything and drop valid nodes!
	nodes, _, err := subparser.ParseSubscriptionContent(raw, "sub-1", "sub_a", "剩余流量|到期|")
	if err != nil {
		t.Fatalf("unexpected error with trailing pipe: %v", err)
	}
	if len(nodes) != 1 || !strings.Contains(nodes[0].Name, "香港") {
		t.Fatalf("expected 1 HK node kept, got %+v", nodes)
	}

	// Excluding every node must return an explicit error
	if _, _, err := subparser.ParseSubscriptionContent(raw, "sub-1", "sub_a", "香港|剩余流量"); err == nil {
		t.Fatal("expected error when all nodes are excluded by filter")
	}
}

func TestConcurrentBootstrapAndProbeRaceFree(t *testing.T) {
	_, h, _, _ := newTestServer(t)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				rec := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, nil)
				if rec.Code != http.StatusOK {
					t.Errorf("bootstrap status %d", rec.Code)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 6; j++ {
				rec := doJSON(t, h, http.MethodPost, "/api/v1/nodes/probe", map[string]any{
					"nodeIds":   []string{"node-hk-01"},
					"timeoutMs": 220,
				}, nil)
				if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
					t.Errorf("unexpected probe status %d", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
}

func TestUnauthenticatedPublicIPRejectedWhileLANAllowed(t *testing.T) {
	_, h, _, _ := newTestServer(t)

	// Public WAN IP (8.8.8.8:54321) must be rejected with 403 when Auth.Enabled == false!
	pubReq := httptest.NewRequest(http.MethodGet, "http://192.168.100.1:2023/api/v1/bootstrap", nil)
	pubReq.RemoteAddr = "8.8.8.8:54321"
	pubRec := httptest.NewRecorder()
	h.ServeHTTP(pubRec, pubReq)
	if pubRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthenticated public IP, got %d: %s", pubRec.Code, pubRec.Body.String())
	}

	pubLoginReq := httptest.NewRequest(http.MethodPost, "http://192.168.100.1:2023/api/v1/auth/login", strings.NewReader(`{}`))
	pubLoginReq.Header.Set("Content-Type", "application/json")
	pubLoginReq.RemoteAddr = "2409:8057:2000::1]:43210"
	pubLoginReq.RemoteAddr = "[2409:8057:2000::1]:43210"
	pubLoginRec := httptest.NewRecorder()
	h.ServeHTTP(pubLoginRec, pubLoginReq)
	if pubLoginRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthenticated public IPv6 login, got %d", pubLoginRec.Code)
	}

	// Private LAN IP (192.168.100.88:54321) must be allowed (200)
	lanReq := httptest.NewRequest(http.MethodGet, "http://192.168.100.1:2023/api/v1/bootstrap", nil)
	lanReq.RemoteAddr = "192.168.100.88:54321"
	lanRec := httptest.NewRecorder()
	h.ServeHTTP(lanRec, lanReq)
	if lanRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for LAN IP, got %d", lanRec.Code)
	}
}

func TestPerIPLoginRateLimitIsolationAndStatsRevision(t *testing.T) {
	srv, h, _, _ := newTestServer(t)
	pwHash, err := hashPassword("AdminPass!2026")
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.state.Auth.Enabled = true
	srv.state.Auth.Username = "admin"
	srv.state.Auth.PasswordHash = pwHash
	srv.mu.Unlock()

	loginFromIP := func(ipPort, pw string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": pw})
		req := httptest.NewRequest(http.MethodPost, "http://192.168.100.1:2023/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ipPort
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Exhaust IP A's failed login quota (4 failures -> 5th returns 429)
	for i := 0; i < 4; i++ {
		if code := loginFromIP("192.168.100.250:40000", "bad").Code; code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d", i+1, code)
		}
	}
	if code := loginFromIP("192.168.100.250:40001", "AdminPass!2026").Code; code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for throttled attacker IP, got %d", code)
	}

	// Legitimate admin on IP B (192.168.100.10) must NOT be locked out by IP A!
	adminRec := loginFromIP("192.168.100.10:51234", "AdminPass!2026")
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for legitimate admin on separate IP, got %d: %s", adminRec.Code, adminRec.Body.String())
	}
	var loginPayload struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(adminRec.Body.Bytes(), &loginPayload)

	// Verify /api/v1/stats returns top-level revision
	statsRec := doJSON(t, h, http.MethodGet, "/api/v1/stats?logs=0", nil, map[string]string{
		"Authorization": "Bearer " + loginPayload.Token,
	})
	var statsBody map[string]any
	_ = json.Unmarshal(statsRec.Body.Bytes(), &statsBody)
	if statsRec.Code != http.StatusOK || statsBody["revision"] != srv.state.Revision {
		t.Fatalf("expected /api/v1/stats to return current revision, got %d %v", statsRec.Code, statsBody)
	}
}

func TestSSRFBlockedInFetchSubscriptionAndResetMissingConfigAndPruning(t *testing.T) {
	// 1. SSRF protection must block loopback, RFC1918 private, and link-local addresses
	for _, targetURL := range []string{
		"http://127.0.0.1:2023/api/v1/bootstrap",
		"http://localhost:8080/sub",
		"http://192.168.100.1:80/cgi-bin/luci",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:2023/sub",
	} {
		if _, _, err := fetchSubscription(targetURL, "clash.meta"); err == nil || !strings.Contains(err.Error(), "SSRF") {
			t.Fatalf("expected SSRF block for %s, got err=%v", targetURL, err)
		}
	}

	// 2. Reset works even if config.dae was missing, and prunes before-reset-* backups to <= 3
	_, h, statePath, cfgPath := newTestServer(t)
	_ = os.Remove(cfgPath)
	for i := 0; i < 5; i++ {
		rec := doJSON(t, h, http.MethodPost, "/api/v1/settings/reset", map[string]string{"confirm": "RESET"}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("reset iteration %d failed: %d %s", i+1, rec.Code, rec.Body.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(statePath), "before-reset-*"))
	if len(backups) != 3 {
		t.Fatalf("expected before-reset-* directories pruned to 3, got %d", len(backups))
	}

	// 3. Verify /api/v1/dns/parse-dsl endpoint
	dnsRec := doJSON(t, h, http.MethodPost, "/api/v1/dns/parse-dsl", map[string]string{
		"dsl": "bind: '127.0.0.1:15353'\nupstream {\n  cndns: 'https://223.5.5.5/dns-query'\n}\nrouting {\n  request {\n    fallback: cndns\n  }\n}",
	}, nil)
	if dnsRec.Code != http.StatusOK || !strings.Contains(dnsRec.Body.String(), `"127.0.0.1:15353"`) {
		t.Fatalf("unexpected /api/v1/dns/parse-dsl response: %d %s", dnsRec.Code, dnsRec.Body.String())
	}
}

func TestFirstBootImportsCleanGroupsAndSurvivesBrokenPreExistingConfig(t *testing.T) {
	// Case A: First boot with single-group config.dae must NOT keep 3 default template groups!
	dirA := t.TempDir()
	cfgPathA := filepath.Join(dirA, "config.dae")
	statePathA := filepath.Join(dirA, "state.json")
	rawValid := `
global {
  log_level: warn
}
node {
  my_node: 'trojan://pass@example.com:443?sni=example.com#Node1'
}
group {
  proxy {
    filter: name(my_node)
    policy: fixed(0)
  }
}
routing {
  fallback: proxy
}
`
	if err := os.WriteFile(cfgPathA, []byte(rawValid), 0600); err != nil {
		t.Fatal(err)
	}
	mgrA := daemon.NewManager("dae-nonexistent", cfgPathA, true)
	srvA := NewServer(statePathA, "dae-nonexistent", true, mgrA, fstest.MapFS{})
	if srvA.InitError() != nil {
		t.Fatalf("unexpected init error: %v", srvA.InitError())
	}
	if len(srvA.state.Groups) != 1 || srvA.state.Groups[0].Name != "proxy" {
		t.Fatalf("expected only 1 imported group ('proxy'), got %+v", srvA.state.Groups)
	}
	if len(srvA.state.Snapshots) != 1 || srvA.state.Snapshots[0].StateJSON == "" {
		t.Fatalf("expected initial snapshot with populated StateJSON, got %+v", srvA.state.Snapshots)
	}

	// Case B: First boot (no state.json) with hand-written syntax-broken config.dae must boot cleanly without overwriting config.dae!
	dirB := t.TempDir()
	cfgPathB := filepath.Join(dirB, "config.dae")
	statePathB := filepath.Join(dirB, "state.json")
	brokenCfg := "global {\n  log_level: warn\n" // unclosed brace
	if err := os.WriteFile(cfgPathB, []byte(brokenCfg), 0600); err != nil {
		t.Fatal(err)
	}
	mgrB := daemon.NewManager("dae-nonexistent", cfgPathB, true)
	srvB := NewServer(statePathB, "dae-nonexistent", true, mgrB, fstest.MapFS{})
	if srvB.InitError() != nil {
		t.Fatalf("expected first boot with broken pre-existing config.dae to degrade gracefully, got: %v", srvB.InitError())
	}
	diskCfgB, _ := os.ReadFile(cfgPathB)
	if string(diskCfgB) != brokenCfg {
		t.Fatal("first boot fallback must NOT overwrite user's pre-existing config.dae")
	}
}

