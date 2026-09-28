package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type AdminSettings struct {
	BaseDomain  string
	Host        string
	Port        int
	TailscaleIP string
	Username    string
	Password    string
	CFAccountID string
	CFZoneID    string
	CFAPIToken  string
}

type AdminAuth struct {
	Host        string
	Port        int
	TailscaleIP string
	username    string
	secret      [32]byte
}

func loadAdminSettings(path string) (AdminSettings, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return AdminSettings{}, fmt.Errorf("read admin .env: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) {
		return AdminSettings{}, fmt.Errorf("admin .env must be a regular file owned by this user with mode 0600: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return AdminSettings{}, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return AdminSettings{}, fmt.Errorf("invalid admin .env line: expected KEY=value")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return AdminSettings{}, err
	}
	port, err := strconv.Atoi(values["DDMAN_ADMIN_PORT"])
	if err != nil || port < 1 || port > 65535 || port == 8788 {
		return AdminSettings{}, fmt.Errorf("DDMAN_ADMIN_PORT must be 1-65535 and not 8788")
	}
	settings := AdminSettings{BaseDomain: strings.ToLower(values["DDMAN_BASE_DOMAIN"]), Host: strings.ToLower(values["DDMAN_ADMIN_HOST"]), Port: port, TailscaleIP: values["DDMAN_ADMIN_TAILSCALE_IP"], Username: values["DDMAN_ADMIN_USERNAME"], Password: values["DDMAN_ADMIN_PASSWORD"], CFAccountID: values["CF_ACCOUNT_ID"], CFZoneID: values["CF_ZONE_ID"], CFAPIToken: values["CF_API_TOKEN"]}
	if settings.BaseDomain != "" && !validBaseDomain(settings.BaseDomain) {
		return AdminSettings{}, fmt.Errorf("DDMAN_BASE_DOMAIN must be a valid DNS domain")
	}
	if validateAdminCredentials(settings.Username, settings.Password) != nil || settings.Host == "" {
		return AdminSettings{}, fmt.Errorf("admin .env needs DDMAN_ADMIN_HOST, DDMAN_ADMIN_USERNAME, and a password of at least 16 characters")
	}
	return settings, nil
}

func saveCFSettings(path, accountID, zoneID, token string) error {
	return saveEnvValues(path, map[string]string{"CF_ACCOUNT_ID": accountID, "CF_ZONE_ID": zoneID, "CF_API_TOKEN": token}, []string{"CF_ACCOUNT_ID", "CF_ZONE_ID", "CF_API_TOKEN"})
}

func saveAdminCredentials(path, username, password string) error {
	if err := validateAdminCredentials(username, password); err != nil {
		return err
	}
	return saveEnvValues(path, map[string]string{"DDMAN_ADMIN_USERNAME": username, "DDMAN_ADMIN_PASSWORD": password}, []string{"DDMAN_ADMIN_USERNAME", "DDMAN_ADMIN_PASSWORD"})
}

func validateAdminCredentials(username, password string) error {
	if len(username) < 3 || len(username) > 128 || strings.ContainsAny(username, " \t\r\n") || len(password) < 16 || len(password) > 1024 || strings.ContainsAny(password, "\r\n") || password != strings.TrimSpace(password) || (strings.HasPrefix(password, `"`) && strings.HasSuffix(password, `"`)) || (strings.HasPrefix(password, "'") && strings.HasSuffix(password, "'")) {
		return fmt.Errorf("admin username must be 3-128 characters without spaces; password must be 16-1024 characters without newlines, outer spaces, or enclosing quotes")
	}
	return nil
}

func validBaseDomain(domain string) bool {
	if len(domain) > 253 || domain != strings.ToLower(domain) {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !validLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func saveEnvValues(path string, values map[string]string, keys []string) error {
	if _, err := loadAdminSettings(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		key, _, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if found {
			if value, ok := values[key]; ok {
				lines = append(lines, key+"="+value)
				seen[key] = true
				continue
			}
		}
		lines = append(lines, line)
	}
	for _, key := range keys {
		if !seen[key] {
			lines = append(lines, key+"="+values[key])
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env-tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newAdminAuth(settings AdminSettings, baseDomain string, routes []Route) (*AdminAuth, error) {
	if settings.Port < 1 || settings.Port > 65535 || settings.Port == 8788 {
		return nil, fmt.Errorf("invalid admin port")
	}
	suffix := "." + baseDomain
	if !strings.HasSuffix(settings.Host, suffix) || !validLabel.MatchString(strings.TrimSuffix(settings.Host, suffix)) {
		return nil, fmt.Errorf("admin host must be one subdomain of %s", baseDomain)
	}
	if settings.TailscaleIP != "" {
		ip := net.ParseIP(settings.TailscaleIP).To4()
		if ip == nil || ip[0] != 100 || ip[1] < 64 || ip[1] > 127 {
			return nil, fmt.Errorf("DDMAN_ADMIN_TAILSCALE_IP must be a Tailscale IPv4 address in 100.64.0.0/10")
		}
	}
	name := strings.TrimSuffix(settings.Host, suffix)
	for _, route := range routes {
		if route.Name == name {
			return nil, fmt.Errorf("admin hostname %s is already used by a public route", settings.Host)
		}
		if route.Port == settings.Port {
			return nil, fmt.Errorf("admin port %d is already used by a public route", settings.Port)
		}
	}
	secret := sha256.Sum256([]byte(settings.Username + "\x00" + settings.Password))
	return &AdminAuth{Host: settings.Host, Port: settings.Port, TailscaleIP: settings.TailscaleIP, username: settings.Username, secret: secret}, nil
}

func (a *AdminAuth) address() string {
	return "127.0.0.1:" + strconv.Itoa(a.Port)
}

func (a *AdminAuth) tailscaleAddress() string {
	if a == nil || a.TailscaleIP == "" {
		return ""
	}
	return net.JoinHostPort(a.TailscaleIP, strconv.Itoa(a.Port))
}

func (a *AdminAuth) authorized(r *http.Request) bool {
	if a == nil || len(r.Header.Get("Authorization")) > 4096 {
		return false
	}
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return a.verifyCredentials(username, password)
}

func (a *AdminAuth) verifyCredentials(username, password string) bool {
	if a == nil || len(username) > 128 || len(password) > 1024 {
		return false
	}
	got := sha256.Sum256([]byte(username + "\x00" + password))
	return subtle.ConstantTimeCompare(got[:], a.secret[:]) == 1
}

func requireBasicAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Wildcard Tunnel Manager", charset="UTF-8"`)
	http.Error(w, "admin login required", http.StatusUnauthorized)
}

type basicAuthTransport struct {
	base     http.RoundTripper
	username string
	password string
}

func (t basicAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.SetBasicAuth(t.username, t.password)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copy)
}
