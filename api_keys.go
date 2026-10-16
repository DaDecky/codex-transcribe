package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type consumerKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"created_at"`
}

func consumerKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func acceptsConsumerKey(keys []consumerKey, value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	hash := consumerKeyHash(value)
	matched := 0
	for _, key := range keys {
		matched |= subtle.ConstantTimeCompare([]byte(hash), []byte(key.Hash))
	}
	return matched == 1
}
func keyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(405)
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
	if r.Method == http.MethodGet {
		rows := []map[string]string{}
		for _, k := range c.APIKeys {
			rows = append(rows, map[string]string{"id": k.ID, "name": k.Name, "prefix": k.Prefix, "created_at": k.CreatedAt})
		}
		writeJSON(w, 200, map[string]any{"keys": rows})
		return
	}
	var input struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		ID     string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
		writeJSON(w, 400, map[string]string{"error": "Invalid request"})
		return
	}
	switch input.Action {
	case "create":
		name := strings.TrimSpace(input.Name)
		if name == "" || len(name) > 80 {
			writeJSON(w, 400, map[string]string{"error": "Use a name between 1 and 80 bytes"})
			return
		}
		if len(c.APIKeys) >= 100 {
			writeJSON(w, 400, map[string]string{"error": "Revoke unused keys before creating more (maximum 100)"})
			return
		}
		secret, err := randomCode()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not generate key"})
			return
		}
		id, err := randomCode()
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not generate key"})
			return
		}
		raw := "sk-dictate-" + secret
		c.APIKeys = append(c.APIKeys, consumerKey{ID: id, Name: name, Prefix: raw[:18], Hash: consumerKeyHash(raw), CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		if saveDashboardConfig(c) != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not save key"})
			return
		}
		writeJSON(w, 201, map[string]string{"key": raw})
		return
	case "revoke":
		found := false
		for i, k := range c.APIKeys {
			if k.ID == input.ID {
				c.APIKeys = append(c.APIKeys[:i], c.APIKeys[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 404, map[string]string{"error": "Key not found"})
			return
		}
		if saveDashboardConfig(c) != nil {
			writeJSON(w, 503, map[string]string{"error": "Could not revoke key"})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	default:
		writeJSON(w, 400, map[string]string{"error": "Unknown action"})
	}
}
func (p *proxy) authorizeConsumer(w http.ResponseWriter, r *http.Request) bool {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if p.apiKey != "" && ok && subtle.ConstantTimeCompare([]byte(consumerKeyHash(provided)), []byte(consumerKeyHash(p.apiKey))) == 1 {
		return true
	}
	if p.managedKeys {
		unlock, err := sessionLock()
		if err != nil {
			writeError(w, 503, "key_store_unavailable", "server_error", "API key store unavailable.", "")
			return false
		}
		c, err := readDashboardConfig()
		unlock()
		if err != nil {
			writeError(w, 503, "key_store_unavailable", "server_error", "API key store unavailable.", "")
			return false
		}
		if ok && acceptsConsumerKey(c.APIKeys, provided) {
			return true
		}
	} else if p.apiKey == "" {
		return true
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, 401, "invalid_api_key", "authentication_error", "A valid Dictate API key is required.", "")
	return false
}
