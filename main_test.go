package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRouteProxyAndUnknownHost(t *testing.T) {
	app := &App{config: Config{BaseDomain: "ddman.cc", Routes: []Route{{Name: "app", Scheme: "http", Port: 3000}}}}
	app.proxyTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "127.0.0.1:3000" {
			t.Errorf("wrong target: %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(r.Host + " " + r.URL.Path))}, nil
	})
	for _, tc := range []struct {
		host, path string
		status     int
		body       string
	}{
		{"app.ddman.cc", "/hello", 200, "app.ddman.cc /hello"},
		{"missing.ddman.cc", "/", 404, "route not found"},
		{"app.home.ddman.cc", "/", 404, "unknown hostname"},
	} {
		req := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		app.proxy(w, req)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Fatalf("host %s: got %d %q", tc.host, w.Code, w.Body.String())
		}
	}
}

func TestPathRoutingLongestPrefixAndStrip(t *testing.T) {
	app := &App{config: Config{BaseDomain: "ddman.cc", Routes: []Route{
		{Name: "app", Scheme: "http", Port: 3000},
		{Name: "app", Path: "/api", StripPrefix: true, Scheme: "http", Port: 4000},
		{Name: "app", Path: "/api/v2", Scheme: "http", Port: 5000},
	}}}
	app.proxyTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := r.URL.Host + " " + r.URL.RequestURI()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	for _, tc := range []struct{ path, want string }{
		{"/", "127.0.0.1:3000 /"},
		{"/apix", "127.0.0.1:3000 /apix"},
		{"/api", "127.0.0.1:4000 /"},
		{"/api/users?id=1", "127.0.0.1:4000 /users?id=1"},
		{"/api/v2/users", "127.0.0.1:5000 /api/v2/users"},
	} {
		req := httptest.NewRequest("GET", "http://app.ddman.cc"+tc.path, nil)
		w := httptest.NewRecorder()
		app.proxy(w, req)
		if w.Code != 200 || w.Body.String() != tc.want {
			t.Errorf("%s: got %d %q, want %q", tc.path, w.Code, w.Body.String(), tc.want)
		}
	}
}

func TestAdminRoutePersistenceAndSecretOmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	app, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app.config.Cloudflare.TunnelToken = "secret"
	h := app.adminHandler()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/routes", strings.NewReader(`{"name":"demo","scheme":"http","port":3000}`))
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1:8787/api/routes", strings.NewReader(`{"name":"demo","path":"/api","scheme":"http","port":4000}`))
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("POST path route: %d %s", w.Code, w.Body.String())
	}
	loaded, err := loadConfig(path)
	if err != nil || len(loaded.config.Routes) != 2 {
		t.Fatalf("reload: %v", err)
	}
	req = httptest.NewRequest("DELETE", "http://127.0.0.1:8787/api/routes/demo?path=%2Fapi", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("DELETE path route: %d %s", w.Code, w.Body.String())
	}
	loaded, err = loadConfig(path)
	if err != nil || len(loaded.config.Routes) != 1 || routePath(loaded.config.Routes[0]) != "/" {
		t.Fatalf("reload after DELETE: %v", err)
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:8787/api/state", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("Tunnel token leaked in API")
	}
}
