package main

import (
	"context"
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

const (
	adminAddress = "127.0.0.1:8787"
	proxyAddress = "127.0.0.1:8788"
)

var validLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Route struct {
	Name   string `json:"name"`
	Scheme string `json:"scheme"`
	Port   int    `json:"port"`
}

type Config struct {
	BaseDomain string           `json:"baseDomain"`
	Routes     []Route          `json:"routes"`
	Cloudflare CloudflareConfig `json:"cloudflare"`
}

type App struct {
	mu             sync.RWMutex
	setupMu        sync.Mutex
	tunnelMu       sync.Mutex
	tunnelCancel   func()
	tunnelRunning  bool
	config         Config
	path           string
	proxyTransport http.RoundTripper
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
	if app.config.Cloudflare.TunnelToken != "" {
		if err := app.startTunnel(); err != nil {
			log.Printf("cloudflared not started: %v", err)
		}
	}
	proxy := &http.Server{Addr: proxyAddress, Handler: http.HandlerFunc(app.proxy), ReadHeaderTimeout: 10 * time.Second}
	admin := &http.Server{Addr: adminAddress, Handler: app.adminHandler(), ReadHeaderTimeout: 10 * time.Second}
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
		_ = proxy.Shutdown(ctx)
	}()
	go func() {
		log.Printf("proxy listening on http://%s", proxyAddress)
		if err := proxy.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("admin UI: http://%s", adminAddress)
	if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadConfig(path string) (*App, error) {
	app := &App{path: path, config: Config{BaseDomain: "ddman.cc", Routes: []Route{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return app, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &app.config); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if app.config.BaseDomain != "ddman.cc" {
		return nil, fmt.Errorf("unsupported base domain %q", app.config.BaseDomain)
	}
	for _, route := range app.config.Routes {
		if err := validateRoute(route); err != nil {
			return nil, fmt.Errorf("invalid saved route: %w", err)
		}
	}
	return app, nil
}

func validateRoute(route Route) error {
	if !validLabel.MatchString(route.Name) {
		return fmt.Errorf("name must be a single lowercase DNS label")
	}
	if route.Scheme != "http" && route.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if route.Port < 1 || route.Port > 65535 || route.Port == 8787 || route.Port == 8788 {
		return fmt.Errorf("port must be 1-65535 and not an app port")
	}
	return nil
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
	for _, route := range a.config.Routes {
		if route.Name == name {
			copy := route
			match = &copy
			break
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
		a.mu.RLock()
		config := a.config
		config.Routes = append([]Route{}, a.config.Routes...)
		a.mu.RUnlock()
		a.tunnelMu.Lock()
		running := a.tunnelRunning
		a.tunnelMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"baseDomain":    config.BaseDomain,
			"routes":        config.Routes,
			"cloudflare":    config.Cloudflare.public(),
			"tunnelRunning": running,
		})
	})
	mux.HandleFunc("POST /api/cloudflare/setup", a.setupCloudflare)
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
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, existing := range a.config.Routes {
			if existing.Name == route.Name {
				http.Error(w, "name already exists", http.StatusConflict)
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
		a.mu.Lock()
		defer a.mu.Unlock()
		for i, route := range a.config.Routes {
			if route.Name == name {
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
		// The admin listener is loopback-only. Reject browser requests from other origins.
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+adminAddress {
			http.Error(w, "origin denied", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") == "" {
			http.Error(w, "origin required", http.StatusForbidden)
			return
		}
		if r.Host != adminAddress {
			http.Error(w, "host denied", http.StatusForbidden)
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
