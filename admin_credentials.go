package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
)

type adminCredentialsRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewUsername     string `json:"newUsername"`
	NewPassword     string `json:"newPassword"`
}

func (a *App) updateAdminCredentials(w http.ResponseWriter, r *http.Request) {
	var input adminCredentialsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateAdminCredentials(input.NewUsername, input.NewPassword); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	current := a.currentAdminAuth()
	if !current.verifyCredentials(current.username, input.CurrentPassword) {
		http.Error(w, "current password is incorrect", http.StatusForbidden)
		return
	}
	a.mu.RLock()
	baseDomain := a.config.BaseDomain
	routes := append([]Route(nil), a.config.Routes...)
	a.mu.RUnlock()
	next, err := newAdminAuth(AdminSettings{Host: current.Host, Port: current.Port, TailscaleIP: current.TailscaleIP, Username: input.NewUsername, Password: input.NewPassword}, baseDomain, routes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveAdminCredentials(filepath.Join(filepath.Dir(a.path), ".env"), input.NewUsername, input.NewPassword); err != nil {
		http.Error(w, "could not save admin credentials", http.StatusInternalServerError)
		return
	}
	a.adminMu.Lock()
	a.adminAuth = next
	a.adminMu.Unlock()
	a.revokeAdminSessions()
	w.WriteHeader(http.StatusNoContent)
}
