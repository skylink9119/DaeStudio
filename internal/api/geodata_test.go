package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestGeoDataConfigSaveUpdateAndResetPreservation(t *testing.T) {
	srv, h, _, _ := newTestServer(t)

	// 1. Bootstrap includes geoDataStatus
	bootRec := doJSON(t, h, http.MethodGet, "/api/v1/bootstrap", nil, nil)
	var boot map[string]any
	_ = json.Unmarshal(bootRec.Body.Bytes(), &boot)
	if bootRec.Code != http.StatusOK || boot["geoDataStatus"] == nil {
		t.Fatalf("expected geoDataStatus in bootstrap, got %d", bootRec.Code)
	}

	// 2. Reject SSRF / HTTP custom URLs
	badCfg := doJSON(t, h, http.MethodPost, "/api/v1/geodata/config", map[string]any{
		"preset":     "custom",
		"geoipUrl":   "http://192.168.1.1/geoip.dat",
		"geositeUrl": "https://example.com/geosite.dat",
	}, nil)
	if badCfg.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on SSRF/HTTP custom URL, got %d", badCfg.Code)
	}

	// 3. Save daily 03:30 schedule with Loyalsoldier preset
	saveCfg := doJSON(t, h, http.MethodPost, "/api/v1/geodata/config", map[string]any{
		"preset":       "loyalsoldier",
		"autoUpdate":   true,
		"scheduleMode": "daily",
		"scheduleTime": "03:30",
	}, nil)
	if saveCfg.Code != http.StatusOK || !srv.state.GeoData.AutoUpdate || srv.state.GeoData.ScheduleTime != "03:30" {
		t.Fatalf("expected saved schedule 03:30, got %d %+v", saveCfg.Code, srv.state.GeoData)
	}

	// 4. Trigger mock update and verify LastStatus == "ok"
	updRec := doJSON(t, h, http.MethodPost, "/api/v1/geodata/update", map[string]string{"target": "all"}, nil)
	if updRec.Code != http.StatusOK || srv.state.GeoData.LastStatus != "ok" || srv.state.GeoData.LastUpdatedAt == "" {
		t.Fatalf("expected mock geodata update ok, got %d %s", updRec.Code, updRec.Body.String())
	}

	// 5. Reset defaults preserves GeoData schedule
	resetRec := doJSON(t, h, http.MethodPost, "/api/v1/settings/reset", map[string]string{"confirm": "RESET"}, nil)
	if resetRec.Code != http.StatusOK || !srv.state.GeoData.AutoUpdate || srv.state.GeoData.ScheduleTime != "03:30" {
		t.Fatalf("expected GeoData schedule preserved across reset, got %d %+v", resetRec.Code, srv.state.GeoData)
	}
}
