package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAdminAuth(t *testing.T, app *App) *AdminAuth {
	t.Helper()
	auth, err := newAdminAuth(AdminSettings{Host: "admin." + app.config.BaseDomain, Port: 8787, Username: "admin", Password: "long-test-password-123"}, app.config.BaseDomain, app.config.Routes)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestAdminLoginOnLocalAndTailscaleAddress(t *testing.T) {
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	app.adminAuth.Port = 9887
	app.adminAuth.TailscaleIP = "100.115.236.102"
	handler := app.adminHandler()
	for _, host := range []string{"127.0.0.1:9887", "100.115.236.102:9887"} {
		request := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/state", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s without login: %d", host, response.Code)
		}
		request.SetBasicAuth("admin", "wrong-password")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s with wrong password: %d", host, response.Code)
		}
		request.SetBasicAuth("admin", "long-test-password-123")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("%s with login: %d", host, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://admin.ddman.cc/api/state", nil)
	request.SetBasicAuth("admin", "long-test-password-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("public admin hostname accepted: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	request.SetBasicAuth("admin", "long-test-password-123")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("old admin port still accepted: %d", response.Code)
	}
}

func TestAdminTailscaleOriginValidation(t *testing.T) {
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	app.adminAuth.TailscaleIP = "100.115.236.102"
	for _, origin := range []string{"http://100.115.236.102:8787", "http://evil.example"} {
		request := httptest.NewRequest(http.MethodGet, "http://100.115.236.102:8787/api/state", nil)
		request.SetBasicAuth("admin", "long-test-password-123")
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		app.adminHandler().ServeHTTP(response, request)
		want := http.StatusOK
		if origin != "http://100.115.236.102:8787" {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Errorf("origin %s: got %d, want %d", origin, response.Code, want)
		}
	}
}

func TestAdminEnvRequiresPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	data := []byte("DDMAN_ADMIN_HOST=admin.ddman.cc\nDDMAN_ADMIN_PORT=8787\nDDMAN_ADMIN_USERNAME=admin\nDDMAN_ADMIN_PASSWORD=long-test-password-123\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadAdminSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Port != 8787 {
		t.Fatalf("admin port: %d", settings.Port)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAdminSettings(path); err == nil {
		t.Fatal("world-readable admin password was accepted")
	}
}

func TestCloudflareSettingsPersistBesideAdminCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	data := []byte("DDMAN_ADMIN_HOST=admin.ddman.cc\nDDMAN_ADMIN_PORT=8787\nDDMAN_ADMIN_USERNAME=admin\nDDMAN_ADMIN_PASSWORD=long-test-password-123\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveCFSettings(path, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "scoped-test-token"); err != nil {
		t.Fatal(err)
	}
	settings, err := loadAdminSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Password != "long-test-password-123" || settings.CFAPIToken != "scoped-test-token" || settings.CFAccountID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("saved settings differ")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf(".env permissions: %v %v", info, err)
	}
}

func TestAdminCredentialsUpdateRequiresCurrentPasswordAndPreservesCloudflareSettings(t *testing.T) {
	dir := t.TempDir()
	app, err := loadTestConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	envPath := filepath.Join(dir, ".env")
	data := "DDMAN_ADMIN_HOST=admin.ddman.cc\nDDMAN_ADMIN_PORT=8787\nDDMAN_ADMIN_USERNAME=admin\nDDMAN_ADMIN_PASSWORD=long-test-password-123\nCF_API_TOKEN=keep-this-token\n"
	if err := os.WriteFile(envPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	post := func(currentPassword string) int {
		body := `{"currentPassword":"` + currentPassword + `","newUsername":"newadmin","newPassword":"new-long-password-456"}`
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/admin/credentials", strings.NewReader(body))
		req.SetBasicAuth("admin", "long-test-password-123")
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		res := httptest.NewRecorder()
		app.adminHandler().ServeHTTP(res, req)
		return res.Code
	}
	if got := post("wrong-password"); got != http.StatusForbidden {
		t.Fatalf("wrong current password: %d", got)
	}
	if got := post("long-test-password-123"); got != http.StatusNoContent {
		t.Fatalf("correct current password: %d", got)
	}
	settings, err := loadAdminSettings(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Username != "newadmin" || settings.Password != "new-long-password-456" || settings.CFAPIToken != "keep-this-token" {
		t.Fatal("saved admin credentials or Cloudflare token differ")
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	oldRequest.SetBasicAuth("admin", "long-test-password-123")
	oldResponse := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old login still accepted: %d", oldResponse.Code)
	}
	newRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	newRequest.SetBasicAuth("newadmin", "new-long-password-456")
	newResponse := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(newResponse, newRequest)
	if newResponse.Code != http.StatusOK {
		t.Fatalf("new login failed: %d", newResponse.Code)
	}
}
