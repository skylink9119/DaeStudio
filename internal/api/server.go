package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"daed-next/internal/compiler"
	"daed-next/internal/daemon"
	"daed-next/internal/model"
	"daed-next/internal/prober"
	"daed-next/internal/subparser"
)

const (
	maxSessions      = 64
	sessionTTL       = 7 * 24 * time.Hour
	smallBodyLimit   = 4 << 10
	defaultBodyLimit = 2 << 20
	largeBodyLimit   = 16 << 20
)

var (
	cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")
	testNet1    = netip.MustParsePrefix("192.0.2.0/24")
	testNet2    = netip.MustParsePrefix("198.51.100.0/24")
	testNet3    = netip.MustParsePrefix("203.0.113.0/24")
	docIPv6     = netip.MustParsePrefix("2001:db8::/32")
)

type loginBucket struct {
	window time.Time
	failed int
	total  int
}

type Server struct {
	initErr     error
	loginWindow time.Time
	loginByIP   map[string]*loginBucket
	loginMu     sync.Mutex
	mu          sync.RWMutex
	changeMu    sync.Mutex
	statePath   string
	daeBin      string
	mockMode    bool
	state       model.AppState
	mgr         *daemon.Manager
	sessions    map[string]time.Time
	webFS       fs.FS
}

func NewServer(statePath, daeBin string, mockMode bool, mgr *daemon.Manager, webFS fs.FS) *Server {
	s := &Server{
		loginByIP: map[string]*loginBucket{},
		statePath: statePath,
		daeBin:    daeBin,
		mockMode:  mockMode,
		mgr:       mgr,
		sessions:  map[string]time.Time{},
		webFS:     webFS,
	}
	s.initErr = s.loadOrInitState()
	if s.initErr == nil {
		s.syncTrackedEndpoints()
		go s.runSubscriptionScheduler()
	}
	return s
}

func (s *Server) syncTrackedEndpoints() {
	s.mu.RLock()
	enabledSubs := make(map[string]bool, len(s.state.Subscriptions))
	for _, sub := range s.state.Subscriptions {
		if sub.Enabled {
			enabledSubs[sub.ID] = true
		}
	}
	targets := make([]daemon.EndpointTarget, 0, len(s.state.Nodes))
	for _, n := range s.state.Nodes {
		if !n.Enabled || n.Port <= 0 || n.Port > 65535 || strings.TrimSpace(n.Address) == "" {
			continue
		}
		if n.SubID != "" && !enabledSubs[n.SubID] {
			continue
		}
		targets = append(targets, daemon.EndpointTarget{
			Host: n.Address,
			Port: uint16(n.Port),
		})
	}
	s.mu.RUnlock()
	s.mgr.SyncNodeEndpoints(targets)
}

func (s *Server) InitError() error { return s.initErr }

func routingReferencesGroup(rt model.RoutingConfig, name string) bool {
	if strings.TrimSpace(rt.Fallback) == name {
		return true
	}
	for _, r := range rt.Rules {
		if strings.TrimSpace(r.Outbound) == name {
			return true
		}
	}
	return false
}

func (s *Server) initialState() model.AppState {
	base := model.DefaultState()
	if s.mockMode {
		return base
	}
	return resetDefaults(base)
}

func (s *Server) loadOrInitState() error {
	b, err := os.ReadFile(s.statePath)
	if err == nil {
		var st model.AppState
		if json.Unmarshal(b, &st) == nil && st.Version != "" {
			// Auto-upgrade & normalize any legacy Shadowrocket URIs in existing state.json
			upgraded := false
			for i, n := range st.Nodes {
				if norm, ok := subparser.ParseNodeURI(n.URI, n.SubID, n.SubTag, i+1); ok && norm.URI != n.URI {
					st.Nodes[i].URI = norm.URI
					st.Nodes[i].Address = norm.Address
					st.Nodes[i].Port = norm.Port
					upgraded = true
				}
			}
			s.state = st
			if s.state.Revision == "" {
				s.state.Revision = randomToken()
			}
			if upgraded {
				return s.persistStateUnlocked()
			}
			return nil
		}
		return fmt.Errorf("状态文件损坏，已停止启动并保留原文件: %s", s.statePath)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("读取状态文件失败: %w", err)
	}

	// First startup: if an existing config.dae is present on disk, back it up and auto-import it!
	cfgPath := s.mgr.ConfigPath()
	if existingBytes, err := os.ReadFile(cfgPath); err == nil && len(strings.TrimSpace(string(existingBytes))) > 0 {
		bakPath := cfgPath + ".bak"
		if _, statErr := os.Stat(bakPath); statErr != nil {
			if err := daemon.WritePrivateFile(bakPath, existingBytes); err != nil {
				return err
			}
		}
		cleanBase := s.initialState()
		cleanBase.Subscriptions = []model.Subscription{}
		cleanBase.Nodes = []model.Node{}
		cleanBase.Groups = []model.Group{}
		if imported, impErr := compiler.ParseConfigDAE(string(existingBytes), cleanBase); impErr == nil {
			if len(imported.Groups) == 0 && routingReferencesGroup(imported.Routing, "proxy") {
				imported.Groups = []model.Group{
					{
						ID:      "grp-proxy",
						Name:    "proxy",
						Enabled: true,
						Policy:  "min_moving_avg",
						Mode:    "smart",
					},
				}
			}
			snapState := imported.Clone()
			snapState.Auth = model.AuthConfig{}
			snapState.Snapshots = nil
			snapBytes, _ := json.Marshal(snapState)
			imported.Snapshots = []model.Snapshot{
				{
					ID:        fmt.Sprintf("snap-init-%d", time.Now().Unix()),
					Name:      "初始导入备份 (原软路由 config.dae)",
					CreatedAt: time.Now().Format(time.RFC3339),
					ConfigDAE: string(existingBytes),
					StateJSON: string(snapBytes),
				},
			}
			s.state = imported
			return s.persistStateUnlocked()
		}
		// Pre-existing hand-written config.dae could not be parsed on first boot:
		// preserve config.dae untouched on disk, store it as a backup snapshot, and boot cleanly.
		fallbackState := s.initialState()
		fallbackState.Snapshots = []model.Snapshot{
			{
				ID:        fmt.Sprintf("snap-init-raw-%d", time.Now().Unix()),
				Name:      "初始原配置备份 (原样保留未覆盖)",
				CreatedAt: time.Now().Format(time.RFC3339),
				ConfigDAE: string(existingBytes),
			},
		}
		s.state = fallbackState
		return s.persistStateUnlocked()
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	s.state = s.initialState()
	cfg := compiler.RenderConfig(s.state)
	if err := s.mgr.WriteConfigFile(cfg); err != nil {
		return err
	}
	return s.persistStateUnlocked()
}

func (s *Server) persistStateUnlocked() (resultErr error) {
	oldRevision, oldUpdated := s.state.Revision, s.state.UpdatedAt
	defer func() {
		if resultErr != nil {
			s.state.Revision, s.state.UpdatedAt = oldRevision, oldUpdated
		}
	}()
	s.state.Revision = randomToken()
	s.state.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := daemon.WritePrivateFile(s.statePath, b); err != nil {
		s.state.Revision = oldRevision
		return err
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	if s.initErr != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "状态加载失败，请检查服务日志并恢复状态文件", 503)
		})
	}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/update", s.withAuth(s.withChange(s.handleAuthUpdate)))
	mux.HandleFunc("GET /api/v1/bootstrap", s.withAuth(s.handleBootstrap))
	mux.HandleFunc("GET /api/v1/stats", s.withAuth(s.handleStats))
	mux.HandleFunc("GET /api/v1/doctor", s.withAuth(s.handleDoctor))
	mux.HandleFunc("GET /api/v1/core/update", s.withAuth(s.handleCoreCheck))
	mux.HandleFunc("POST /api/v1/core/update", s.withAuth(s.withChange(s.handleCoreInstall)))
	mux.HandleFunc("GET /api/v1/connectivity", s.withAuth(s.handleConnectivity))
	mux.HandleFunc("POST /api/v1/settings/reset", s.withAuth(s.withChange(s.handleResetDefaults)))
	mux.HandleFunc("POST /api/v1/state", s.withAuth(s.withChange(s.handleSaveState)))
	mux.HandleFunc("POST /api/v1/config/render", s.withAuth(s.handleConfigRender))
	mux.HandleFunc("POST /api/v1/service/action", s.withAuth(s.withChange(s.handleServiceAction)))
	mux.HandleFunc("POST /api/v1/subscriptions/parse", s.withAuth(s.withChange(s.handleSubParse)))
	mux.HandleFunc("POST /api/v1/nodes/import", s.withAuth(s.withChange(s.handleNodesImport)))
	mux.HandleFunc("POST /api/v1/nodes/save", s.withAuth(s.withChange(s.handleNodeSave)))
	mux.HandleFunc("POST /api/v1/nodes/probe", s.withAuth(s.withChange(s.handleNodesProbe)))
	mux.HandleFunc("POST /api/v1/config/import", s.withAuth(s.withChange(s.handleConfigImport)))
	mux.HandleFunc("POST /api/v1/routing/parse-dsl", s.withAuth(s.handleParseRoutingDSL))
	mux.HandleFunc("POST /api/v1/dns/parse-dsl", s.withAuth(s.handleParseDNSDSL))
	mux.HandleFunc("POST /api/v1/snapshots", s.withAuth(s.withChange(s.handleCreateSnapshot)))
	mux.HandleFunc("POST /api/v1/snapshots/delete", s.withAuth(s.withChange(s.handleDeleteSnapshot)))
	mux.HandleFunc("POST /api/v1/snapshots/restore", s.withAuth(s.withChange(s.handleRestoreSnapshot)))
	mux.HandleFunc("POST /api/v1/logs/clear", s.withAuth(s.handleClearLogs))

	// Static SPA handler
	subWeb, err := fs.Sub(s.webFS, "web")
	if err == nil {
		fileServer := http.FileServer(http.FS(subWeb))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "endpoint not found"})
				return
			}
			fileServer.ServeHTTP(w, r)
		})
	}

	return s.withCSRFGuard(mux)
}

func (s *Server) withCSRFGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") {
			ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
			if !strings.HasPrefix(ct, "application/json") {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "拒绝非 JSON API 请求 (CSRF Guard)"})
				return
			}
			if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "跨站请求已被拦截 (CSRF Guard)"})
				return
			}
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "跨站来源 Origin 校验失败"})
					return
				}
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("请求必须只包含一个 JSON 对象")
	}
	return nil
}

func clientState(st model.AppState) model.AppState {
	out := st.Clone()
	out.Auth.PasswordHash = ""
	for i := range out.Snapshots {
		out.Snapshots[i].StateJSON = ""
	}
	return out
}

func (s *Server) isValidSessionLocked(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	exp, ok := s.sessions[token]
	return ok && exp.After(now)
}

func (s *Server) mintSessionLocked() string {
	now := time.Now()
	for k, exp := range s.sessions {
		if !exp.After(now) {
			delete(s.sessions, k)
		}
	}
	for len(s.sessions) >= maxSessions {
		var oldestKey string
		var oldestExp time.Time
		for k, exp := range s.sessions {
			if oldestKey == "" || exp.Before(oldestExp) {
				oldestKey = k
				oldestExp = exp
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.sessions, oldestKey)
	}
	tok := newToken()
	s.sessions[tok] = now.Add(sessionTTL)
	return tok
}

func (s *Server) withChange(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.changeMu.TryLock() {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "另一项配置操作正在执行，请等待完成"})
			return
		}
		defer s.changeMu.Unlock()
		if expected := r.Header.Get("If-Match"); expected != "" {
			s.mu.RLock()
			actual := s.state.Revision
			s.mu.RUnlock()
			if expected != actual {
				writeJSON(w, 409, map[string]any{"ok": false, "error": "配置已在其他页面或后台更新。当前编辑已保留，请备份编辑内容后刷新页面重新修改。"})
				return
			}
		}
		s.withAuth(next)(w, r)
	}
}

func parseRemoteAddrIP(remoteAddr string) (netip.Addr, bool) {
	raw := strings.TrimSpace(remoteAddr)
	if raw == "" {
		return netip.Addr{}, false
	}
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	} else {
		host = strings.Trim(raw, "[]")
	}
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func remoteIPKey(remoteAddr string) string {
	if addr, ok := parseRemoteAddrIP(remoteAddr); ok {
		return addr.String()
	}
	return strings.TrimSpace(remoteAddr)
}

func isAllowedUnauthenticatedRemote(remoteAddr string) bool {
	addr, ok := parseRemoteAddrIP(remoteAddr)
	if !ok {
		return false
	}
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		cgnatPrefix.Contains(addr) ||
		testNet1.Contains(addr) ||
		testNet2.Contains(addr) ||
		testNet3.Contains(addr) ||
		docIPv6.Contains(addr)
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		s.mu.RLock()
		enabled := s.state.Auth.Enabled
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		validSession := s.isValidSessionLocked(token, now)
		s.mu.RUnlock()

		if !enabled {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			host = strings.Trim(host, "[]")
			if host != "localhost" && !isAllowedUnauthenticatedRemote(host) {
				writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "未设置密码时请通过路由器 IP 访问面板"})
				return
			}
			if !isAllowedUnauthenticatedRemote(r.RemoteAddr) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"ok":    false,
					"error": "未启用登录密码保护时仅允许局域网或本机访问，请先在内网设置面板密码",
				})
				return
			}
			next(w, r)
			return
		}

		if !validSession {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":       "需要登录认证",
				"authEnabled": true,
			})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效"})
		return
	}

	if !s.loginMu.TryLock() {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, 429, map[string]any{"ok": false, "error": "登录校验繁忙，请稍后重试"})
		return
	}
	defer s.loginMu.Unlock()

	s.mu.Lock()
	if !s.state.Auth.Enabled {
		if !isAllowedUnauthenticatedRemote(r.RemoteAddr) {
			s.mu.Unlock()
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok":    false,
				"error": "未启用登录密码保护时仅允许局域网或本机访问，请先在内网设置面板密码",
			})
			return
		}
		tok := s.mintSessionLocked()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
		return
	}

	ipKey := remoteIPKey(r.RemoteAddr)
	now := time.Now()
	if s.loginByIP == nil {
		s.loginByIP = map[string]*loginBucket{}
	}
	if s.loginWindow.IsZero() || now.Sub(s.loginWindow) >= time.Minute {
		s.loginWindow = now
		clear(s.loginByIP)
	}
	if _, exists := s.loginByIP[ipKey]; !exists && len(s.loginByIP) >= 1024 {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, map[string]any{"ok": false, "error": "登录尝试过于频繁"})
		return
	}
	bucket := s.loginByIP[ipKey]
	if bucket == nil || now.Sub(bucket.window) >= time.Minute {
		bucket = &loginBucket{window: now}
		s.loginByIP[ipKey] = bucket
	}
	if bucket.failed >= 4 || bucket.total >= 20 {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "登录尝试过于频繁，请一分钟后重试"})
		return
	}
	bucket.total++
	expectedUser := s.state.Auth.Username
	storedHash := s.state.Auth.PasswordHash
	s.mu.Unlock()

	userMatch := subtle.ConstantTimeCompare([]byte(req.Username), []byte(expectedUser)) == 1
	passMatch := verifyPassword(req.Password, storedHash)
	var upgradedHash string
	var upgradeErr error
	if userMatch && passMatch && !strings.HasPrefix(storedHash, "pbkdf2-sha256$") {
		upgradedHash, upgradeErr = hashPassword(req.Password)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Auth.Enabled || s.state.Auth.Username != expectedUser || s.state.Auth.PasswordHash != storedHash {
		writeJSON(w, 409, map[string]any{"ok": false, "error": "登录设置已变化，请重新登录"})
		return
	}
	bucket = s.loginByIP[ipKey]
	if bucket == nil {
		bucket = &loginBucket{window: now}
		s.loginByIP[ipKey] = bucket
	}
	if !userMatch || !passMatch {
		bucket.failed++
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "用户名或密码错误"})
		return
	}
	if upgradeErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "密码升级失败"})
		return
	}
	if upgradedHash != "" && s.state.Auth.PasswordHash == storedHash {
		old := s.state.Auth.PasswordHash
		s.state.Auth.PasswordHash = upgradedHash
		if err := s.persistStateUnlocked(); err != nil {
			s.state.Auth.PasswordHash = old
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "密码升级保存失败"})
			return
		}
	}
	tok := s.mintSessionLocked()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

func (s *Server) handleAuthUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  bool   `json:"enabled"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var newPasswordHash string
	if req.Password != "" {
		var err error
		newPasswordHash, err = hashPassword(req.Password)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "密码加密失败"})
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Enabled && newPasswordHash == "" && s.state.Auth.PasswordHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "启用密码保护请先设置登录密码"})
		return
	}
	previousAuth := s.state.Auth
	s.state.Auth.Enabled = req.Enabled
	if strings.TrimSpace(req.Username) != "" {
		s.state.Auth.Username = strings.TrimSpace(req.Username)
	}
	if newPasswordHash != "" {
		s.state.Auth.PasswordHash = newPasswordHash
	}
	if err := s.persistStateUnlocked(); err != nil {
		s.state.Auth = previousAuth
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存鉴权设置失败: " + err.Error()})
		return
	}
	// Revoke all previous sessions when auth configuration or password changes.
	s.sessions = map[string]time.Time{}
	tok := s.mintSessionLocked()
	safeAuth := s.state.Auth
	safeAuth.PasswordHash = ""
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok, "auth": safeAuth, "revision": s.state.Revision})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	s.syncTrackedEndpoints()
	s.mu.RLock()
	st := s.state.Clone()
	s.mu.RUnlock()

	cfg := compiler.RenderConfig(st)
	writeJSON(w, http.StatusOK, map[string]any{
		"state":     clientState(st),
		"revision":  st.Revision,
		"configDae": cfg,
		"stats":     s.mgr.Stats(),
		"doctor":    daemon.RunSystemDoctor(s.daeBin, s.mockMode),
		"logs":      s.mgr.Logs(),
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.syncTrackedEndpoints()
	s.mu.RLock()
	rev := s.state.Revision
	s.mu.RUnlock()
	response := map[string]any{
		"stats":    s.mgr.Stats(),
		"revision": rev,
	}
	// Keep logs enabled by default for existing API clients.
	if r.URL.Query().Get("logs") != "0" {
		response["logs"] = s.mgr.Logs()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, daemon.RunSystemDoctor(s.daeBin, s.mockMode))
}

func (s *Server) handleCoreCheck(w http.ResponseWriter, r *http.Request) {
	release, err := s.mgr.CheckCore(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, release)
}

func (s *Server) handleCoreInstall(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.mu.RLock()
	authEnabled := s.state.Auth.Enabled
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	valid := s.isValidSessionLocked(token, now)
	s.mu.RUnlock()
	if !authEnabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "请先启用面板登录鉴权"})
		return
	}
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "需要重新登录", "authEnabled": true})
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSONBody(w, r, 1024, &req); err != nil || req.Version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择要安装的版本"})
		return
	}
	if err := s.mgr.InstallCore(r.Context(), req.Version); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func mergeSnapshotPayloads(next, prev []model.Snapshot) []model.Snapshot {
	if len(next) == 0 {
		return next
	}
	byID := make(map[string]string, len(prev))
	for _, sn := range prev {
		if sn.ID != "" && sn.StateJSON != "" {
			byID[sn.ID] = sn.StateJSON
		}
	}
	out := make([]model.Snapshot, len(next))
	copy(out, next)
	for i := range out {
		if out[i].StateJSON == "" {
			out[i].StateJSON = byID[out[i].ID]
		}
	}
	return out
}

func (s *Server) handleConfigRender(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State model.AppState `json:"state"`
	}
	if err := decodeJSONBody(w, r, largeBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}
	cfg := compiler.RenderConfig(req.State)
	var outboundErr string
	if err := compiler.ValidateOutbounds(req.State); err != nil {
		outboundErr = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"configDae":   cfg,
		"outboundErr": outboundErr,
	})
}

func (s *Server) handleSaveState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State          model.AppState `json:"state"`
		Apply          bool           `json:"apply"`
		SwitchOutbound bool           `json:"switchOutbound"`
	}
	if err := decodeJSONBody(w, r, largeBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式无效: " + err.Error()})
		return
	}

	s.mu.RLock()
	revision := s.state.Revision
	s.mu.RUnlock()
	if req.State.Revision != revision {
		writeJSON(w, 409, map[string]any{"ok": false, "error": "配置已在其他页面或后台更新。当前编辑已保留，请备份编辑内容后刷新页面重新修改。"})
		return
	}
	s.saveCandidate(w, req.State, req.Apply, req.SwitchOutbound)
}

func (s *Server) saveCandidate(w http.ResponseWriter, state model.AppState, apply, switchOutbound bool) {
	s.mu.RLock()
	previous := s.state.Clone()
	s.mu.RUnlock()
	candidate := state.Clone()
	candidate.Auth = previous.Auth
	candidate.Snapshots = mergeSnapshotPayloads(candidate.Snapshots, previous.Snapshots)
	if err := compiler.ValidateOutbounds(candidate); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	cfg := compiler.RenderConfig(candidate)
	oldConfig, err := os.ReadFile(s.mgr.ConfigPath())
	if err != nil && !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法备份当前配置: " + err.Error()})
		return
	}
	if err := s.mgr.ApplyConfig(cfg, apply, switchOutbound); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.mu.Lock()
	s.state = candidate
	err = s.persistStateUnlocked()
	if err != nil {
		s.state = previous
	}
	committed := clientState(s.state)
	s.mu.Unlock()
	if err != nil {
		message := "保存面板状态失败: " + err.Error()
		if len(oldConfig) > 0 {
			if restoreErr := s.mgr.ApplyConfig(string(oldConfig), apply, false); restoreErr != nil {
				message += "；恢复上一次配置也失败: " + restoreErr.Error()
			}
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": message})
		return
	}

	s.syncTrackedEndpoints()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "state": committed, "revision": committed.Revision, "configDae": cfg, "valid": true, "stats": s.mgr.Stats(),
	})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效"})
		return
	}

	s.mu.RLock()
	st := s.state.Clone()
	s.mu.RUnlock()
	cfg := compiler.RenderConfig(st)

	var err error
	msg := ""
	switch req.Action {
	case "start":
		if err = compiler.ValidateOutbounds(st); err == nil {
			err = s.mgr.Start(cfg)
		}
		msg = "dae 核心已启动"
	case "stop":
		err = s.mgr.Stop()
		msg = "dae 核心已停止"
	case "reload":
		if err = compiler.ValidateOutbounds(st); err == nil {
			err = s.mgr.Reload(cfg)
		}
		msg = "dae 已确认重载完成"
	case "validate":
		ok, m := s.mgr.Validate(cfg)
		if !ok {
			err = fmt.Errorf("%s", m)
		} else {
			msg = m
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未知操作: " + req.Action})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"error":     err.Error(),
			"stats":     s.mgr.Stats(),
			"logs":      s.mgr.Logs(),
			"configDae": cfg,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"message":   msg,
		"stats":     s.mgr.Stats(),
		"logs":      s.mgr.Logs(),
		"configDae": cfg,
	})
}

func (s *Server) handleSubParse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID               string `json:"id"`
		Tag              string `json:"tag"`
		Name             string `json:"name"`
		URL              string `json:"url"`
		RawContent       string `json:"rawContent"`
		UserAgent        string `json:"userAgent"`
		ExcludeKeywords  string `json:"excludeKeywords"`
		AutoUpdateHours  int    `json:"autoUpdateHours"`
		PersistConverted bool   `json:"persistConverted"`
	}
	if err := decodeJSONBody(w, r, largeBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	subID := req.ID
	if subID == "" {
		subID = fmt.Sprintf("sub-%d", time.Now().UnixMilli())
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = fmt.Sprintf("sub_%d", time.Now().Unix()%10000)
	}
	ua := req.UserAgent
	if ua == "" {
		ua = "clash.meta/1.18"
	}

	rawBody := strings.TrimSpace(req.RawContent)
	var meta subparser.SubMeta
	if rawBody == "" && strings.TrimSpace(req.URL) != "" {
		var err error
		rawBody, meta, err = fetchSubscription(strings.TrimSpace(req.URL), ua)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	nodes, detectedFormat, err := subparser.ParseSubscriptionContent(rawBody, subID, tag, req.ExcludeKeywords)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	persistConverted := req.PersistConverted
	if detectedFormat == "clash" || detectedFormat == "singbox" || strings.TrimSpace(req.URL) == "" {
		persistConverted = true
	}

	now := time.Now().Format(time.RFC3339)
	subObj := model.Subscription{
		ID:                subID,
		Tag:               tag,
		Name:              defaultString(req.Name, tag),
		URL:               strings.TrimSpace(req.URL),
		Format:            detectedFormat,
		UserAgent:         ua,
		Enabled:           true,
		AutoUpdateHours:   req.AutoUpdateHours,
		ExcludeKeywords:   req.ExcludeKeywords,
		PersistConverted:  persistConverted,
		NodeCount:         len(nodes),
		UsedUploadBytes:   meta.Upload,
		UsedDownloadBytes: meta.Download,
		TotalTrafficBytes: meta.Total,
		ExpireUnix:        meta.Expire,
		UpdatedAt:         now,
	}

	s.mu.Lock()
	previous := s.state.Clone()
	candidate := previous.Clone()
	found := false
	for i, existing := range candidate.Subscriptions {
		if existing.ID == subID {
			subObj.Enabled = existing.Enabled
			if subObj.TotalTrafficBytes == 0 {
				subObj.TotalTrafficBytes = existing.TotalTrafficBytes
				subObj.UsedUploadBytes = existing.UsedUploadBytes
				subObj.UsedDownloadBytes = existing.UsedDownloadBytes
				subObj.ExpireUnix = existing.ExpireUnix
			}
			candidate.Subscriptions[i] = subObj
			found = true
			break
		}
	}
	if !found {
		candidate.Subscriptions = append(candidate.Subscriptions, subObj)
	}

	var keptNodes []model.Node
	for _, n := range candidate.Nodes {
		if n.SubID != subID {
			keptNodes = append(keptNodes, n)
		}
	}
	candidate.Nodes = append(keptNodes, preserveNodeIDs(candidate.Nodes, nodes, subID)...)
	// A refresh can reorder nodes, but may not invalidate an active pin.
	if found && compiler.ValidateOutbounds(previous) == nil {
		if err := compiler.ValidateOutbounds(candidate); err != nil {
			s.mu.Unlock()
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": "保留原有订阅，更新会使当前出口失效：" + err.Error()})
			return
		}
	}
	s.state = candidate
	if err := s.persistStateUnlocked(); err != nil {
		s.state = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存订阅失败：" + err.Error()})
		return
	}
	st := clientState(s.state)
	cfg := compiler.RenderConfig(s.state)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"detectedFormat": detectedFormat,
		"subscription":   subObj,
		"addedCount":     len(nodes),
		"state":          st,
		"revision":       st.Revision,
		"configDae":      cfg,
	})
}

func (s *Server) handleNodesImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawText string `json:"rawText"`
	}
	if err := decodeJSONBody(w, r, largeBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}

	nodes, format, err := subparser.ParseSubscriptionContent(req.RawText, "", "", "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.mu.Lock()
	previous := s.state.Clone()
	nextNodes := make([]model.Node, 0, len(s.state.Nodes)+len(nodes))
	nextNodes = append(nextNodes, s.state.Nodes...)
	nextNodes = append(nextNodes, nodes...)
	s.state.Nodes = nextNodes
	if err := s.persistStateUnlocked(); err != nil {
		s.state = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存节点失败: " + err.Error()})
		return
	}
	st := clientState(s.state)
	cfg := compiler.RenderConfig(s.state)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"detectedFormat": format,
		"addedCount":     len(nodes),
		"state":          st,
		"revision":       st.Revision,
		"configDae":      cfg,
	})
}

func (s *Server) handleNodesProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeIDs   []string `json:"nodeIds"`
		TimeoutMs int      `json:"timeoutMs"`
	}
	if err := decodeJSONBody(w, r, defaultBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}

	idFilter := map[string]bool{}
	for _, id := range req.NodeIDs {
		idFilter[id] = true
	}

	s.mu.RLock()
	var targets []model.Node
	for _, n := range s.state.Nodes {
		if len(idFilter) == 0 || idFilter[n.ID] {
			targets = append(targets, n)
		}
	}
	s.mu.RUnlock()

	timeout := 2500 * time.Millisecond
	if req.TimeoutMs > 200 && req.TimeoutMs <= 10000 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}

	results := prober.ProbeNodes(targets, timeout, 20)
	resByID := map[string]prober.Result{}
	for _, res := range results {
		resByID[res.ID] = res
	}

	s.mu.Lock()
	nextNodes := make([]model.Node, len(s.state.Nodes))
	copy(nextNodes, s.state.Nodes)
	for i, n := range nextNodes {
		if res, ok := resByID[n.ID]; ok {
			nextNodes[i].LatencyMs = res.LatencyMs
			nextNodes[i].Alive = res.Alive
			nextNodes[i].CheckedAt = res.CheckedAt
		}
	}
	previousNodes := s.state.Nodes
	s.state.Nodes = nextNodes
	if err := s.persistStateUnlocked(); err != nil {
		s.state.Nodes = previousNodes
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存测速结果失败: " + err.Error()})
		return
	}
	nodes := append([]model.Node(nil), s.state.Nodes...)
	revision := s.state.Revision
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"results":  results,
		"revision": revision,
		"nodes":    nodes,
	})
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfigDAE string `json:"configDae"`
		Apply     bool   `json:"apply"`
	}
	if err := decodeJSONBody(w, r, largeBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}
	if syntaxErrs := compiler.ValidateSyntax(req.ConfigDAE); len(syntaxErrs) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": strings.Join(syntaxErrs, "; ")})
		return
	}

	s.mu.RLock()
	previous := s.state.Clone()
	s.mu.RUnlock()

	newState, err := compiler.ParseConfigDAE(req.ConfigDAE, previous)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	newState.Auth = previous.Auth
	newState.Snapshots = previous.Snapshots
	if err := compiler.ValidateOutbounds(newState); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	rendered := compiler.RenderConfig(newState)
	oldConfig, readErr := os.ReadFile(s.mgr.ConfigPath())
	if readErr != nil && !os.IsNotExist(readErr) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法备份当前配置: " + readErr.Error()})
		return
	}
	if err := s.mgr.ApplyConfig(rendered, req.Apply, false); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.mu.Lock()
	s.state = newState
	persistErr := s.persistStateUnlocked()
	if persistErr != nil {
		s.state = previous
	}
	st := clientState(s.state)
	s.mu.Unlock()
	if persistErr != nil {
		if len(oldConfig) > 0 {
			_ = s.mgr.ApplyConfig(string(oldConfig), req.Apply, false)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存配置状态失败: " + persistErr.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"state":     st,
		"revision":  st.Revision,
		"configDae": rendered,
		"stats":     s.mgr.Stats(),
		"logs":      s.mgr.Logs(),
	})
}

func (s *Server) handleParseRoutingDSL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DSL string `json:"dsl"`
	}
	if err := decodeJSONBody(w, r, defaultBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}
	rules, fb := compiler.ParseRoutingDSL(req.DSL)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"rules":    rules,
		"fallback": fb,
	})
}

func (s *Server) handleParseDNSDSL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DSL string `json:"dsl"`
	}
	if err := decodeJSONBody(w, r, defaultBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}
	dnsCfg := compiler.ParseDNSDSL(req.DSL)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"dns": dnsCfg,
	})
}

func (s *Server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效"})
		return
	}

	s.mu.Lock()
	previous := s.state.Clone()
	cfg := compiler.RenderConfig(previous)
	snapState := previous.Clone()
	snapState.Auth = model.AuthConfig{}
	snapState.Snapshots = nil
	stateBytes, _ := json.Marshal(snapState)

	snap := model.Snapshot{
		ID:        fmt.Sprintf("snap-%d", time.Now().UnixMilli()),
		Name:      defaultString(req.Name, "快照 "+time.Now().Format("01-02 15:04")),
		CreatedAt: time.Now().Format(time.RFC3339),
		ConfigDAE: cfg,
		StateJSON: string(stateBytes),
	}
	nextSnaps := append([]model.Snapshot{snap}, s.state.Snapshots...)
	if len(nextSnaps) > 20 {
		nextSnaps = nextSnaps[:20]
	}
	s.state.Snapshots = nextSnaps
	if err := s.persistStateUnlocked(); err != nil {
		s.state = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存快照失败: " + err.Error()})
		return
	}
	snaps := clientState(s.state).Snapshots
	revision := s.state.Revision
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "snapshots": snaps, "revision": revision})
}

func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Apply bool   `json:"apply"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效"})
		return
	}

	s.mu.RLock()
	previous := s.state.Clone()
	var targetSnap *model.Snapshot
	for i := range previous.Snapshots {
		if previous.Snapshots[i].ID == req.ID {
			cp := previous.Snapshots[i]
			targetSnap = &cp
			break
		}
	}
	s.mu.RUnlock()

	if targetSnap == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "未找到对应快照"})
		return
	}

	var restored model.AppState
	if targetSnap.StateJSON != "" && json.Unmarshal([]byte(targetSnap.StateJSON), &restored) == nil && restored.Version != "" {
		restored.Auth = previous.Auth
		restored.Snapshots = previous.Snapshots
	} else {
		var err error
		restored, err = compiler.ParseConfigDAE(targetSnap.ConfigDAE, previous)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		restored.Auth = previous.Auth
		restored.Snapshots = previous.Snapshots
	}

	if err := compiler.ValidateOutbounds(restored); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	rendered := compiler.RenderConfig(restored)
	oldConfig, readErr := os.ReadFile(s.mgr.ConfigPath())
	if readErr != nil && !os.IsNotExist(readErr) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法备份当前配置: " + readErr.Error()})
		return
	}
	if err := s.mgr.ApplyConfig(rendered, req.Apply, false); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.mu.Lock()
	s.state = restored
	persistErr := s.persistStateUnlocked()
	if persistErr != nil {
		s.state = previous
	}
	st := clientState(s.state)
	s.mu.Unlock()
	if persistErr != nil {
		if len(oldConfig) > 0 {
			_ = s.mgr.ApplyConfig(string(oldConfig), req.Apply, false)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存快照恢复状态失败: " + persistErr.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"state":     st,
		"revision":  st.Revision,
		"configDae": rendered,
		"stats":     s.mgr.Stats(),
		"logs":      s.mgr.Logs(),
	})
}

func (s *Server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, smallBodyLimit))
	s.mgr.ClearLogs()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "logs": s.mgr.Logs()})
}

func (s *Server) handleConnectivity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"targets": prober.ProbeConnectivity(),
	})
}

func (s *Server) handleNodeSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Tag  string `json:"tag"`
		URI  string `json:"uri"`
	}
	if err := decodeJSONBody(w, r, defaultBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	parsed, ok := subparser.ParseNodeURI(req.URI, "", "", int(time.Now().Unix()%1000))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "无效的节点链接 URI"})
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		parsed.Name = strings.TrimSpace(req.Name)
		parsed.Region = subparser.DetectRegion(parsed.Name)
	}
	if strings.TrimSpace(req.Tag) != "" {
		parsed.Tag = strings.TrimSpace(req.Tag)
	}

	s.mu.Lock()
	previous := s.state.Clone()
	candidate := previous.Clone()
	if req.ID != "" {
		found := false
		for i, existing := range candidate.Nodes {
			if existing.ID == req.ID {
				parsed.ID = existing.ID
				parsed.SubID = existing.SubID
				parsed.SubTag = existing.SubTag
				parsed.Enabled = existing.Enabled
				parsed.LatencyMs = existing.LatencyMs
				parsed.Alive = existing.Alive
				parsed.CheckedAt = existing.CheckedAt
				candidate.Nodes[i] = parsed
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "未找到要编辑的节点"})
			return
		}
		if compiler.ValidateOutbounds(previous) == nil {
			if err := compiler.ValidateOutbounds(candidate); err != nil {
				s.mu.Unlock()
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "修改会使当前固定出口失效：" + err.Error()})
				return
			}
		}
	} else {
		candidate.Nodes = append(candidate.Nodes, parsed)
	}
	s.state = candidate
	if err := s.persistStateUnlocked(); err != nil {
		s.state = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存节点失败: " + err.Error()})
		return
	}
	st := clientState(s.state)
	cfg := compiler.RenderConfig(s.state)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"state":     st,
		"revision":  st.Revision,
		"configDae": cfg,
	})
}

func isDisallowedSubscriptionAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		cgnatPrefix.Contains(addr)
}

func newSafeSubscriptionClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy:             nil, // Resolve and validate the actual destination, never delegate to an environment proxy.
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			portNum, err := strconv.Atoi(portStr)
			if err != nil || portNum <= 0 || portNum > 65535 {
				return nil, fmt.Errorf("无效订阅端口")
			}
			cleanHost := host
			if idx := strings.IndexByte(cleanHost, '%'); idx >= 0 {
				cleanHost = cleanHost[:idx]
			}
			var addrs []netip.Addr
			if literal, parseErr := netip.ParseAddr(cleanHost); parseErr == nil {
				addrs = []netip.Addr{literal}
			} else {
				addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
				if err != nil || len(addrs) == 0 {
					return nil, fmt.Errorf("无法解析订阅域名")
				}
			}
			for _, ip := range addrs {
				if isDisallowedSubscriptionAddr(ip) {
					return nil, fmt.Errorf("禁止访问本机或内网私有地址 (SSRF 防护)")
				}
			}
			var lastErr error
			for _, ip := range addrs {
				target := net.JoinHostPort(ip.Unmap().String(), portStr)
				conn, dialErr := dialer.DialContext(ctx, network, target)
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("订阅重定向次数过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("禁止重定向至非 HTTP/HTTPS 协议")
			}
			return nil
		},
	}
}

func fetchSubscription(rawURL, userAgent string) (string, subparser.SubMeta, error) {
	var meta subparser.SubMeta
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") || req.URL.Host == "" {
		return "", meta, fmt.Errorf("无效的 HTTP/HTTPS 订阅地址")
	}
	hostOnly := req.URL.Hostname()
	if idx := strings.IndexByte(hostOnly, '%'); idx >= 0 {
		hostOnly = hostOnly[:idx]
	}
	if strings.EqualFold(hostOnly, "localhost") {
		return "", meta, fmt.Errorf("禁止访问本机或内网私有地址 (SSRF 防护)")
	}
	if literal, parseErr := netip.ParseAddr(hostOnly); parseErr == nil && isDisallowedSubscriptionAddr(literal) {
		return "", meta, fmt.Errorf("禁止访问本机或内网私有地址 (SSRF 防护)")
	}
	req.Header.Set("User-Agent", defaultString(userAgent, "clash.meta/1.18"))
	client := newSafeSubscriptionClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "SSRF 防护") || strings.Contains(err.Error(), "禁止") {
			return "", meta, err
		}
		return "", meta, fmt.Errorf("无法连接订阅服务器，请检查当前网络或使用离线导入")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", meta, fmt.Errorf("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	const limit = 8 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit {
		return "", meta, fmt.Errorf("订阅响应读取失败或超过 8 MiB")
	}
	return string(b), subparser.ParseUserInfoHeader(resp.Header.Get("Subscription-Userinfo")), nil
}

func preserveNodeIDs(existing, fresh []model.Node, subID string) []model.Node {
	remaining := map[string][]model.Node{}
	for _, node := range existing {
		if node.SubID == subID {
			remaining[node.URI] = append(remaining[node.URI], node)
		}
	}
	for i, node := range fresh {
		prior := remaining[node.URI]
		if len(prior) == 0 {
			continue
		}
		fresh[i].ID = prior[0].ID
		fresh[i].Tag = prior[0].Tag
		fresh[i].Enabled = prior[0].Enabled
		fresh[i].LatencyMs = prior[0].LatencyMs
		fresh[i].CheckedAt = prior[0].CheckedAt
		remaining[node.URI] = prior[1:]
	}
	return fresh
}

func (s *Server) runSubscriptionScheduler() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.RLock()
		subs := append([]model.Subscription(nil), s.state.Subscriptions...)
		s.mu.RUnlock()
		for _, sub := range subs {
			if !sub.Enabled || strings.TrimSpace(sub.URL) == "" || sub.AutoUpdateHours <= 0 {
				continue
			}
			last, err := time.Parse(time.RFC3339, sub.UpdatedAt)
			if err == nil && time.Since(last) < time.Duration(sub.AutoUpdateHours)*time.Hour {
				continue
			}
			s.refreshSingleSubBackground(sub)
		}
	}
}

func (s *Server) refreshSingleSubBackground(sub model.Subscription) bool {
	raw, meta, err := fetchSubscription(sub.URL, sub.UserAgent)
	if err != nil {
		return false
	}
	nodes, format, err := subparser.ParseSubscriptionContent(raw, sub.ID, sub.Tag, sub.ExcludeKeywords)
	if err != nil || len(nodes) == 0 || !s.changeMu.TryLock() {
		return false
	}
	defer s.changeMu.Unlock()

	s.mu.RLock()
	candidate := s.state.Clone()
	s.mu.RUnlock()
	found := false
	for i, existing := range candidate.Subscriptions {
		if existing.ID != sub.ID || !existing.Enabled || strings.TrimSpace(existing.URL) != strings.TrimSpace(sub.URL) {
			continue
		}
		next := &candidate.Subscriptions[i]
		next.Format, next.NodeCount, next.UpdatedAt = format, len(nodes), time.Now().Format(time.RFC3339)
		if meta.Total > 0 {
			next.UsedUploadBytes, next.UsedDownloadBytes, next.TotalTrafficBytes, next.ExpireUnix = meta.Upload, meta.Download, meta.Total, meta.Expire
		}
		if format == "clash" || format == "singbox" {
			next.PersistConverted = true
		}
		found = true
		break
	}
	if !found {
		return false
	}
	var kept []model.Node
	for _, n := range candidate.Nodes {
		if n.SubID != sub.ID {
			kept = append(kept, n)
		}
	}
	candidate.Nodes = append(kept, preserveNodeIDs(candidate.Nodes, nodes, sub.ID)...)
	if compiler.ValidateOutbounds(candidate) != nil {
		return false
	}
	oldConfig, _ := os.ReadFile(s.mgr.ConfigPath())
	wasRunning := s.mgr.Stats().Running
	if s.mgr.ApplyConfig(compiler.RenderConfig(candidate), wasRunning, false) != nil {
		return false
	}
	s.mu.Lock()
	previous := s.state.Clone()
	s.state = candidate
	persistErr := s.persistStateUnlocked()
	if persistErr != nil {
		s.state = previous
	}
	s.mu.Unlock()
	if persistErr != nil {
		if len(oldConfig) > 0 {
			_ = s.mgr.ApplyConfig(string(oldConfig), wasRunning, false)
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func defaultString(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}
