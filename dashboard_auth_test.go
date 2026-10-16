package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func authRequest(action, password, confirm string, cookie *http.Cookie) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"action": action, "password": password, "confirm": confirm})
	r := httptest.NewRequest("POST", "http://127.0.0.1:8377/accounts/auth", strings.NewReader(string(b)))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Dictate-Request", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	dashboardGuard(dashboardAuthAPI)(w, r)
	return w
}
func TestPasswordGateSetupPersistenceAndRevocation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unlock, err := sessionLock()
	if err != nil {
		t.Fatal(err)
	}
	c, err := readDashboardConfig()
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	secret := c.SessionSecret
	protected := requireDashboardAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	w := httptest.NewRecorder()
	protected(w, httptest.NewRequest("GET", "/accounts/api", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated account data accessible")
	}
	if authRequest("setup", "abc", "abc", nil).Code != 400 {
		t.Fatal("password shorter than four accepted")
	}
	const password = "abcd"
	w = authRequest("setup", password, password, nil)
	if w.Code != 200 {
		t.Fatalf("setup failed: %s", w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/accounts" {
		t.Fatal("unsafe cookie attributes")
	}
	path, _ := dashboardConfigPath()
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), password) {
		t.Fatal("plaintext password persisted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe config permissions")
	}
	unlock, _ = sessionLock()
	loaded, err := readDashboardConfig()
	unlock()
	if err != nil || loaded.SessionSecret != secret {
		t.Fatal("session secret did not persist")
	}
	r := httptest.NewRequest("GET", "/accounts/api", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected(w, r)
	if w.Code != 204 {
		t.Fatal("session not accepted")
	}
	tampered := *cookie
	tampered.Value += "x"
	r = httptest.NewRequest("GET", "/accounts/api", nil)
	r.AddCookie(&tampered)
	if validDashboardSession(r, loaded) {
		t.Fatal("tampered session accepted")
	}
	expired := *cookie
	expired.Value = signedSession(loaded, time.Now().Add(-time.Hour).Unix(), "test")
	r = httptest.NewRequest("GET", "/accounts/api", nil)
	r.AddCookie(&expired)
	if validDashboardSession(r, loaded) {
		t.Fatal("expired session accepted")
	}
	if authRequest("setup", password, password, nil).Code != 409 {
		t.Fatal("password overwritten without authentication")
	}
	if authRequest("login", "wrong password", "", nil).Code != 401 {
		t.Fatal("wrong password accepted")
	}
	if authRequest("login", password, "", nil).Code != 200 {
		t.Fatal("correct password rejected")
	}
	if authRequest("logout", "", "", cookie).Code != 200 {
		t.Fatal("logout failed")
	}
	r = httptest.NewRequest("GET", "/accounts/api", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	protected(w, r)
	if w.Code != 401 {
		t.Fatal("logout did not invalidate existing sessions")
	}
}
func TestPasswordLoginThrottled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	passwordLimit.Lock()
	passwordLimit.failures = 0
	passwordLimit.until = time.Time{}
	passwordLimit.Unlock()
	t.Cleanup(func() {
		passwordLimit.Lock()
		passwordLimit.failures = 0
		passwordLimit.until = time.Time{}
		passwordLimit.Unlock()
	})
	if authRequest("setup", "test-password-123", "test-password-123", nil).Code != 200 {
		t.Fatal("setup failed")
	}
	for i := 0; i < 5; i++ {
		if authRequest("login", "wrong", "", nil).Code != 401 {
			t.Fatal("unexpected login response")
		}
	}
	if authRequest("login", "test-password-123", "", nil).Code != 429 {
		t.Fatal("password attempts not throttled")
	}
}
func TestDashboardAuthCannotBypassExistingOriginGuard(t *testing.T) {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8377/accounts/auth", strings.NewReader(`{"action":"setup","password":"attacker-password","confirm":"attacker-password"}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-Dictate-Request", "1")
	w := httptest.NewRecorder()
	dashboardGuard(dashboardAuthAPI)(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin setup accepted")
	}
}
