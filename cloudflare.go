package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var validID = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var validTunnelID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

type CloudflareConfig struct {
	AccountID   string `json:"accountId,omitempty"`
	ZoneID      string `json:"zoneId,omitempty"`
	TunnelID    string `json:"tunnelId,omitempty"`
	DNSRecordID string `json:"dnsRecordId,omitempty"`
	TunnelToken string `json:"tunnelToken,omitempty"`
}

func (c CloudflareConfig) public() map[string]string {
	return map[string]string{"accountId": c.AccountID, "zoneId": c.ZoneID, "tunnelId": c.TunnelID, "dnsRecordId": c.DNSRecordID}
}

type setupRequest struct {
	AccountID        string `json:"accountId"`
	ZoneID           string `json:"zoneId"`
	APIToken         string `json:"apiToken"`
	ExistingTunnelID string `json:"existingTunnelId"`
}

type cfResponse struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

type cfClient struct {
	token   string
	http    *http.Client
	baseURL string
}

func (c cfClient) request(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	baseURL := c.baseURL
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	var decoded cfResponse
	if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
		return fmt.Errorf("Cloudflare HTTP %d: invalid response: %w", resp.StatusCode, err)
	}
	if !decoded.Success || resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("Cloudflare 拒絕 API Token (401)：請確認填的是 My Profile → API Tokens 建立時顯示的 Token secret，不是 cloudflared 安裝指令中的 eyJ... Tunnel token；也請確認 Token 仍有效")
		}
		if resp.StatusCode == http.StatusForbidden {
			return errors.New("Cloudflare 拒絕權限 (403)：請確認 API Token 具備 Account / Cloudflare Tunnel / Edit 與 ddman.cc 的 Zone / DNS / Edit")
		}
		messages := make([]string, 0, len(decoded.Errors))
		for _, item := range decoded.Errors {
			messages = append(messages, fmt.Sprintf("%d: %s", item.Code, item.Message))
		}
		return fmt.Errorf("Cloudflare HTTP %d: %s", resp.StatusCode, strings.Join(messages, "; "))
	}
	if out != nil {
		return json.Unmarshal(decoded.Result, out)
	}
	return nil
}

func (a *App) setupCloudflare(w http.ResponseWriter, r *http.Request) {
	var input setupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !validID.MatchString(input.AccountID) || !validID.MatchString(input.ZoneID) || strings.TrimSpace(input.APIToken) == "" {
		http.Error(w, "enter valid Account ID, Zone ID, and API Token", http.StatusBadRequest)
		return
	}
	input.APIToken = strings.TrimSpace(input.APIToken)
	input.APIToken = strings.TrimPrefix(input.APIToken, "Bearer ")
	if strings.HasPrefix(input.APIToken, "eyJ") || strings.Contains(input.APIToken, "cloudflared") {
		http.Error(w, "這是 Tunnel token 或安裝指令；API Token 請從 Cloudflare 的 My Profile → API Tokens 建立並複製", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(input.APIToken, "cfk_") {
		http.Error(w, "這是 Global API Key，不是 API Token；請在 My Profile → API Tokens → Create Token 建立 Custom Token", http.StatusBadRequest)
		return
	}
	if input.ExistingTunnelID != "" && !validTunnelID.MatchString(input.ExistingTunnelID) {
		http.Error(w, "existing Tunnel ID must be a UUID", http.StatusBadRequest)
		return
	}
	if _, err := findCloudflared(); err != nil {
		http.Error(w, "install cloudflared before setup: brew install cloudflared", http.StatusBadRequest)
		return
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	current := a.config.Cloudflare
	a.mu.RUnlock()
	if current.TunnelID != "" && (current.AccountID != input.AccountID || current.ZoneID != input.ZoneID) {
		http.Error(w, "this app is already bound to another Cloudflare account or zone", http.StatusConflict)
		return
	}
	if current.TunnelID != "" && input.ExistingTunnelID != "" && current.TunnelID != input.ExistingTunnelID {
		http.Error(w, "this app is already bound to another Tunnel", http.StatusConflict)
		return
	}
	client := cfClient{token: input.APIToken, http: &http.Client{Timeout: 20 * time.Second}}
	if err := a.configureCloudflare(client, input); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := a.startTunnel(); err != nil {
		http.Error(w, "Cloudflare configured, but cloudflared could not start: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) configureCloudflare(client cfClient, input setupRequest) error {
	a.mu.RLock()
	current := a.config.Cloudflare
	a.mu.RUnlock()
	importing := current.TunnelID == "" && input.ExistingTunnelID != ""
	if importing {
		current.TunnelID = input.ExistingTunnelID
		current.AccountID = input.AccountID
		current.ZoneID = input.ZoneID
		var tunnel struct {
			ID        string `json:"id"`
			ConfigSrc string `json:"config_src"`
		}
		base := "/accounts/" + input.AccountID + "/cfd_tunnel/" + current.TunnelID
		if err := client.request(http.MethodGet, base, nil, &tunnel); err != nil {
			return fmt.Errorf("inspect existing Tunnel: %w", err)
		}
		if tunnel.ID != current.TunnelID || tunnel.ConfigSrc != "cloudflare" {
			return errors.New("existing Tunnel must be remotely managed and belong to this account")
		}
		var configuration struct {
			Config struct {
				Ingress []struct {
					Service string `json:"service"`
				} `json:"ingress"`
			} `json:"config"`
		}
		if err := client.request(http.MethodGet, base+"/configurations", nil, &configuration); err != nil {
			return fmt.Errorf("inspect existing Tunnel routes: %w", err)
		}
		for _, ingress := range configuration.Config.Ingress {
			if ingress.Service != "http_status:404" {
				return errors.New("existing Tunnel already has routes; refusing to replace them")
			}
		}
	}
	var records []struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		Proxied bool   `json:"proxied"`
	}
	if err := client.request(http.MethodGet, "/zones/"+input.ZoneID+"/dns_records?name=%2A.ddman.cc&per_page=100", nil, &records); err != nil {
		return fmt.Errorf("check wildcard DNS: %w", err)
	}
	for _, record := range records {
		if record.Name != "*.ddman.cc" {
			continue
		}
		if current.TunnelID == "" || record.Type != "CNAME" || record.Content != current.TunnelID+".cfargotunnel.com" || !record.Proxied {
			return fmt.Errorf("*.ddman.cc already has a DNS record; refusing to replace it")
		}
		current.DNSRecordID = record.ID
	}
	if importing {
		if err := a.persistCloudflare(current); err != nil {
			return err
		}
	}
	if current.TunnelID == "" {
		var tunnel struct {
			ID string `json:"id"`
		}
		body := map[string]string{"name": "ddman-home-tunnel", "config_src": "cloudflare"}
		if err := client.request(http.MethodPost, "/accounts/"+input.AccountID+"/cfd_tunnel", body, &tunnel); err != nil {
			return fmt.Errorf("create Tunnel: %w", err)
		}
		if tunnel.ID == "" {
			return errors.New("Cloudflare did not return a Tunnel ID")
		}
		current.TunnelID = tunnel.ID
		current.AccountID = input.AccountID
		current.ZoneID = input.ZoneID
		if err := a.persistCloudflare(current); err != nil {
			return err
		}
	}
	var token string
	if err := client.request(http.MethodGet, "/accounts/"+input.AccountID+"/cfd_tunnel/"+current.TunnelID+"/token", nil, &token); err != nil {
		return fmt.Errorf("retrieve Tunnel token: %w", err)
	}
	current.TunnelToken = token
	if err := a.persistCloudflare(current); err != nil {
		return err
	}
	config := map[string]any{"config": map[string]any{"ingress": []map[string]string{
		{"hostname": "*.ddman.cc", "service": "http://127.0.0.1:8788"},
		{"service": "http_status:404"},
	}}}
	if err := client.request(http.MethodPut, "/accounts/"+input.AccountID+"/cfd_tunnel/"+current.TunnelID+"/configurations", config, nil); err != nil {
		return fmt.Errorf("configure Tunnel ingress: %w", err)
	}
	if current.DNSRecordID == "" {
		var created struct {
			ID string `json:"id"`
		}
		body := map[string]any{"type": "CNAME", "name": "*.ddman.cc", "content": current.TunnelID + ".cfargotunnel.com", "proxied": true, "ttl": 1, "comment": "Managed by ddman-home-tunnel"}
		if err := client.request(http.MethodPost, "/zones/"+input.ZoneID+"/dns_records", body, &created); err != nil {
			return fmt.Errorf("create wildcard DNS: %w", err)
		}
		current.DNSRecordID = created.ID
		if err := a.persistCloudflare(current); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) persistCloudflare(value CloudflareConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.config.Cloudflare
	a.config.Cloudflare = value
	if err := a.saveLocked(); err != nil {
		a.config.Cloudflare = old
		return err
	}
	return nil
}

func (a *App) startTunnel() error {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if a.tunnelRunning {
		return nil
	}
	a.mu.RLock()
	token := a.config.Cloudflare.TunnelToken
	a.mu.RUnlock()
	if token == "" {
		return errors.New("Tunnel token is missing")
	}
	path, err := findCloudflared()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, "tunnel", "run")
	cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	a.tunnelCancel = cancel
	a.tunnelRunning = true
	go func() {
		if err := cmd.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "cloudflared exited: %v\n", err)
		}
		a.tunnelMu.Lock()
		a.tunnelRunning = false
		a.tunnelCancel = nil
		a.tunnelMu.Unlock()
	}()
	return nil
}

func findCloudflared() (string, error) {
	if path, err := exec.LookPath("cloudflared"); err == nil {
		return path, nil
	}
	local, err := filepath.Abs(filepath.Join("tools", "cloudflared"))
	if err != nil {
		return "", err
	}
	if stat, err := os.Stat(local); err == nil && !stat.IsDir() && stat.Mode()&0111 != 0 {
		return local, nil
	}
	return "", errors.New("cloudflared not found in PATH or tools/cloudflared")
}
