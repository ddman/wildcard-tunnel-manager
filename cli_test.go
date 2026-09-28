package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIUseSocketSendsTokenToRunningApp(t *testing.T) {
	var posted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/state":
			fmt.Fprint(w, `{"cloudflare":{"accountId":"account","zoneId":"zone","tunnelId":"tunnel"}}`)
		case "POST /api/cloudflare/setup":
			posted = true
			var input setupRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Errorf("decode setup request: %v", err)
			}
			if input.AccountID != "account" || input.ZoneID != "zone" || input.ExistingTunnelID != "tunnel" || input.APIToken != "secret-token" || r.Header.Get("Origin") != "http://"+r.Host {
				t.Errorf("wrong setup request: %+v", input)
			}
			fmt.Fprint(w, `{"ok":true}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	var output bytes.Buffer
	err := runCLI([]string{"tunnel", "use-socket", "--token-stdin"}, strings.NewReader("secret-token\n"), &output, server.Client(), server.URL)
	if err != nil || !posted || strings.Contains(output.String(), "secret-token") {
		t.Fatalf("CLI result: posted=%v output=%q error=%v", posted, output.String(), err)
	}
}
