package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

const adminSessionCookie = "ddman_session"
const adminSessionLifetime = 12 * time.Hour

func (a *App) createAdminSession(w http.ResponseWriter, r *http.Request) error {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	a.sessionMu.Lock()
	if a.sessions == nil {
		a.sessions = make(map[string]time.Time)
	}
	a.sessions[token] = time.Now().Add(adminSessionLifetime)
	a.sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(adminSessionLifetime.Seconds())})
	return nil
}

func (a *App) hasAdminSession(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return false
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	expiry, found := a.sessions[cookie.Value]
	if !found {
		return false
	}
	if time.Now().After(expiry) {
		delete(a.sessions, cookie.Value)
		return false
	}
	return true
}

func (a *App) revokeAdminSessions() {
	a.sessionMu.Lock()
	a.sessions = make(map[string]time.Time)
	a.sessionMu.Unlock()
}

func (a *App) logoutAdmin(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		a.sessionMu.Lock()
		delete(a.sessions, cookie.Value)
		a.sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) loginAdmin(w http.ResponseWriter, r *http.Request, auth *AdminAuth) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid login form", http.StatusBadRequest)
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if !auth.verifyCredentials(username, password) {
		serveLoginHTML(w, http.StatusUnauthorized, true)
		return
	}
	if err := a.createAdminSession(w, r); err != nil {
		http.Error(w, "could not start login session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func serveLoginHTML(w http.ResponseWriter, status int, invalid bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	page := loginHTML
	if invalid {
		page = strings.Replace(page, `id="login-error" hidden`, `id="login-error"`, 1)
	}
	_, _ = w.Write([]byte(page))
}

const loginHTML = `<!doctype html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>登入 · Wildcard Tunnel Manager</title>
  <style>
    :root{font-family:system-ui,-apple-system,sans-serif;color:#142236;background:#f4f7fb}
    body{min-height:100vh;display:grid;place-items:center;margin:0;padding:24px;box-sizing:border-box}
    main{width:min(100%,390px);background:#fff;border:1px solid #dce4ed;border-radius:16px;padding:32px;box-shadow:0 8px 28px #14223612}
    h1{font-size:28px;margin:0 0 10px}p{color:#526173;line-height:1.5;margin:0 0 24px}
    label{display:grid;gap:7px;margin:16px 0;font-weight:600;font-size:14px}
    input{font:inherit;padding:12px;border:1px solid #b9c7d7;border-radius:8px;width:100%;box-sizing:border-box}
    button{font:inherit;background:#155eef;color:#fff;border:0;border-radius:8px;padding:12px;width:100%;cursor:pointer;margin-top:8px}
    #login-error{color:#b42318;margin:0 0 12px}#login-error[hidden]{display:none}
  </style>
</head>
<body>
  <main>
    <h1>管理登入</h1>
    <p>登入後管理 Cloudflare Tunnel 與本機路由。</p>
    <p id="login-error" hidden role="alert">帳號或密碼不正確。</p>
    <form method="post" action="/login">
      <label>帳號<input name="username" autocomplete="username" required autofocus></label>
      <label>密碼<input name="password" type="password" autocomplete="current-password" required></label>
      <button type="submit">登入</button>
    </form>
  </main>
</body>
</html>`
