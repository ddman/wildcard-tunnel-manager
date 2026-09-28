package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTwoIsolatedInstancesTakeTurnsBindingWildcard(t *testing.T) {
	const account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const zone = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ids := []string{"11111111-2222-3333-4444-555555555555", "66666666-7777-8888-9999-aaaaaaaaaaaa"}
	created := 0
	wildcardTarget := ""
	configurations := make(map[string]json.RawMessage)
	client := cfClient{token: "test", baseURL: "https://example.invalid", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var result any = map[string]any{}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			if r.URL.Query().Get("name") == "*.ddman.cc" && wildcardTarget != "" {
				result = []map[string]any{{"id": "wildcard-record", "name": "*.ddman.cc", "type": "CNAME", "proxied": true, "content": wildcardTarget}}
			} else {
				result = []any{}
			}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/cfd_tunnel"):
			if created >= len(ids) {
				t.Fatal("created too many tunnels")
			}
			result = map[string]any{"id": ids[created]}
			created++
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/token"):
			result = "fake-tunnel-token"
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/configurations"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Config json.RawMessage `json:"config"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(r.URL.Path, "/")
			configurations[parts[4]] = payload.Config
			result = map[string]any{}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/configurations"):
			parts := strings.Split(r.URL.Path, "/")
			result = map[string]any{"config": configurations[parts[4]]}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			wildcardTarget = body.Content
			result = map[string]any{"id": "wildcard-record"}
		case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/dns_records/wildcard-record"):
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			wildcardTarget = body.Content
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/dns_records/wildcard-record"):
			wildcardTarget = ""
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/connections"):
			result = []map[string]string{{"id": "one-connector"}}
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/cfd_tunnel/"):
			result = map[string]string{"status": "healthy"}
		default:
			t.Errorf("unexpected Cloudflare call: %s %s", r.Method, r.URL.Path)
		}
		encoded, err := json.Marshal(map[string]any{"success": true, "result": result})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})}}
	newInstance := func(port string) *App {
		dir := t.TempDir()
		env := "DDMAN_ADMIN_HOST=admin.ddman.cc\nDDMAN_ADMIN_PORT=" + port + "\nDDMAN_ADMIN_USERNAME=admin\nDDMAN_ADMIN_PASSWORD=long-test-password-123\nCF_ACCOUNT_ID=" + account + "\nCF_ZONE_ID=" + zone + "\nCF_API_TOKEN=test\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0600); err != nil {
			t.Fatal(err)
		}
		settings, err := loadAdminSettings(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		app, err := loadTestConfig(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		app.adminAuth, err = newAdminAuth(settings, app.config.BaseDomain, app.config.Routes)
		if err != nil {
			t.Fatal(err)
		}
		app.cfAccountID, app.cfZoneID, app.cfAPIToken = settings.CFAccountID, settings.CFZoneID, settings.CFAPIToken
		return app
	}
	home, office := newInstance("8787"), newInstance("8789")
	if home.cfAccountID != office.cfAccountID || home.cfZoneID != office.cfZoneID || home.cfAPIToken != office.cfAPIToken || home.adminAuth.Port == office.adminAuth.Port {
		t.Fatal("test instances must share Cloudflare settings and have distinct local ports")
	}
	input := setupRequest{AccountID: home.cfAccountID, ZoneID: home.cfZoneID, APIToken: home.cfAPIToken}
	if err := home.configureCloudflare(client, input); err != nil {
		t.Fatal(err)
	}
	if got := checkBinding(client, account, zone, "ddman.cc", home.config.Cloudflare.TunnelID, home.proxySocket); got.Status != "bound" {
		t.Fatalf("home first: %+v", got)
	}
	var conflict *BindingConflictError
	if err := office.configureCloudflare(client, input); !errors.As(err, &conflict) {
		t.Fatalf("office expected conflict, got %v", err)
	}
	if office.config.Cloudflare.TunnelID != "" {
		t.Fatal("ordinary bind created an office Tunnel before conflict resolution")
	}
	input.Force = true
	if err := office.configureCloudflare(client, input); err != nil {
		t.Fatal(err)
	}
	if got := checkBinding(client, account, zone, "ddman.cc", office.config.Cloudflare.TunnelID, office.proxySocket); got.Status != "bound" {
		t.Fatalf("office after takeover: %+v", got)
	}
	if got := checkBinding(client, account, zone, "ddman.cc", home.config.Cloudflare.TunnelID, home.proxySocket); got.Status != "conflict" {
		t.Fatalf("home after takeover: %+v", got)
	}
	input.Force = false
	if err := home.configureCloudflare(client, input); !errors.As(err, &conflict) {
		t.Fatalf("home expected conflict, got %v", err)
	}
	input.Force = true
	if err := home.configureCloudflare(client, input); err != nil {
		t.Fatal(err)
	}
	if got := checkBinding(client, account, zone, "ddman.cc", home.config.Cloudflare.TunnelID, home.proxySocket); got.Status != "bound" {
		t.Fatalf("home after reclaim: %+v", got)
	}
	if got := checkBinding(client, account, zone, "ddman.cc", office.config.Cloudflare.TunnelID, office.proxySocket); got.Status != "conflict" {
		t.Fatalf("office after reclaim: %+v", got)
	}
	if created != 2 {
		t.Fatalf("want two independent Tunnels, got %d", created)
	}
	if err := removeOwnedDNS(client, zone, office.config.Cloudflare.TunnelID, "ddman.cc"); err != nil {
		t.Fatal(err)
	}
	if wildcardTarget != home.config.Cloudflare.TunnelID+".cfargotunnel.com" {
		t.Fatal("old office instance removed the new home binding")
	}
	if err := removeOwnedDNS(client, zone, home.config.Cloudflare.TunnelID, "ddman.cc"); err != nil {
		t.Fatal(err)
	}
	if wildcardTarget != "" {
		t.Fatal("home did not release its own binding")
	}
}
