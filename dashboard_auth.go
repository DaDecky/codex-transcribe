package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dashboardCookie = "dictate_dashboard"
const passwordIterations = 600000

type dashboardConfig struct {
	APIKeys       []consumerKey `json:"api_keys,omitempty"`
	SessionSecret string        `json:"session_secret"`
	PasswordSalt  string        `json:"password_salt,omitempty"`
	PasswordHash  string        `json:"password_hash,omitempty"`
}

func dashboardConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "codex-dictate", "dashboard.json"), err
}
func saveDashboardConfig(c dashboardConfig) error {
	path, err := dashboardConfigPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dashboard-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Caller holds sessionLock, shared by dashboard processes on different ports.
func readDashboardConfig() (dashboardConfig, error) {
	path, err := dashboardConfigPath()
	if err != nil {
		return dashboardConfig{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		secret, err := randomCode()
		if err != nil {
			return dashboardConfig{}, err
		}
		c := dashboardConfig{SessionSecret: secret}
		return c, saveDashboardConfig(c)
	}
	if err != nil {
		return dashboardConfig{}, err
	}
	var c dashboardConfig
	if json.Unmarshal(b, &c) != nil {
		return c, errors.New("invalid dashboard config")
	}
	key, err := base64.RawURLEncoding.DecodeString(c.SessionSecret)
	if err != nil || len(key) != 32 {
		return c, errors.New("invalid dashboard session secret")
	}
	if (c.PasswordHash == "") != (c.PasswordSalt == "") {
		return c, errors.New("invalid dashboard password config")
	}
	if c.PasswordHash != "" {
		hash, e1 := base64.RawURLEncoding.DecodeString(c.PasswordHash)
		salt, e2 := base64.RawURLEncoding.DecodeString(c.PasswordSalt)
		if e1 != nil || e2 != nil || len(hash) != 32 || len(salt) != 16 {
			return c, errors.New("invalid dashboard password config")
		}
	}
	// Refuse to keep credentials readable by other OS users.
	if err = os.Chmod(path, 0600); err != nil {
		return c, err
	}
	return c, nil
}
func passwordHash(password, salt string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(salt)
	if err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, raw, passwordIterations, 32)
	return base64.RawURLEncoding.EncodeToString(key), err
}
func signedSession(c dashboardConfig, expiry int64, nonce string) string {
	payload := strconv.FormatInt(expiry, 10) + "." + nonce
	mac := hmac.New(sha256.New, []byte(c.SessionSecret))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func validDashboardSession(r *http.Request, c dashboardConfig) bool {
	if c.PasswordHash == "" {
		return false
	}
	cookie, err := r.Cookie(dashboardCookie)
	if err != nil || len(cookie.Value) > 256 {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expiry <= time.Now().Unix() || expiry > time.Now().Add(8*time.Hour+time.Minute).Unix() {
		return false
	}
	expected := signedSession(c, expiry, parts[1])
	return hmac.Equal([]byte(cookie.Value), []byte(expected))
}
func setDashboardCookie(w http.ResponseWriter, c dashboardConfig) error {
	nonce, err := randomCode()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Value: signedSession(c, time.Now().Add(8*time.Hour).Unix(), nonce), Path: "/accounts", HttpOnly: true, Secure: secureDashboardCookie(), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	return nil
}
func requireDashboardAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		unlock, err := sessionLock()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Dashboard configuration unavailable"})
			return
		}
		c, err := readDashboardConfig()
		unlock()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Dashboard configuration unavailable"})
			return
		}
		if !validDashboardSession(r, c) {
			if r.URL.Path == "/accounts" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, dashboardLoginHTML)
				return
			}
			writeJSON(w, 401, map[string]string{"error": "Unlock the dashboard first"})
			return
		}
		next(w, r)
	}
}

var passwordLimit struct {
	sync.Mutex
	failures int
	until    time.Time
}

func dashboardAuthAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if r.Method == http.MethodGet {
		unlock, err := sessionLock()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Configuration unavailable"})
			return
		}
		defer unlock()
		c, err := readDashboardConfig()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Configuration unavailable"})
			return
		}
		writeJSON(w, 200, map[string]bool{"setup": c.PasswordHash == "", "authenticated": validDashboardSession(r, c)})
		return
	}
	var input struct {
		Action   string `json:"action"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
		writeJSON(w, 400, map[string]string{"error": "Invalid request"})
		return
	}
	if input.Action == "setup" && secureDashboardCookie() {
		writeJSON(w, 403, map[string]string{"error": "Public dashboard setup requires the server bootstrap password file"})
		return
	}
	passwordLimit.Lock()
	defer passwordLimit.Unlock()
	if time.Now().Before(passwordLimit.until) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, map[string]string{"error": "Too many attempts. Try again in one minute."})
		return
	}
	unlock, err := sessionLock()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Configuration unavailable"})
		return
	}
	defer unlock()
	c, err := readDashboardConfig()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Configuration unavailable"})
		return
	}
	if input.Action == "logout" {
		if !validDashboardSession(r, c) {
			writeJSON(w, 401, map[string]string{"error": "Not authenticated"})
			return
		}
		// Rotate persisted secret so copied cookies and sessions on other ports expire.
		c.SessionSecret, err = randomCode()
		if err == nil {
			err = saveDashboardConfig(c)
		}
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not lock dashboard"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Path: "/accounts", MaxAge: -1, HttpOnly: true, Secure: secureDashboardCookie(), SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if input.Action == "setup" {
		if c.PasswordHash != "" {
			writeJSON(w, 409, map[string]string{"error": "A dashboard password is already configured"})
			return
		}
		if len(input.Password) < 4 || len(input.Password) > 256 || input.Password != input.Confirm {
			writeJSON(w, 400, map[string]string{"error": "Use 4–256 characters and matching passwords"})
			return
		}
		salt := make([]byte, 16)
		if _, err = rand.Read(salt); err == nil {
			c.PasswordSalt = base64.RawURLEncoding.EncodeToString(salt)
			c.PasswordHash, err = passwordHash(input.Password, c.PasswordSalt)
		}
		if err == nil {
			err = saveDashboardConfig(c)
		}
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not save dashboard password"})
			return
		}
	} else if input.Action == "login" {
		if c.PasswordHash == "" {
			writeJSON(w, 409, map[string]string{"error": "Set a dashboard password first"})
			return
		}
		if len(input.Password) > 256 {
			input.Password = ""
		}
		hash, err := passwordHash(input.Password, c.PasswordSalt)
		if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(c.PasswordHash)) != 1 {
			passwordLimit.failures++
			if passwordLimit.failures >= 5 {
				passwordLimit.until = time.Now().Add(time.Minute)
				passwordLimit.failures = 0
			}
			writeJSON(w, 401, map[string]string{"error": "Incorrect password"})
			return
		}
	} else {
		writeJSON(w, 400, map[string]string{"error": "Unknown action"})
		return
	}
	passwordLimit.failures = 0
	if err = setDashboardCookie(w, c); err != nil {
		writeJSON(w, 503, map[string]string{"error": "Could not create session"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

const dashboardLoginHTML = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unlock · Codex Dictate</title><style>:root{color-scheme:dark;font:15px/1.5 system-ui;background:#111214;color:#e9e9ed}main{max-width:360px;margin:15vh auto;padding:24px}h1{font-size:26px;margin:6px 0}p{color:#9c9fa8;font-size:13px}label{display:block;margin:16px 0 6px}input,button{box-sizing:border-box;width:100%;padding:11px;border-radius:8px;border:1px solid #36383f;font:inherit}input{background:#191a1e;color:#eee}button{margin-top:20px;background:#c9efd9;color:#132d1e;cursor:pointer}#error{color:#ffb4ac;min-height:24px}[hidden]{display:none}</style><main><p>CODEX DICTATE</p><h1 id="title">Unlock dashboard</h1><p id="hint">Enter your local dashboard password.</p><form id="form"><label for="password">Password</label><input id="password" type="password" maxlength="256" autocomplete="current-password" required><div id="confirmation" hidden><label for="confirm">Confirm password</label><input id="confirm" type="password" maxlength="256" autocomplete="new-password"></div><button id="submit" disabled>Unlock</button></form><p id="error" role="status"></p><p>This password protects account management. Dictation continues while the dashboard is locked.</p></main><script>let setup=false;const el=id=>document.getElementById(id);fetch('/accounts/auth').then(r=>r.json()).then(d=>{if(d.error)throw Error(d.error);setup=d.setup;if(setup){el('title').textContent='Set dashboard password';el('hint').textContent='Choose a password with at least 4 characters. Your session secret is generated automatically.';el('password').autocomplete='new-password';el('password').minLength=4;el('confirmation').hidden=false;el('confirm').required=true;el('submit').textContent='Set password'}el('submit').disabled=false}).catch(e=>el('error').textContent=e.message);el('form').onsubmit=async e=>{e.preventDefault();el('submit').disabled=true;try{const r=await fetch('/accounts/auth',{method:'POST',headers:{'Content-Type':'application/json','X-Dictate-Request':'1'},body:JSON.stringify({action:setup?'setup':'login',password:el('password').value,confirm:el('confirm').value})});const d=await r.json();if(!r.ok)throw Error(d.error||'Sign-in failed');location.replace('/accounts')}catch(e){el('error').textContent=e.message;el('password').value='';el('confirm').value=''}finally{el('submit').disabled=false}};</script></html>`
