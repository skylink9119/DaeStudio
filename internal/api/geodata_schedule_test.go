package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextGeoRun(t *testing.T) {
	c := geoSchedule{Mode: "update", Weekday: 0, Hour: 4, Minute: 30}
	for _, tc := range []struct{ now, want string }{
		{"2026-11-28T23:00:00+08:00", "2026-11-29T04:30:00+08:00"},
		{"2026-11-29T04:30:00+08:00", "2026-12-06T04:30:00+08:00"},
		{"2026-12-31T12:00:00+08:00", "2027-01-03T04:30:00+08:00"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextGeoRun(now, c).Format(time.RFC3339); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
	c.Minute = 60
	if validGeoSchedule(c) {
		t.Fatal("invalid minute accepted")
	}
}
func TestGeoSchedulerClaimsOccurrenceAndRespectsBusy(t *testing.T) {
	s := &Server{statePath: filepath.Join(t.TempDir(), "state.json")}
	now := time.Now()
	c := geoSchedule{Enabled: true, Mode: "update", Weekday: 0, Hour: 4, NextRun: now.Add(-time.Minute)}
	if err := s.writeGeoSchedule(c); err != nil {
		t.Fatal(err)
	}
	s.changeMu.Lock()
	s.runGeoSchedule(now)
	s.changeMu.Unlock()
	got, _ := s.readGeoSchedule()
	if got.Revision != 0 {
		t.Fatal("busy operation was not deferred")
	}
	// Authentication disabled: execution is recorded but no network or update occurs.
	s.runGeoSchedule(now)
	got, _ = s.readGeoSchedule()
	if got.Revision != 1 || !got.NextRun.After(now) || got.Result != "未启用登录鉴权，已跳过" {
		t.Fatalf("unexpected %+v", got)
	}
	s.runGeoSchedule(now.Add(time.Minute))
	again, _ := s.readGeoSchedule()
	if again.Revision != got.Revision {
		t.Fatal("occurrence duplicated")
	}
}
func TestGeoScheduleCorruptionAndMissedWindow(t *testing.T) {
	s := &Server{statePath: filepath.Join(t.TempDir(), "state.json")}
	if err := os.WriteFile(s.geoSchedulePath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s.runGeoSchedule(time.Now())
	if _, err := s.readGeoSchedule(); err == nil {
		t.Fatal("corruption ignored")
	}
	c := geoSchedule{Enabled: true, Mode: "check", Weekday: 1, Hour: 3, NextRun: time.Now().Add(-48 * time.Hour)}
	if err := s.writeGeoSchedule(c); err != nil {
		t.Fatal(err)
	}
	s.runGeoSchedule(time.Now())
	got, _ := s.readGeoSchedule()
	if got.Result != "错过执行时间超过 24 小时，已跳过" {
		t.Fatal(got.Result)
	}
}
