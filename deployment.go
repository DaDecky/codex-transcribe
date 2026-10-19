package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strings"
)

// The public origin is explicitly configured, never inferred from proxy headers.
// TLS must terminate at the reverse proxy; the backend stays on a private network.
func publicOrigin() (string, error) {
	raw := os.Getenv("CODEX_DICTATE_PUBLIC_URL")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("CODEX_DICTATE_PUBLIC_URL must be an HTTPS origin without a path, credentials, query, or fragment")
	}
	return "https://" + u.Host, nil
}

func secureDashboardCookie() bool {
	origin, err := publicOrigin()
	return err == nil && origin != ""
}

// Caller holds sessionLock. A public deployment never opens first-run setup to
// the Internet. The bootstrap secret initializes only an unconfigured password.
func initializeDashboardPassword(c dashboardConfig) error {
	origin, err := publicOrigin()
	if err != nil {
		return err
	}
	if c.PasswordHash != "" {
		return nil
	}
	path := os.Getenv("CODEX_DICTATE_DASHBOARD_PASSWORD_FILE")
	if path == "" {
		if origin != "" {
			return errors.New("public dashboard requires CODEX_DICTATE_DASHBOARD_PASSWORD_FILE on first startup")
		}
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("cannot read dashboard bootstrap password file")
	}
	password := strings.TrimRight(string(b), "\r\n")
	if len(password) < 4 || len(password) > 256 {
		return errors.New("dashboard bootstrap password must contain 4–256 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	c.PasswordSalt = base64.RawURLEncoding.EncodeToString(salt)
	c.PasswordHash, err = passwordHash(password, c.PasswordSalt)
	if err != nil {
		return err
	}
	return saveDashboardConfig(c)
}
