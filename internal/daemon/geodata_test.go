package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGeoPublishRollback(t *testing.T) {
	p := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	err := publishGeo(p, []byte("new"), func() error {
		b, _ := os.ReadFile(p)
		if string(b) != "new" {
			t.Fatal("candidate not visible to validator")
		}
		return errors.New("incompatible rules")
	})
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, f := range []string{p, p + ".previous"} {
		b, _ := os.ReadFile(f)
		if string(b) != "old" {
			t.Fatal("old database lost")
		}
	}
	if err := publishGeo(p, []byte("valid"), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "valid" {
		t.Fatal("not published")
	}
}
func TestGeoPublishRollbackMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "geosite.dat")
	if err := publishGeo(p, []byte("bad"), func() error { return errors.New("bad") }); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("failed first install left database")
	}
}
func TestGeoSourceAllowlist(t *testing.T) {
	for _, kind := range []string{"../config", "", "https://example.com"} {
		if _, _, err := geoSource(kind); err == nil {
			t.Fatal("unexpected source accepted")
		}
	}
}
