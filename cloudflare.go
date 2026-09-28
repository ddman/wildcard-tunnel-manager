package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
	AccountID     string `json:"accountId,omitempty"`
	ZoneID        string `json:"zoneId,omitempty"`
	TunnelID      string `json:"tunnelId,omitempty"`
	DNSRecordID   string `json:"dnsRecordId,omitempty"`
	TunnelToken   string `json:"tunnelToken,omitempty"`
	SocketIngress bool   `json:"socketIngress,omitempty"`
	AdminHostname string `json:"adminHostname,omitempty"`
	AdminService  string `json:"adminService,omitempty"`
}

func (c CloudflareConfig) public() map[string]string {
	return map[string]string{"accountId": c.AccountID, "zoneId": c.ZoneID, "tunnelId": c.TunnelID, "dnsRecordId": c.DNSRecordID, "adminHostname": c.AdminHostname, "adminService": c.AdminService}
}

type setupRequest struct {
	AccountID        string `json:"accountId"`
	ZoneID           string `json:"zoneId"`
	APIToken         string `json:"apiToken"`
	ExistingTunnelID string `json:"existingTunnelId"`
	Force            bool   `json:"force"`
}

type BindingConflictError struct{ Hostname, OwnerTunnelID string }

func (e *BindingConflictError) Error() string {
	return fmt.Sprintf("%s 已由另一條 Tunnel（%s）綁定；如要接管，請確認後使用強制綁定", e.Hostname, e.OwnerTunnelID)
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
			if strings.Contains(path, "/dns_records") {
				return errors.New("Cloudflare 拒絕 DNS 紀錄權限 (403)：請在 API Token 加入目前網域的 Zone → DNS → Edit（DNS Write），不是 Zone → DNS Settings → Edit（DNS 設定：編輯）；並確認 Zone ID 正確")
			}
			return errors.New("Cloudflare 拒絕 Tunnel 權限 (403)：請確認 API Token 具備 Account → Cloudflare Tunnel → Edit，且 Account ID 正確")
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
	a.mu.RLock()
	if input.AccountID == "" {
		input.AccountID = a.cfAccountID
	}
	if input.ZoneID == "" {
		input.ZoneID = a.cfZoneID
	}
	if input.APIToken == "" {
		input.APIToken = a.cfAPIToken
	}
	a.mu.RUnlock()
	if !validID.MatchString(input.AccountID) || !validID.MatchString(input.ZoneID) || strings.TrimSpace(input.APIToken) == "" {
		http.Error(w, "enter valid Account ID, Zone ID, and API Token", http.StatusBadRequest)
		return
	}
	input.APIToken = strings.TrimSpace(input.APIToken)
	input.APIToken = strings.TrimPrefix(input.APIToken, "Bearer ")
	if len(input.APIToken) > 4096 || strings.ContainsAny(input.APIToken, "\r\n") {
		http.Error(w, "invalid API Token", http.StatusBadRequest)
		return
	}
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
	if err := saveCFSettings(filepath.Join(filepath.Dir(a.path), ".env"), input.AccountID, input.ZoneID, input.APIToken); err != nil {
		http.Error(w, "could not save Cloudflare settings", http.StatusInternalServerError)
		return
	}
	a.mu.Lock()
	a.cfAccountID, a.cfZoneID, a.cfAPIToken = input.AccountID, input.ZoneID, input.APIToken
	a.mu.Unlock()
	log.Printf("Cloudflare setup request started")
	client := cfClient{token: input.APIToken, http: &http.Client{Timeout: 20 * time.Second}}
	if err := a.configureCloudflare(client, input); err != nil {
		log.Printf("Cloudflare setup request failed")
		a.refreshBindingHealth()
		var conflict *BindingConflictError
		if errors.As(err, &conflict) {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return
	}
	if err := a.startTunnel(); err != nil {
		http.Error(w, "Cloudflare configured, but cloudflared could not start: "+err.Error(), http.StatusBadGateway)
		return
	}
	if a.legacyProxy != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.legacyProxy.Shutdown(ctx); err != nil {
				log.Printf("stop legacy proxy: %v", err)
			}
		}()
	}
	log.Printf("Cloudflare setup updated socket ingress")
	a.refreshBindingHealth()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) configureCloudflare(client cfClient, input setupRequest) error {
	if a.adminAuth == nil {
		return errors.New("admin login is not configured")
	}
	a.mu.RLock()
	current := a.config.Cloudflare
	wildcardHostname := "*." + a.config.BaseDomain
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
	var wildcardNeedsPatch bool
	current.DNSRecordID = ""
	if err := client.request(http.MethodGet, "/zones/"+input.ZoneID+"/dns_records?name="+url.QueryEscape(wildcardHostname)+"&per_page=100", nil, &records); err != nil {
		return fmt.Errorf("check wildcard DNS: %w", err)
	}
	for _, record := range records {
		if record.Name != wildcardHostname {
			continue
		}
		if record.Type != "CNAME" || !record.Proxied || !validTunnelDNSContent(record.Content) {
			return fmt.Errorf("%s already has a non-Tunnel DNS record; refusing to replace it", wildcardHostname)
		}
		if record.Content != current.TunnelID+".cfargotunnel.com" {
			if !input.Force {
				return &BindingConflictError{Hostname: wildcardHostname, OwnerTunnelID: strings.TrimSuffix(record.Content, ".cfargotunnel.com")}
			}
			wildcardNeedsPatch = true
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
		idSuffix := make([]byte, 6)
		if _, err := rand.Read(idSuffix); err != nil {
			return err
		}
		body := map[string]string{"name": fmt.Sprintf("wildcard-tunnel-manager-%x", idSuffix), "config_src": "cloudflare"}
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
	config := tunnelIngressConfig(wildcardHostname, a.proxySocket)
	if err := client.request(http.MethodPut, "/accounts/"+input.AccountID+"/cfd_tunnel/"+current.TunnelID+"/configurations", config, nil); err != nil {
		return fmt.Errorf("configure Tunnel ingress: %w", err)
	}
	current.SocketIngress = true
	current.AdminHostname = ""
	current.AdminService = ""
	if err := a.persistCloudflare(current); err != nil {
		return err
	}
	if current.DNSRecordID == "" {
		var created struct {
			ID string `json:"id"`
		}
		body := map[string]any{"type": "CNAME", "name": wildcardHostname, "content": current.TunnelID + ".cfargotunnel.com", "proxied": true, "ttl": 1, "comment": "Managed by Wildcard Tunnel Manager"}
		if err := client.request(http.MethodPost, "/zones/"+input.ZoneID+"/dns_records", body, &created); err != nil {
			return fmt.Errorf("create wildcard DNS: %w", err)
		}
		current.DNSRecordID = created.ID
		if err := a.persistCloudflare(current); err != nil {
			return err
		}
	} else if wildcardNeedsPatch {
		if err := client.request(http.MethodPatch, "/zones/"+input.ZoneID+"/dns_records/"+current.DNSRecordID, map[string]any{"content": current.TunnelID + ".cfargotunnel.com", "proxied": true}, nil); err != nil {
			return fmt.Errorf("transfer wildcard DNS: %w", err)
		}
	}
	if input.Force && wildcardNeedsPatch {
		var verify []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := client.request(http.MethodGet, "/zones/"+input.ZoneID+"/dns_records?name="+url.QueryEscape(wildcardHostname)+"&per_page=100", nil, &verify); err != nil {
			return fmt.Errorf("verify transferred DNS: %w", err)
		}
		matched := false
		for _, record := range verify {
			if record.Name == wildcardHostname && record.Content == current.TunnelID+".cfargotunnel.com" {
				matched = true
			}
		}
		if !matched {
			return errors.New("Cloudflare DNS transfer could not be verified; another host may have changed it")
		}
	}
	return nil
}

func validTunnelDNSContent(content string) bool {
	if !strings.HasSuffix(content, ".cfargotunnel.com") {
		return false
	}
	return validTunnelID.MatchString(strings.TrimSuffix(content, ".cfargotunnel.com"))
}

func tunnelIngressConfig(wildcardHostname, socket string) map[string]any {
	return map[string]any{"config": map[string]any{"ingress": []map[string]any{
		{"hostname": wildcardHostname, "service": "unix:" + socket},
		{"service": "http_status:404"},
	}}}
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
