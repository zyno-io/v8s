package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerSPAFallback(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/a.js":   {Data: []byte("console.log(1)")},
		"problems/x.js": {Data: []byte("x")},
	}
	if !available(fsys) {
		t.Fatal("index.html should make the UI available")
	}
	if available(fstest.MapFS{".keep": {}}) {
		t.Fatal("placeholder-only dist must not be available")
	}
	h := handler(fsys)
	for path, want := range map[string]string{"/": "app", "/clusters/prod": "app", "/assets/a.js": "console.log(1)"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: code=%d body=%q", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: missing CSP", path)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing.js", nil))
	if rec.Code != 404 {
		t.Errorf("missing asset: code=%d", rec.Code)
	}
}
