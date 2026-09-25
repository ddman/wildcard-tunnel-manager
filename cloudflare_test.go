package main

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareSetupCreatesWildcardWithoutTouchingExistingNames(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const zone = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const tunnel = "11111111-2222-3333-4444-555555555555"
	var calls []string
	client := cfClient{token: "test", baseURL: "https://example.invalid", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		var result string
		switch r.Method + " " + r.URL.Path {
		case "GET /zones/" + zone + "/dns_records":
			result = `[]`
		case "POST /accounts/" + account + "/cfd_tunnel":
			result = `{"id":"` + tunnel + `"}`
		case "GET /accounts/" + account + "/cfd_tunnel/" + tunnel + "/token":
			result = `"secret-tunnel-token"`
		case "PUT /accounts/" + account + "/cfd_tunnel/" + tunnel + "/configurations":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"hostname":"*.ddman.cc"`) {
				t.Errorf("missing wildcard ingress: %s", body)
			}
			result = `{}`
		case "POST /zones/" + zone + "/dns_records":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"name":"*.ddman.cc"`) || !strings.Contains(string(body), `"proxied":true`) {
				t.Errorf("bad DNS payload: %s", body)
			}
			result = `{"id":"record-id"}`
		default:
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			result = `{}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"result":` + result + `}`))}, nil
	})}}
	app, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.configureCloudflare(client, setupRequest{AccountID: account, ZoneID: zone}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 {
		t.Fatalf("got %d calls: %v", len(calls), calls)
	}
	if app.config.Cloudflare.TunnelID != tunnel || app.config.Cloudflare.DNSRecordID != "record-id" {
		t.Fatalf("config: %+v", app.config.Cloudflare)
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
	app, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.configureCloudflare(client, setupRequest{AccountID: account, ZoneID: zone, ExistingTunnelID: tunnel}); err != nil {
		t.Fatal(err)
	}
	if createdTunnel || app.config.Cloudflare.TunnelID != tunnel {
		t.Fatal("existing tunnel was not reused")
	}
}
