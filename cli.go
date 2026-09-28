package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func runCLI(args []string, stdin io.Reader, stdout io.Writer, client *http.Client, adminURL string) error {
	if len(args) != 3 || args[0] != "tunnel" || args[1] != "use-socket" || args[2] != "--token-stdin" {
		return errors.New("usage: wildcard-tunnel-manager tunnel use-socket --token-stdin (pipe the Cloudflare API Token to stdin)")
	}
	response, err := client.Get(adminURL + "/api/state")
	if err != nil {
		return fmt.Errorf("connect to running local app: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("read local app state: HTTP %d", response.StatusCode)
	}
	var state struct {
		Cloudflare struct {
			AccountID string `json:"accountId"`
			ZoneID    string `json:"zoneId"`
			TunnelID  string `json:"tunnelId"`
		} `json:"cloudflare"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&state); err != nil {
		return fmt.Errorf("read local app state: %w", err)
	}
	if state.Cloudflare.TunnelID == "" {
		return errors.New("no Tunnel is configured in the running local app")
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(stdin, 4097))
	if err != nil {
		return fmt.Errorf("read API Token from stdin: %w", err)
	}
	if len(tokenBytes) > 4096 {
		return errors.New("API Token from stdin is too long")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("API Token from stdin is empty")
	}
	body, err := json.Marshal(setupRequest{AccountID: state.Cloudflare.AccountID, ZoneID: state.Cloudflare.ZoneID, ExistingTunnelID: state.Cloudflare.TunnelID, APIToken: token})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, adminURL+"/api/cloudflare/setup", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", adminURL)
	response, err = client.Do(request)
	if err != nil {
		return fmt.Errorf("update Tunnel through local app: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return fmt.Errorf("update Tunnel: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	_, _ = fmt.Fprintln(stdout, "Tunnel 已改用 Unix socket；舊代理入口將關閉。")
	return nil
}
