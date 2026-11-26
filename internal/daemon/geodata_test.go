package daemon

import (
	"strings"
	"testing"
	"time"

	"daed-next/internal/model"
)

func TestGeoDataNormalizePresetsAndSchedule(t *testing.T) {
	cfg, err := NormalizeGeoDataConfig(model.GeoDataConfig{})
	if err != nil || cfg.Preset != "loyalsoldier" || cfg.ScheduleMode != "daily" || cfg.ScheduleTime != "04:00" {
		t.Fatalf("unexpected normalized defaults: %+v, err=%v", cfg, err)
	}

	src := ResolveGeoSources(cfg)
	if !strings.Contains(src.GeoIPURL, "Loyalsoldier/v2ray-rules-dat") || !strings.HasSuffix(src.GeoSiteSumURL, "geosite.dat.sha256sum") {
		t.Fatalf("unexpected loyalsoldier URLs: %+v", src)
	}

	// Reject HTTP or private/loopback custom URLs (SSRF guard)
	for _, bad := range []string{
		"http://example.com/geoip.dat",
		"https://127.0.0.1/geoip.dat",
		"https://192.168.1.1/geoip.dat",
		"https://localhost/geoip.dat",
		"https://100.64.0.1/geoip.dat",
	} {
		_, err := NormalizeGeoDataConfig(model.GeoDataConfig{
			Preset:     "custom",
			GeoIPURL:   bad,
			GeoSiteURL: "https://example.com/geosite.dat",
		})
		if err == nil {
			t.Fatalf("expected SSRF/HTTP custom URL %q to be rejected", bad)
		}
	}

	// Valid custom HTTPS URLs accepted
	validCustom, err := NormalizeGeoDataConfig(model.GeoDataConfig{
		Preset:        "custom",
		GeoIPURL:      "https://cdn.example.com/rules/geoip.dat",
		GeoSiteURL:    "https://cdn.example.com/rules/geosite.dat",
		AutoUpdate:    true,
		ScheduleMode:  "weekly",
		ScheduleTime:  "03:30",
		ScheduleWeekday: 0,
	})
	if err != nil {
		t.Fatalf("expected valid custom config accepted, got: %v", err)
	}

	// Test weekly schedule calculation & due window
	loc := time.FixedZone("CST", 8*3600)
	// 2026-11-29 is Sunday (Weekday == 0)
	sunBefore := time.Date(2026, 11, 29, 3, 0, 0, 0, loc)
	nextRun := NextGeoUpdateRun(validCustom, sunBefore)
	if nextRun.Hour() != 3 || nextRun.Minute() != 30 || nextRun.Day() != 29 {
		t.Fatalf("expected Sunday 03:30, got %v", nextRun)
	}
	if due, _ := IsGeoUpdateDue(validCustom, "", sunBefore); due {
		t.Fatal("should not be due before 03:30")
	}

	sunDuring := time.Date(2026, 11, 29, 3, 45, 0, 0, loc)
	due, slotKey := IsGeoUpdateDue(validCustom, "", sunDuring)
	if !due || slotKey == "" {
		t.Fatal("expected weekly update due at Sunday 03:45")
	}
	if dueAgain, _ := IsGeoUpdateDue(validCustom, slotKey, sunDuring); dueAgain {
		t.Fatal("same slotKey must not fire twice")
	}

	// Test interval schedule (every 3 days at 04:00)
	intCfg, _ := NormalizeGeoDataConfig(model.GeoDataConfig{
		Preset:        "loyalsoldier",
		AutoUpdate:    true,
		ScheduleMode:  "interval",
		IntervalDays:  3,
		ScheduleTime:  "04:00",
		LastUpdatedAt: time.Date(2026, 11, 26, 4, 5, 0, 0, loc).Format(time.RFC3339),
	})
	day2 := time.Date(2026, 11, 28, 4, 10, 0, 0, loc)
	if due2, _ := IsGeoUpdateDue(intCfg, "", day2); due2 {
		t.Fatal("interval=3d should not fire after only 2 days")
	}
	day3 := time.Date(2026, 11, 29, 4, 10, 0, 0, loc)
	if due3, _ := IsGeoUpdateDue(intCfg, "", day3); !due3 {
		t.Fatal("interval=3d should fire on day 3 within maintenance window")
	}

	// SHA256sum parser handles standard sha256sum format
	sampleSum := strings.Repeat("b", 64) + "  geosite.dat\n"
	parsedHex, err := parseFirstSHA256Hex(sampleSum)
	if err != nil || parsedHex != strings.Repeat("b", 64) {
		t.Fatalf("unexpected parsed sha256: %q %v", parsedHex, err)
	}
}
