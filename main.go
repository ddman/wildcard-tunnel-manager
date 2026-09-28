package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const legacyProxyAddress = "127.0.0.1:8788"

var validLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var validPathPrefix = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+(?:/[A-Za-z0-9._~-]+)*)?$`)

type Route struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	StripPrefix bool   `json:"stripPrefix,omitempty"`
	Scheme      string `json:"scheme"`
	Port        int    `json:"port"`
}

type Config struct {
	BaseDomain string           `json:"baseDomain"`
	Routes     []Route          `json:"routes"`
	Cloudflare CloudflareConfig `json:"cloudflare"`
}

type App struct {
	mu              sync.RWMutex
	setupMu         sync.Mutex
	tunnelMu        sync.Mutex
	tunnelCancel    func()
	tunnelRunning   bool
	legacyProxy     *http.Server
	config          Config
	path            string
	proxySocket     string
	proxyTransport  http.RoundTripper
	adminMu         sync.RWMutex
	adminAuth       *AdminAuth
	sessionMu       sync.Mutex
	sessions        map[string]time.Time
	healthMu        sync.RWMutex
	healthRefreshMu sync.Mutex
	bindingHealth   BindingHealth
	cfAccountID     string
	cfZoneID        string
	cfAPIToken      string
}

func (a *App) currentAdminAuth() *AdminAuth {
	a.adminMu.RLock()
	defer a.adminMu.RUnlock()
	return a.adminAuth
}

func main() {
	path := os.Getenv("DDMAN_CONFIG")
	if path == "" {
		path = "config.json"
	}
	app, err := loadConfig(path)
	if err != nil {
		log.Fatal(err)
	}
	settings, err := loadAdminSettings(filepath.Join(filepath.Dir(app.path), ".env"))
	if err != nil {
		log.Fatal(err)
	}
	if settings.BaseDomain == "" {
		settings.BaseDomain = app.config.BaseDomain // Existing installations predate DDMAN_BASE_DOMAIN.
	}
	if settings.BaseDomain == "" {
		log.Fatal("set DDMAN_BASE_DOMAIN in .env")
	}
	if app.config.BaseDomain == "" {
		app.config.BaseDomain = settings.BaseDomain
	} else if app.config.BaseDomain != settings.BaseDomain {
		log.Fatalf("DDMAN_BASE_DOMAIN %q does not match saved config domain %q", settings.BaseDomain, app.config.BaseDomain)
	}
	app.adminAuth, err = newAdminAuth(settings, app.config.BaseDomain, app.config.Routes)
	if err != nil {
		log.Fatal(err)
	}
	app.cfAccountID, app.cfZoneID, app.cfAPIToken = settings.CFAccountID, settings.CFZoneID, settings.CFAPIToken
	if app.cfAccountID == "" {
		app.cfAccountID = app.config.Cloudflare.AccountID
	}
	if app.cfZoneID == "" {
		app.cfZoneID = app.config.Cloudflare.ZoneID
	}
	app.setBindingHealth(BindingHealth{Status: "checking", Message: "正在檢查 Cloudflare 綁定。"})
	if len(os.Args) > 1 {
		client := &http.Client{Timeout: 2 * time.Minute, Transport: basicAuthTransport{username: settings.Username, password: settings.Password}}
		if err := runCLI(os.Args[1:], os.Stdin, os.Stdout, client, "http://"+app.adminAuth.address()); err != nil {
			log.Fatal(err)
		}
		return
	}
	listener, err := listenProxySocket(app.proxySocket)
	if err != nil {
		log.Fatal(err)
	}
	if app.config.Cloudflare.TunnelID != "" && !app.config.Cloudflare.SocketIngress {
		legacyListener, err := net.Listen("tcp", legacyProxyAddress)
		if err != nil {
			log.Fatal(err)
		}
		app.legacyProxy = &http.Server{Handler: http.HandlerFunc(app.proxy), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Printf("legacy proxy listening on http://%s until Tunnel migration", legacyProxyAddress)
			if err := app.legacyProxy.Serve(legacyListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}()
	}
	if app.config.Cloudflare.TunnelToken != "" {
		if err := app.startTunnel(); err != nil {
			log.Printf("cloudflared not started: %v", err)
		}
	}
	go app.monitorBindingHealth()
	proxy := &http.Server{Handler: http.HandlerFunc(app.proxy), ReadHeaderTimeout: 10 * time.Second}
	admin := &http.Server{Addr: app.adminAuth.address(), Handler: app.adminHandler(), ReadHeaderTimeout: 10 * time.Second}
	var tailAdmin *http.Server
	if address := app.adminAuth.tailscaleAddress(); address != "" {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			log.Printf("Tailscale admin listener unavailable on %s: %v", address, err)
		} else {
			tailAdmin = &http.Server{Handler: app.adminHandler(), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				log.Printf("admin UI on Tailscale: http://%s", address)
				if err := tailAdmin.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("Tailscale admin listener stopped: %v", err)
				}
			}()
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		app.tunnelMu.Lock()
		if app.tunnelCancel != nil {
			app.tunnelCancel()
		}
		app.tunnelMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = admin.Shutdown(ctx)
		if tailAdmin != nil {
			_ = tailAdmin.Shutdown(ctx)
		}
		_ = proxy.Shutdown(ctx)
		if app.legacyProxy != nil {
			_ = app.legacyProxy.Shutdown(ctx)
		}
	}()
	go func() {
		log.Printf("proxy listening on unix:%s", app.proxySocket)
		if err := proxy.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("admin UI: http://%s", app.adminAuth.address())
	if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadConfig(path string) (*App, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	socketHash := sha256.Sum256([]byte(absPath))
	socketDir := filepath.Join("/tmp", fmt.Sprintf("ddman-home-tunnel-%d", os.Getuid()))
	app := &App{path: absPath, proxySocket: filepath.Join(socketDir, fmt.Sprintf("%x.sock", socketHash[:8])), config: Config{Routes: []Route{}}}
	data, err := os.ReadFile(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return app, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &app.config); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if !validBaseDomain(app.config.BaseDomain) {
		return nil, fmt.Errorf("invalid saved base domain %q", app.config.BaseDomain)
	}
	for _, route := range app.config.Routes {
		if err := validateRoute(route); err != nil {
			return nil, fmt.Errorf("invalid saved route: %w", err)
		}
	}
	return app, nil
}

func listenProxySocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("proxy socket directory is not private to this user: %s", dir)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("proxy socket path is occupied by a non-socket file: %s", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("proxy socket is already in use: %s", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
			return nil, fmt.Errorf("check proxy socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func validateRoute(route Route) error {
	if !validLabel.MatchString(route.Name) {
		return fmt.Errorf("name must be a single lowercase DNS label")
	}
	if route.Scheme != "http" && route.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if route.Port < 1 || route.Port > 65535 || route.Port == 8788 {
		return fmt.Errorf("port must be 1-65535 and not the legacy proxy port")
	}
	path := routePath(route)
	if !validPathPrefix.MatchString(path) {
		return fmt.Errorf("path must be / or a path prefix such as /api")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("path cannot contain . or .. segments")
		}
	}
	return nil
}

func routePath(route Route) string {
	if route.Path == "" {
		return "/"
	}
	return route.Path
}

func pathMatchesPrefix(path, prefix string) bool {
	return prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (a *App) saveLocked() error {
	data, err := json.MarshalIndent(a.config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *App) proxy(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	suffix := "." + a.config.BaseDomain
	if !strings.HasSuffix(host, suffix) {
		http.Error(w, "unknown hostname", http.StatusNotFound)
		return
	}
	name := strings.TrimSuffix(host, suffix)
	if !validLabel.MatchString(name) {
		http.Error(w, "unknown hostname", http.StatusNotFound)
		return
	}
	a.mu.RLock()
	var match *Route
	longest := -1
	for _, route := range a.config.Routes {
		prefix := routePath(route)
		if route.Name == name && pathMatchesPrefix(r.URL.Path, prefix) && len(prefix) > longest {
			copy := route
			match = &copy
			longest = len(prefix)
		}
	}
	a.mu.RUnlock()
	if match == nil {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	target := &url.URL{Scheme: match.Scheme, Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(match.Port))}
	p := httputil.NewSingleHostReverseProxy(target)
	if a.proxyTransport != nil {
		p.Transport = a.proxyTransport
	}
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy %s: %v", name, err)
		http.Error(w, "local service unavailable", http.StatusBadGateway)
	}
	if match.StripPrefix && routePath(*match) != "/" {
		r = r.Clone(r.Context())
		r.URL.Path = strings.TrimPrefix(r.URL.Path, routePath(*match))
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		r.URL.RawPath = ""
	}
	p.ServeHTTP(w, r)
}

func (a *App) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		adminAuth := a.currentAdminAuth()
		a.mu.RLock()
		config := a.config
		config.Routes = append([]Route{}, a.config.Routes...)
		tokenStored := a.cfAPIToken != ""
		cfAccountID, cfZoneID := a.cfAccountID, a.cfZoneID
		a.mu.RUnlock()
		a.tunnelMu.Lock()
		running := a.tunnelRunning
		a.tunnelMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"bindingHealth":            a.bindingHealthSnapshot(),
			"cloudflareAPITokenStored": tokenStored,
			"cloudflareAccountID":      cfAccountID,
			"cloudflareZoneID":         cfZoneID,
			"adminHostname":            adminAuth.Host,
			"adminPort":                adminAuth.Port,
			"adminUsername":            adminAuth.username,
			"adminTailscaleAddress":    adminAuth.tailscaleAddress(),
			"baseDomain":               config.BaseDomain,
			"bindings": []map[string]any{{
				"id":                   "primary",
				"baseDomain":           config.BaseDomain,
				"wildcardHostname":     "*." + config.BaseDomain,
				"cloudflare":           config.Cloudflare.public(),
				"tunnelRunning":        running,
				"tunnelTokenStored":    config.Cloudflare.TunnelToken != "",
				"needsSocketMigration": config.Cloudflare.TunnelID != "" && !config.Cloudflare.SocketIngress,
			}},
			"routes":               config.Routes,
			"proxySocket":          a.proxySocket,
			"cloudflare":           config.Cloudflare.public(),
			"tunnelRunning":        running,
			"needsSocketMigration": config.Cloudflare.TunnelID != "" && !config.Cloudflare.SocketIngress,
		})
	})
	mux.HandleFunc("GET /api/cloudflare/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.bindingHealthSnapshot())
	})
	mux.HandleFunc("POST /api/admin/credentials", a.updateAdminCredentials)
	mux.HandleFunc("POST /api/cloudflare/setup", a.setupCloudflare)
	mux.HandleFunc("POST /api/cloudflare/unbind", a.unbindCloudflare)
	mux.HandleFunc("POST /api/routes", func(w http.ResponseWriter, r *http.Request) {
		var route Route
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&route); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := validateRoute(route); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if route.Path == "" {
			route.Path = "/"
		}
		adminAuth := a.currentAdminAuth()
		if route.Name+"."+a.config.BaseDomain == adminAuth.Host {
			http.Error(w, "hostname is reserved for admin", http.StatusConflict)
			return
		}
		if route.Port == adminAuth.Port {
			http.Error(w, "port is reserved for admin", http.StatusConflict)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, existing := range a.config.Routes {
			if existing.Name == route.Name && routePath(existing) == route.Path {
				http.Error(w, "hostname and path already exist", http.StatusConflict)
				return
			}
		}
		a.config.Routes = append(a.config.Routes, route)
		if err := a.saveLocked(); err != nil {
			a.config.Routes = a.config.Routes[:len(a.config.Routes)-1]
			http.Error(w, "could not save route", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, route)
	})
	mux.HandleFunc("DELETE /api/routes/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/"
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, route := range a.config.Routes {
			if route.Name == name && routePath(route) == path {
				old := a.config.Routes
				a.config.Routes = append(append([]Route{}, old[:i]...), old[i+1:]...)
				if err := a.saveLocked(); err != nil {
					a.config.Routes = old
					http.Error(w, "could not save route", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		adminAuth := a.currentAdminAuth()
		if adminAuth == nil {
			http.Error(w, "admin login is not configured", http.StatusServiceUnavailable)
			return
		}
		allowedOrigin := "http://" + r.Host
		if r.Host != adminAuth.address() && r.Host != adminAuth.tailscaleAddress() {
			http.Error(w, "host denied", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != allowedOrigin {
			http.Error(w, "origin denied", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") == "" {
			http.Error(w, "origin required", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/login" && r.Method == http.MethodGet {
			if a.hasAdminSession(r) {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			serveLoginHTML(w, http.StatusOK, false)
			return
		}
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			a.loginAdmin(w, r, adminAuth)
			return
		}
		if r.URL.Path == "/logout" && r.Method == http.MethodPost {
			a.logoutAdmin(w, r)
			return
		}
		if !a.hasAdminSession(r) && !(strings.HasPrefix(r.URL.Path, "/api/") && adminAuth.authorized(r)) {
			if r.URL.Path == "/" && r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
