package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed accounts.html
var accountsHTML string

func removeAccount(a accountStore, id string) error {
	found := false
	for i, v := range a.Accounts {
		if v.ID == id {
			a.Accounts = append(a.Accounts[:i], a.Accounts[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return errors.New("account not found")
	}
	if a.Active == id {
		a.Active = ""
		if len(a.Accounts) > 0 {
			a.Active = a.Accounts[0].ID
		}
	}
	return saveAccounts(a)
}

// Restrict browser access to loopback or the explicitly configured HTTPS origin.
// Mutations require a custom header; cross-origin preflights are rejected.
func dashboardGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; media-src 'self' blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		expectedOrigin, err := publicOrigin()
		if err != nil {
			http.Error(w, "Invalid public URL configuration", http.StatusForbidden)
			return
		}
		public := expectedOrigin != ""
		if public {
			if "https://"+r.Host != expectedOrigin {
				http.Error(w, "Invalid dashboard host", http.StatusForbidden)
				return
			}
		} else {
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil || host != "127.0.0.1" {
				http.Error(w, "Open dashboard through http://127.0.0.1:<port>/accounts", http.StatusForbidden)
				return
			}
			remote, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || net.ParseIP(remote) == nil || !net.ParseIP(remote).IsLoopback() {
				http.Error(w, "Local access only", http.StatusForbidden)
				return
			}
			expectedOrigin = "http://" + r.Host
		}
		origin := r.Header.Get("Origin")
		if (origin != "" && origin != expectedOrigin) || (public && r.Method != http.MethodGet && origin == "") {
			http.Error(w, "Invalid origin", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(w, "Cross-site access blocked", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Dictate-Request") != "1" {
			http.Error(w, "Missing request header", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func registerDashboard(mux *http.ServeMux) {
	mux.HandleFunc("/accounts/auth", dashboardGuard(dashboardAuthAPI))
	mux.HandleFunc("/accounts", dashboardGuard(requireDashboardAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(accountsHTML))
	})))
	mux.HandleFunc("/accounts/api", dashboardGuard(requireDashboardAuth(accountAPI)))
}

func accountAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input struct {
			Action   string `json:"action"`
			ID       string `json:"id"`
			Label    string `json:"label"`
			Callback string `json:"callback"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input) != nil {
			writeJSON(w, 400, map[string]string{"error": "Invalid request"})
			return
		}
		if input.Action == "login" {
			link, _, err := beginLogin()
			if err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]string{"url": link})
			return
		}
		if input.Action == "cancel-login" || input.Action == "callback" {
			var err error
			if input.Action == "cancel-login" {
				err = cancelLogin(input.ID)
			} else {
				err = submitCallback(input.Callback)
			}
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		unlock, err := sessionLock()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
		defer unlock()
		a, err := readAccounts()
		if err == nil {
			switch input.Action {
			case "activate":
				found := false
				for _, v := range a.Accounts {
					if v.ID == input.ID {
						a.Active = v.ID
						found = true
						break
					}
				}
				if !found {
					err = errors.New("Account not found")
				} else {
					err = saveAccounts(a)
				}
			case "remove":
				err = removeAccount(a, input.ID)
			case "rename":
				label := strings.TrimSpace(input.Label)
				if label == "" || len(label) > 80 {
					err = errors.New("Use a name between 1 and 80 bytes")
					break
				}
				found := false
				for i := range a.Accounts {
					if a.Accounts[i].ID == input.ID {
						a.Accounts[i].Label = label
						found = true
						break
					}
				}
				if !found {
					err = errors.New("Account not found")
				} else {
					err = saveAccounts(a)
				}
			default:
				err = errors.New("Unknown action")
			}
		}
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	unlock, err := sessionLock()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	defer unlock()
	a, err := readAccounts()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return
	}
	type publicAccount struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Active bool   `json:"active"`
		Status string `json:"status"`
	}
	rows := []publicAccount{}
	for _, v := range a.Accounts {
		status := "Ready"
		if v.Session.ExpiresAt <= time.Now().Unix() {
			status = "Refresh needed"
		}
		if v.Session.RefreshToken == "" {
			status = "Sign-in needed"
		}
		rows = append(rows, publicAccount{v.ID, v.Label, v.ID == a.Active, status})
	}
	pending, message, link, loginID := pendingLogin()
	writeJSON(w, 200, map[string]any{"accounts": rows, "pending": pending, "message": message, "legacy": legacyAuthMode, "login_url": link, "login_id": loginID})
}
