package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTMLLoginSessionAndLogout(t *testing.T) {
	app, err := loadTestConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	handler := app.adminHandler()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("unauthenticated page: %d %v", response.Code, response.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/login", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `name="password"`) {
		t.Fatalf("login page: %d", response.Code)
	}
	login := func(password string) *httptest.ResponseRecorder {
		body := "username=admin&password=" + password
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://127.0.0.1:8787")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if bad := login("wrong"); bad.Code != http.StatusUnauthorized || bad.Header().Get("WWW-Authenticate") != "" || !strings.Contains(bad.Body.String(), "帳號或密碼不正確") {
		t.Fatalf("wrong login: %d %v", bad.Code, bad.Header())
	}
	good := login("long-test-password-123")
	if good.Code != http.StatusSeeOther || len(good.Result().Cookies()) != 1 || !good.Result().Cookies()[0].HttpOnly {
		t.Fatalf("correct login: %d %v", good.Code, good.Header())
	}
	cookie := good.Result().Cookies()[0]
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session API access: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/logout", nil)
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("logged out session accepted: %d", response.Code)
	}
}

func TestPasswordChangeRevokesHTMLSession(t *testing.T) {
	dir := t.TempDir()
	app, err := loadTestConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	app.adminAuth = testAdminAuth(t, app)
	data := "DDMAN_ADMIN_HOST=admin.ddman.cc\nDDMAN_ADMIN_PORT=8787\nDDMAN_ADMIN_USERNAME=admin\nDDMAN_ADMIN_PASSWORD=long-test-password-123\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/login", strings.NewReader("username=admin&password=long-test-password-123"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", "http://127.0.0.1:8787")
	loginResponse := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("login: %d", loginResponse.Code)
	}
	cookie := loginResponse.Result().Cookies()[0]
	body := `{"currentPassword":"long-test-password-123","newUsername":"admin","newPassword":"new-long-password-456"}`
	change := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/admin/credentials", strings.NewReader(body))
	change.Header.Set("Origin", "http://127.0.0.1:8787")
	change.AddCookie(cookie)
	changeResponse := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(changeResponse, change)
	if changeResponse.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", changeResponse.Code, changeResponse.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/state", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.adminHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old session accepted after password change: %d", response.Code)
	}
}
