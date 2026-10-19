package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicOriginValidation(t *testing.T) {
	for _, raw := range []string{"http://dictate.example.com", "https://user:pass@dictate.example.com", "https://dictate.example.com/accounts", "https://dictate.example.com?x=1", "https://dictate.example.com#x", "https://"} {
		t.Setenv("CODEX_DICTATE_PUBLIC_URL", raw)
		if _, err := publicOrigin(); err == nil {
			t.Errorf("accepted invalid public URL %q", raw)
		}
	}
	t.Setenv("CODEX_DICTATE_PUBLIC_URL", "https://dictate.example.com/")
	if got, err := publicOrigin(); err != nil || got != "https://dictate.example.com" {
		t.Fatal("valid public origin rejected")
	}
	if err := validateListen("0.0.0.0:8378", ""); err != nil {
		t.Fatal("managed-key deployment listener rejected")
	}
}

func TestPublicDashboardGuard(t *testing.T) {
	t.Setenv("CODEX_DICTATE_PUBLIC_URL", "https://dictate.example.com")
	handler := dashboardGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, tc := range []struct {
		name, method, host, origin, header string
		want                               int
	}{
		{"proxied get", "GET", "dictate.example.com", "", "", 204},
		{"same origin mutation", "POST", "dictate.example.com", "https://dictate.example.com", "1", 204},
		{"foreign origin", "POST", "dictate.example.com", "https://evil.example", "1", 403},
		{"missing origin", "POST", "dictate.example.com", "", "1", 403},
		{"http origin", "POST", "dictate.example.com", "http://dictate.example.com", "1", 403},
		{"missing custom header", "POST", "dictate.example.com", "https://dictate.example.com", "", 403},
		{"forged host", "GET", "evil.example", "", "", 403},
		{"loopback bypass", "GET", "127.0.0.1:8378", "", "", 403},
		{"preflight", "OPTIONS", "dictate.example.com", "https://dictate.example.com", "1", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+"/accounts/api", nil)
			r.RemoteAddr = "172.20.0.3:40000"
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Dictate-Request", tc.header)
			r.Header.Set("X-Forwarded-Host", "dictate.example.com")
			r.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestPublicBootstrapPasswordPersistence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_DICTATE_PUBLIC_URL", "https://dictate.example.com")
	t.Setenv("CODEX_DICTATE_DASHBOARD_PASSWORD_FILE", "")
	unlock, err := sessionLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	c, err := readDashboardConfig()
	if err != nil {
		t.Fatal(err)
	}
	if initializeDashboardPassword(c) == nil {
		t.Fatal("public setup allowed without bootstrap password")
	}
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("abcd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_DICTATE_DASHBOARD_PASSWORD_FILE", path)
	if err := initializeDashboardPassword(c); err != nil {
		t.Fatal(err)
	}
	saved, err := readDashboardConfig()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := passwordHash("abcd", saved.PasswordSalt)
	if err != nil || hash != saved.PasswordHash || saved.SessionSecret != c.SessionSecret {
		t.Fatal("bootstrap password or secret not persisted correctly")
	}
	t.Setenv("CODEX_DICTATE_DASHBOARD_PASSWORD_FILE", "/nonexistent")
	if err := initializeDashboardPassword(saved); err != nil {
		t.Fatal("existing password depended on bootstrap file")
	}
	w := httptest.NewRecorder()
	if err := setDashboardCookie(w, saved); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("public session cookie missing security attributes")
	}
}

func TestPublicDashboardCannotSetPasswordOverHTTP(t *testing.T) {
	t.Setenv("CODEX_DICTATE_PUBLIC_URL", "https://dictate.example.com")
	r := httptest.NewRequest("POST", "http://dictate.example.com/accounts/auth", strings.NewReader(`{"action":"setup","password":"abcd","confirm":"abcd"}`))
	r.Header.Set("Origin", "https://dictate.example.com")
	r.Header.Set("X-Dictate-Request", "1")
	w := httptest.NewRecorder()
	dashboardGuard(dashboardAuthAPI)(w, r)
	if w.Code != 403 {
		t.Fatal("public dashboard accepted web password setup")
	}
}
