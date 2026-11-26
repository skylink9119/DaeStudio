package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeoInstallRequiresAuthenticationEnabled(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleGeoInstall(w, httptest.NewRequest(http.MethodPost, "/api/v1/geodata/update", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d", w.Code)
	}
}
