package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"daed-next/internal/daemon"
	"daed-next/internal/model"
)

func pruneResetBackups(dir string, keep int) {
	matches, err := filepath.Glob(filepath.Join(dir, "before-reset-*"))
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		fi, errI := os.Stat(matches[i])
		fj, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return matches[i] < matches[j]
		}
		return fi.ModTime().Before(fj.ModTime())
	})
	for _, oldDir := range matches[:len(matches)-keep] {
		_ = os.RemoveAll(oldDir)
	}
}

func resetDefaults(previous model.AppState) model.AppState {
	next := model.ProductionDefaults()
	next.Auth = previous.Auth
	next.UpdateRepo = previous.UpdateRepo
	next.Global.WanInterface = previous.Global.WanInterface
	next.Global.LanInterfaces = append([]string(nil), previous.Global.LanInterfaces...)
	next.Subscriptions = []model.Subscription{}
	next.Nodes = []model.Node{}
	next.Snapshots = previous.Clone().Snapshots
	if previous.DNS.Bind != "" {
		next.DNS.RawDSL = strings.Replace(next.DNS.RawDSL, "bind: '"+next.DNS.Bind+"'", "bind: '"+previous.DNS.Bind+"'", 1)
		next.DNS.Bind = previous.DNS.Bind
	}
	return next
}

func (s *Server) handleResetDefaults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil || req.Confirm != "RESET" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请输入 RESET 确认恢复默认设置"})
		return
	}
	s.mu.RLock()
	previous := s.state.Clone()
	s.mu.RUnlock()

	dataDir := filepath.Dir(s.statePath)
	backup, err := os.MkdirTemp(dataDir, "before-reset-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "创建恢复备份失败: " + err.Error()})
		return
	}
	stateBytes, err := json.MarshalIndent(previous, "", "  ")
	if err == nil {
		err = daemon.WritePrivateFile(filepath.Join(backup, "state.json"), stateBytes)
	}
	var config []byte
	if err == nil {
		var cfgErr error
		config, cfgErr = os.ReadFile(s.mgr.ConfigPath())
		if cfgErr != nil && !os.IsNotExist(cfgErr) {
			err = cfgErr
		} else if cfgErr == nil {
			err = daemon.WritePrivateFile(filepath.Join(backup, "config.dae"), config)
		}
	}
	if err != nil {
		_ = os.RemoveAll(backup)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存恢复备份失败: " + err.Error()})
		return
	}
	pruneResetBackups(dataDir, 3)

	next := resetDefaults(previous)
	snapshotState := previous.Clone()
	snapshotState.Auth = model.AuthConfig{}
	snapshotState.Snapshots = nil
	snapshotBytes, _ := json.Marshal(snapshotState)
	snapshot := model.Snapshot{
		ID:   "reset-" + time.Now().Format("20060102-150405.000000000"),
		Name: "恢复默认设置前", CreatedAt: time.Now().Format(time.RFC3339),
		ConfigDAE: string(config), StateJSON: string(snapshotBytes),
	}
	next.Snapshots = append([]model.Snapshot{snapshot}, next.Snapshots...)
	if len(next.Snapshots) > 20 {
		next.Snapshots = next.Snapshots[:20]
	}
	s.saveCandidate(w, next, s.mgr.Stats().Running, false)
}
