package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTestConfig(path string) (*App, error) {
	app, err := loadConfig(path)
	if err == nil && app.config.BaseDomain == "" {
		app.config.BaseDomain = "ddman.cc"
	}
	return app, err
}

func TestConfigurableBaseDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	app, err := loadConfig(path)
	if err != nil || app.config.BaseDomain != "" {
		t.Fatalf("fresh config domain: %q, %v", app.config.BaseDomain, err)
	}
	app.config.BaseDomain = "example.org"
	if err := app.saveLocked(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil || loaded.config.BaseDomain != "example.org" {
		t.Fatalf("saved config domain: %q, %v", loaded.config.BaseDomain, err)
	}
	loaded.config.Routes = []Route{{Name: "app", Scheme: "http", Port: 3000}}
	loaded.proxyTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("matched"))}, nil
	})
	for _, tc := range []struct {
		host string
		want int
	}{{"app.example.org", 200}, {"app.ddman.cc", 404}} {
		w := httptest.NewRecorder()
		loaded.proxy(w, httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/", nil))
		if w.Code != tc.want {
			t.Errorf("host %s: got %d, want %d", tc.host, w.Code, tc.want)
		}
	}
	for _, domain := range []string{"", "example", "*.example.org", "example..org", "EXAMPLE.org"} {
		if validBaseDomain(domain) {
			t.Errorf("accepted invalid domain %q", domain)
		}
	}
}

func TestProxyUnixSocketAndRestart(t *testing.T) {
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.config.Routes = []Route{{Name: "app", Scheme: "http", Port: 3000}}
	app.proxyTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(r.Host + " " + r.URL.Path))}, nil
	})
	listener, err := listenProxySocket(app.proxySocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listenProxySocket(app.proxySocket); err == nil {
		t.Fatal("second listener replaced an active proxy socket")
	}
	server := &http.Server{Handler: http.HandlerFunc(app.proxy)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", app.proxySocket)
	}}}
	response, err := client.Get("http://app.ddman.cc/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != "app.ddman.cc /hello" {
		t.Fatalf("socket proxy: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(app.proxySocket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
	listener, err = listenProxySocket(app.proxySocket)
	if err != nil {
		t.Fatalf("restart socket: %v", err)
	}
	_ = listener.Close()
}

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
	app, err := loadTestConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app.config.Cloudflare.TunnelToken = "secret"
	app.adminAuth = testAdminAuth(t, app)
	h := app.adminHandler()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/routes", strings.NewReader(`{"name":"demo","scheme":"http","port":3000}`))
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.SetBasicAuth("admin", "long-test-password-123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1:8787/api/routes", strings.NewReader(`{"name":"demo","path":"/api","scheme":"http","port":4000}`))
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.SetBasicAuth("admin", "long-test-password-123")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("POST path route: %d %s", w.Code, w.Body.String())
	}
	loaded, err := loadTestConfig(path)
	if err != nil || len(loaded.config.Routes) != 2 {
		t.Fatalf("reload: %v", err)
	}
	req = httptest.NewRequest("DELETE", "http://127.0.0.1:8787/api/routes/demo?path=%2Fapi", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.SetBasicAuth("admin", "long-test-password-123")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("DELETE path route: %d %s", w.Code, w.Body.String())
	}
	loaded, err = loadTestConfig(path)
	if err != nil || len(loaded.config.Routes) != 1 || routePath(loaded.config.Routes[0]) != "/" {
		t.Fatalf("reload after DELETE: %v", err)
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1:8787/api/state", nil)
	req.SetBasicAuth("admin", "long-test-password-123")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("Tunnel token leaked in API")
	}
	var state struct {
		Bindings []struct {
			BaseDomain        string `json:"baseDomain"`
			WildcardHostname  string `json:"wildcardHostname"`
			TunnelTokenStored bool   `json:"tunnelTokenStored"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Bindings) != 1 || state.Bindings[0].BaseDomain != app.config.BaseDomain || state.Bindings[0].WildcardHostname != "*."+app.config.BaseDomain || !state.Bindings[0].TunnelTokenStored {
		t.Fatalf("unexpected bindings state: %+v", state.Bindings)
	}
}
