package webassets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedAssetsServeIndexAndSPAFallback(t *testing.T) {
	h := Handler()
	for _, target := range []string{"/", "/queue", "/review/some-item"} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
			t.Fatalf("%s: got status=%d body=%q", target, w.Code, w.Body.String())
		}
	}
}

func TestMissingStaticAssetDoesNotBecomeSPA(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("got status %d", w.Code)
	}
}
