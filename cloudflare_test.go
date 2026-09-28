package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareSetupCreatesWildcardWithoutTouchingExistingNames(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const zone = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const tunnel = "11111111-2222-3333-4444-555555555555"
	const domain = "example.dev"
	var calls []string
	var appSocket string
	client := cfClient{token: "test", baseURL: "https://example.invalid", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		var result string
		switch r.Method + " " + r.URL.Path {
		case "GET /zones/" + zone + "/dns_records":
			if r.URL.Query().Get("name") != "*."+domain {
				t.Errorf("wrong DNS lookup: %s", r.URL.RawQuery)
			}
			result = `[]`
		case "POST /accounts/" + account + "/cfd_tunnel":
			result = `{"id":"` + tunnel + `"}`
		case "GET /accounts/" + account + "/cfd_tunnel/" + tunnel + "/token":
			result = `"secret-tunnel-token"`
		case "PUT /accounts/" + account + "/cfd_tunnel/" + tunnel + "/configurations":
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Config struct {
					Ingress []struct {
						Hostname      string `json:"hostname"`
						Service       string `json:"service"`
						OriginRequest struct {
							HTTPHostHeader string `json:"httpHostHeader"`
						} `json:"originRequest"`
					} `json:"ingress"`
				} `json:"config"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Config.Ingress) != 2 || payload.Config.Ingress[0].Hostname != "*."+domain || payload.Config.Ingress[1].Service != "http_status:404" {
				t.Errorf("wrong public ingress: %s", body)
			}
			if strings.Contains(string(body), "admin."+domain) {
				t.Errorf("admin must not be public: %s", body)
			}
			if !strings.Contains(string(body), `"hostname":"*.`+domain+`"`) {
				t.Errorf("missing wildcard ingress: %s", body)
			}
			if !strings.Contains(string(body), `"service":"unix:`+appSocket+`"`) {
				t.Errorf("wrong socket ingress: %s", body)
			}
			result = `{}`
		case "POST /zones/" + zone + "/dns_records":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"name":"*.`+domain+`"`) || !strings.Contains(string(body), `"proxied":true`) {
				t.Errorf("bad DNS payload: %s", body)
			}
			result = `{"id":"record-id"}`
		default:
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			result = `{}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"result":` + result + `}`))}, nil
	})}}
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.config.BaseDomain = domain
	app.adminAuth = testAdminAuth(t, app)
	app.adminAuth.Port = 9887
	appSocket = app.proxySocket
	if err := app.configureCloudflare(client, setupRequest{AccountID: account, ZoneID: zone}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 {
		t.Fatalf("got %d calls: %v", len(calls), calls)
	}
	if app.config.Cloudflare.TunnelID != tunnel || app.config.Cloudflare.DNSRecordID != "record-id" {
		t.Fatalf("config: %+v", app.config.Cloudflare)
	}
	if !app.config.Cloudflare.SocketIngress {
		t.Fatal("socket ingress was not persisted")
	}
	if app.config.Cloudflare.AdminHostname != "" || app.config.Cloudflare.AdminService != "" {
		t.Fatal("admin ingress must not be persisted")
	}
	data, err := io.ReadAll(strings.NewReader(app.config.Cloudflare.TunnelToken))
	if err != nil || string(data) != "secret-tunnel-token" {
		t.Fatal("token was not saved")
	}
}

func TestCloudflareSetupImportsEmptyTunnel(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const zone = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const tunnel = "11111111-2222-3333-4444-555555555555"
	var createdTunnel bool
	client := cfClient{token: "test", baseURL: "https://example.invalid", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var result string
		switch r.Method + " " + r.URL.Path {
		case "GET /accounts/" + account + "/cfd_tunnel/" + tunnel:
			result = `{"id":"` + tunnel + `","config_src":"cloudflare"}`
		case "GET /accounts/" + account + "/cfd_tunnel/" + tunnel + "/configurations":
			result = `{"config":{"ingress":[{"service":"http_status:404"}]}}`
		case "GET /zones/" + zone + "/dns_records":
			result = `[]`
		case "GET /accounts/" + account + "/cfd_tunnel/" + tunnel + "/token":
			result = `"secret-tunnel-token"`
		case "PUT /accounts/" + account + "/cfd_tunnel/" + tunnel + "/configurations":
			result = `{}`
		case "POST /zones/" + zone + "/dns_records":
			result = `{"id":"record-id"}`
		case "POST /accounts/" + account + "/cfd_tunnel":
			createdTunnel = true
			result = `{}`
		default:
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			result = `{}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"result":` + result + `}`))}, nil
	})}}
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	if err := app.configureCloudflare(client, setupRequest{AccountID: account, ZoneID: zone, ExistingTunnelID: tunnel}); err != nil {
		t.Fatal(err)
	}
	if createdTunnel || app.config.Cloudflare.TunnelID != tunnel {
		t.Fatal("existing tunnel was not reused")
	}
}

func TestCloudflareRejectsTunnelTokenInAPIField(t *testing.T) {
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	request := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/cloudflare/setup", strings.NewReader(`{"accountId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","zoneId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","apiToken":"eyJfake-tunnel-token"}`))
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	request.SetBasicAuth("admin", "long-test-password-123")
	response := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Tunnel token") {
		t.Fatalf("got %d %s", response.Code, response.Body.String())
	}
}
