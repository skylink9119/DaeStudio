package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"daed-next/internal/daemon"
	"daed-next/internal/model"
)

func (s *Server) handleGeoDataStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.state.GeoData
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, s.mgr.InspectGeoData(cfg))
}

func (s *Server) handleGeoDataSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req model.GeoDataConfig
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效: " + err.Error()})
		return
	}
	norm, err := daemon.NormalizeGeoDataConfig(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.mu.Lock()
	previous := s.state.GeoData
	norm.LastUpdatedAt = previous.LastUpdatedAt
	norm.LastStatus = previous.LastStatus
	norm.LastMessage = previous.LastMessage
	s.state.GeoData = norm
	if err := s.persistStateUnlocked(); err != nil {
		s.state.GeoData = previous
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存分流数据库设置失败: " + err.Error()})
		return
	}
	rev := s.state.Revision
	savedCfg := s.state.GeoData
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"revision":      rev,
		"geoData":       savedCfg,
		"geoDataStatus": s.mgr.InspectGeoData(savedCfg),
	})
}

func (s *Server) handleGeoDataUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := decodeJSONBody(w, r, smallBodyLimit, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式无效"})
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = "all"
	}

	msg, status, rev, err := s.executeGeoDataUpdate(r.Context(), target)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok":            false,
			"error":         err.Error(),
			"revision":      rev,
			"geoDataStatus": status,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"message":       msg,
		"revision":      rev,
		"geoDataStatus": status,
	})
}

func (s *Server) executeGeoDataUpdate(ctx context.Context, target string) (string, daemon.GeoDataStatus, string, error) {
	if !s.changeMu.TryLock() {
		return "", daemon.GeoDataStatus{}, "", fmt.Errorf("另一项配置操作正在执行，请稍后重试")
	}
	defer s.changeMu.Unlock()
	if s.panelRestartPending {
		return "", daemon.GeoDataStatus{}, "", fmt.Errorf("面板正在重启")
	}

	s.mu.RLock()
	cfg := s.state.GeoData
	s.mu.RUnlock()

	norm, normErr := daemon.NormalizeGeoDataConfig(cfg)
	if normErr != nil {
		norm = model.DefaultGeoDataConfig()
	}

	msg, updateErr := s.mgr.UpdateGeoData(ctx, norm, target)
	nowStr := time.Now().Format(time.RFC3339)

	s.mu.Lock()
	cur, err := daemon.NormalizeGeoDataConfig(s.state.GeoData)
	if err != nil {
		cur = norm
	}
	if updateErr != nil {
		cur.LastStatus = "error"
		cur.LastMessage = updateErr.Error()
	} else {
		cur.LastUpdatedAt = nowStr
		cur.LastStatus = "ok"
		cur.LastMessage = msg
	}
	prev := s.state.GeoData
	s.state.GeoData = cur
	if pErr := s.persistStateUnlocked(); pErr != nil {
		s.state.GeoData = prev
	}
	rev := s.state.Revision
	finalCfg := s.state.GeoData
	s.mu.Unlock()

	return msg, s.mgr.InspectGeoData(finalCfg), rev, updateErr
}

func (s *Server) runGeoDataScheduler() {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()
	var lastFiredSlot string
	for now := range ticker.C {
		s.mu.RLock()
		cfg := s.state.GeoData
		s.mu.RUnlock()

		due, slotKey := daemon.IsGeoUpdateDue(cfg, lastFiredSlot, now)
		if !due {
			continue
		}
		lastFiredSlot = slotKey
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		_, _, _, _ = s.executeGeoDataUpdate(ctx, "all")
		cancel()
	}
}
