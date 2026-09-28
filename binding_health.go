package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type BindingHealth struct {
	Status        string `json:"status"`
	Message       string `json:"message"`
	OwnerTunnelID string `json:"ownerTunnelId,omitempty"`
	CheckedAt     string `json:"checkedAt,omitempty"`
}

func (a *App) bindingHealthSnapshot() BindingHealth {
	a.healthMu.RLock()
	defer a.healthMu.RUnlock()
	return a.bindingHealth
}

func (a *App) setBindingHealth(health BindingHealth) {
	health.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	a.healthMu.Lock()
	a.bindingHealth = health
	a.healthMu.Unlock()
}

func (a *App) refreshBindingHealth() {
	a.healthRefreshMu.Lock()
	defer a.healthRefreshMu.Unlock()
	a.mu.RLock()
	accountID, zoneID, token := a.cfAccountID, a.cfZoneID, a.cfAPIToken
	config := a.config
	a.mu.RUnlock()
	if token == "" || !validID.MatchString(accountID) || !validID.MatchString(zoneID) {
		a.setBindingHealth(BindingHealth{Status: "unverified", Message: "尚未保存 Cloudflare API Token；無法確認目前綁定。請在 Cloudflare 設定頁輸入憑證。"})
		return
	}
	client := cfClient{token: token, http: &http.Client{Timeout: 8 * time.Second}}
	a.setBindingHealth(checkBinding(client, accountID, zoneID, config.BaseDomain, config.Cloudflare.TunnelID, a.proxySocket))
}

func (a *App) monitorBindingHealth() {
	a.refreshBindingHealth()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.refreshBindingHealth()
	}
}

func checkBinding(client cfClient, accountID, zoneID, baseDomain, localTunnelID, proxySocket string) BindingHealth {
	hostname := "*." + baseDomain
	var records []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Content string `json:"content"`
		Proxied bool   `json:"proxied"`
	}
	if err := client.request(http.MethodGet, "/zones/"+zoneID+"/dns_records?name="+url.QueryEscape(hostname)+"&per_page=100", nil, &records); err != nil {
		return BindingHealth{Status: "error", Message: "Cloudflare DNS 檢查失敗：" + err.Error()}
	}
	for _, record := range records {
		if record.Name != hostname {
			continue
		}
		if record.Type != "CNAME" || !record.Proxied || !strings.HasSuffix(record.Content, ".cfargotunnel.com") {
			return BindingHealth{Status: "conflict", Message: hostname + " 已有非本工具 Tunnel 的 DNS 紀錄；不會自動覆蓋。"}
		}
		owner := strings.TrimSuffix(record.Content, ".cfargotunnel.com")
		if !validTunnelID.MatchString(owner) {
			return BindingHealth{Status: "conflict", Message: hostname + " 指向無法辨識的 Tunnel。"}
		}
		if owner != localTunnelID {
			var tunnel struct {
				Status string `json:"status"`
			}
			if err := client.request(http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+owner, nil, &tunnel); err != nil {
				return BindingHealth{Status: "conflict", OwnerTunnelID: owner, Message: fmt.Sprintf("%s 指向其他 Tunnel（%s）；無法確認對方連線狀態。", hostname, owner)}
			}
			return BindingHealth{Status: "conflict", OwnerTunnelID: owner, Message: fmt.Sprintf("%s 目前由其他 Tunnel（%s）綁定；對方狀態：%s。一般綁定不會覆蓋。", hostname, owner, tunnel.Status)}
		}
		var configuration struct {
			Config struct {
				Ingress []struct {
					Hostname string `json:"hostname"`
					Service  string `json:"service"`
				} `json:"ingress"`
			} `json:"config"`
		}
		if err := client.request(http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+owner+"/configurations", nil, &configuration); err != nil {
			return BindingHealth{Status: "error", OwnerTunnelID: owner, Message: "無法檢查 Tunnel 入口設定：" + err.Error()}
		}
		wildcardReady := false
		for _, ingress := range configuration.Config.Ingress {
			if ingress.Hostname == hostname && ingress.Service == "unix:"+proxySocket {
				wildcardReady = true
			}
		}
		if !wildcardReady {
			return BindingHealth{Status: "partial", OwnerTunnelID: owner, Message: "DNS 指向本機 Tunnel，但 Cloudflare ingress 尚未指向本機代理；請重新套用 Tunnel 設定。"}
		}
		var connectors []struct {
			ID string `json:"id"`
		}
		if err := client.request(http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+owner+"/connections", nil, &connectors); err != nil {
			return BindingHealth{Status: "error", OwnerTunnelID: owner, Message: "DNS 指向本機 Tunnel，但無法檢查 connector：" + err.Error()}
		}
		if len(connectors) > 1 {
			return BindingHealth{Status: "shared", OwnerTunnelID: owner, Message: fmt.Sprintf("同一 Tunnel 目前有 %d 個 connector；流量可能到不同主機。", len(connectors))}
		}
		if len(connectors) == 0 {
			return BindingHealth{Status: "disconnected", OwnerTunnelID: owner, Message: "DNS 指向本機 Tunnel，但 Cloudflare 尚未看到 connector 連線。"}
		}
		return BindingHealth{Status: "bound", OwnerTunnelID: owner, Message: hostname + " 已綁定到本機 Tunnel，connector 正在執行。"}
	}
	return BindingHealth{Status: "unbound", Message: hostname + " 尚未建立 DNS 綁定。"}
}
