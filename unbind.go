package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

func (a *App) unbindCloudflare(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	accountID, zoneID, token := a.cfAccountID, a.cfZoneID, a.cfAPIToken
	localID, domain := a.config.Cloudflare.TunnelID, a.config.BaseDomain
	a.mu.RUnlock()
	if token == "" || !validID.MatchString(accountID) || !validID.MatchString(zoneID) || localID == "" {
		http.Error(w, "Cloudflare credentials or local Tunnel are missing", http.StatusConflict)
		return
	}
	client := cfClient{token: token, http: &http.Client{Timeout: 8 * time.Second}}
	if err := removeOwnedDNS(client, zoneID, localID, domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	a.refreshBindingHealth()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func removeOwnedDNS(client cfClient, zoneID, localID, domain string) error {
	for _, name := range []string{"*." + domain} {
		var records []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := client.request(http.MethodGet, "/zones/"+zoneID+"/dns_records?name="+url.QueryEscape(name)+"&per_page=100", nil, &records); err != nil {
			return fmt.Errorf("check %s DNS: %w", name, err)
		}
		for _, record := range records {
			if record.Name != name || record.Type != "CNAME" || record.Content != localID+".cfargotunnel.com" {
				continue
			}
			if err := client.request(http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+record.ID, nil, nil); err != nil {
				return fmt.Errorf("remove %s DNS: %w", name, err)
			}
		}
	}
	return nil
}
