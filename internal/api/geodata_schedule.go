package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var geoScheduleZone = time.FixedZone("北京时间", 8*60*60)

type geoSchedule struct {
	Enabled  bool      `json:"enabled"`
	Mode     string    `json:"mode"`
	Weekday  int       `json:"weekday"`
	Hour     int       `json:"hour"`
	Minute   int       `json:"minute"`
	Revision int64     `json:"revision"`
	NextRun  time.Time `json:"nextRun"`
	LastRun  time.Time `json:"lastRun"`
	Result   string    `json:"result"`
}

func validGeoSchedule(c geoSchedule) bool {
	return (c.Mode == "check" || c.Mode == "update") && c.Weekday >= 0 && c.Weekday <= 6 && c.Hour >= 0 && c.Hour < 24 && c.Minute >= 0 && c.Minute < 60
}
func nextGeoRun(now time.Time, c geoSchedule) time.Time {
	local := now.In(geoScheduleZone)
	n := time.Date(local.Year(), local.Month(), local.Day(), c.Hour, c.Minute, 0, 0, geoScheduleZone)
	days := (c.Weekday - int(local.Weekday()) + 7) % 7
	n = n.AddDate(0, 0, days)
	if !n.After(now) {
		n = n.AddDate(0, 0, 7)
	}
	return n
}
func (s *Server) geoSchedulePath() string {
	return filepath.Join(filepath.Dir(s.statePath), "geodata-schedule.json")
}
func (s *Server) readGeoSchedule() (geoSchedule, error) {
	c := geoSchedule{Mode: "check", Weekday: 0, Hour: 4}
	b, err := os.ReadFile(s.geoSchedulePath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("定时配置损坏，已停止执行: %w", err)
	}
	if !validGeoSchedule(c) {
		return c, fmt.Errorf("定时配置无效，已停止执行")
	}
	return c, nil
}
func (s *Server) writeGeoSchedule(c geoSchedule) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.statePath), ".geodata-schedule-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.geoSchedulePath())
}
func (s *Server) handleGeoScheduleGet(w http.ResponseWriter, r *http.Request) {
	c, err := s.readGeoSchedule()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) handleGeoScheduleSave(w http.ResponseWriter, r *http.Request) {
	var req geoSchedule
	if err := decodeJSONBody(w, r, 4096, &req); err != nil || !validGeoSchedule(req) {
		writeJSON(w, 400, map[string]string{"error": "请选择有效的星期、时间和更新模式"})
		return
	}
	s.mu.RLock()
	enabled := s.state.Auth.Enabled
	s.mu.RUnlock()
	if req.Enabled && !enabled {
		writeJSON(w, 403, map[string]string{"error": "请先启用面板登录鉴权"})
		return
	}
	previous, err := s.readGeoSchedule()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if req.Revision != previous.Revision {
		writeJSON(w, 409, map[string]string{"error": "定时设置已变化，请刷新后重试"})
		return
	}
	req.Revision = previous.Revision + 1
	req.LastRun = previous.LastRun
	req.Result = previous.Result
	req.NextRun = time.Time{}
	if req.Enabled {
		req.NextRun = nextGeoRun(time.Now(), req)
	}
	if err = s.writeGeoSchedule(req); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, req)
}
func (s *Server) runGeoScheduler() {
	if s.mockMode {
		return
	}
	s.runGeoSchedule(time.Now())
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		s.runGeoSchedule(now)
	}
}
func (s *Server) runGeoSchedule(now time.Time) {
	if !s.changeMu.TryLock() {
		return
	}
	defer s.changeMu.Unlock()
	if s.panelRestartPending {
		return
	}
	c, err := s.readGeoSchedule()
	if err != nil || !c.Enabled {
		return
	}
	if c.NextRun.IsZero() {
		c.NextRun = nextGeoRun(now, c)
		_ = s.writeGeoSchedule(c)
		return
	}
	if now.Before(c.NextRun) {
		return
	}
	// Claim the occurrence durably before network work, avoiding duplicate runs
	// after process restart. Outages over 24 hours skip the missed occurrence.
	missed := now.Sub(c.NextRun) > 24*time.Hour
	c.NextRun = nextGeoRun(now, c)
	c.Revision++
	c.LastRun = now
	c.Result = "执行已开始；若面板重启导致中断，本周不重复执行"
	s.mu.RLock()
	auth := s.state.Auth.Enabled
	s.mu.RUnlock()
	if missed {
		c.Result = "错过执行时间超过 24 小时，已跳过"
	} else if !auth {
		c.Result = "未启用登录鉴权，已跳过"
	}
	if s.writeGeoSchedule(c) != nil {
		return
	}
	if missed || !auth {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	results := []string{}
	for _, kind := range []string{"geosite", "geoip"} {
		release, err := s.mgr.CheckGeo(ctx, kind)
		if err != nil {
			results = append(results, kind+" 检测失败: "+err.Error())
			continue
		}
		if !release.Available {
			results = append(results, kind+" 已是最新 "+release.Latest)
			continue
		}
		if c.Mode == "check" {
			results = append(results, kind+" 可更新至 "+release.Latest)
			continue
		}
		if err = s.mgr.InstallGeo(ctx, kind, release.Digest); err != nil {
			results = append(results, kind+" 更新失败: "+err.Error())
			break // Do not reload again after an uncertain core failure.
		}
		results = append(results, kind+" 已更新至 "+release.Latest)
	}
	c.Result = strings.Join(results, "；")
	_ = s.writeGeoSchedule(c)
}
