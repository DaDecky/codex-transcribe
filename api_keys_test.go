package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestManagedConsumerKeyLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := testProxy(t, successfulUpstream)
	p.managedKeys = true
	if authRequest("setup", "test-only-password", "test-only-password", nil).Code != 200 {
		t.Fatal("setup failed")
	}
	for _, auth := range []string{"", "Bearer wrong", "Basic invalid"} {
		r := uploadRequest(t, nil, []byte("audio"))
		r.Header.Set("Authorization", auth)
		assertError(t, serve(p, r), 401, "invalid_api_key")
	}
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/accounts/keys", strings.NewReader(body))
		w := httptest.NewRecorder()
		keyAPI(w, r)
		return w
	}
	created := call(`{"action":"create","name":"Test consumer"}`)
	if created.Code != 201 {
		t.Fatalf("create failed: %s", created.Body.String())
	}
	var result struct {
		Key string `json:"key"`
	}
	json.Unmarshal(created.Body.Bytes(), &result)
	if !strings.HasPrefix(result.Key, "sk-dictate-") {
		t.Fatal("invalid key")
	}
	path, _ := dashboardConfigPath()
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), result.Key) {
		t.Fatal("plaintext key persisted")
	}
	r := uploadRequest(t, nil, []byte("audio"))
	r.Header.Set("Authorization", "Bearer "+result.Key)
	if response := serve(p, r); response.Code != 200 {
		t.Fatalf("valid key rejected: %s", response.Body.String())
	}
	// Secret is returned only by creation, never by listing.
	listed := httptest.NewRecorder()
	keyAPI(listed, httptest.NewRequest("GET", "/accounts/keys", nil))
	if strings.Contains(listed.Body.String(), result.Key) || strings.Contains(listed.Body.String(), `"hash"`) {
		t.Fatal("key list leaked secret/hash")
	}
	var list struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	json.Unmarshal(listed.Body.Bytes(), &list)
	if len(list.Keys) != 1 {
		t.Fatal("key not listed")
	}
	if response := call(`{"action":"revoke","id":"` + list.Keys[0].ID + `"}`); response.Code != 200 {
		t.Fatal("revoke failed")
	}
	r = uploadRequest(t, nil, []byte("audio"))
	r.Header.Set("Authorization", "Bearer "+result.Key)
	assertError(t, serve(p, r), 401, "invalid_api_key")
}
func TestKeysAndDashboardTranscriptionRequireDashboardSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := testProxy(t, successfulUpstream)
	p.managedKeys = true
	created := authRequest("setup", "test-only-password", "test-only-password", nil)
	if created.Code != 200 {
		t.Fatal("setup failed")
	}
	cookie := created.Result().Cookies()[0]
	for _, path := range []string{"/accounts/keys", "/accounts/transcriptions"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8377"+path, strings.NewReader(`{"action":"create","name":"attacker"}`))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("X-Dictate-Request", "1")
		response := serve(p, r)
		if response.Code != 401 {
			t.Fatalf("%s bypassed dashboard gate: %d", path, response.Code)
		}
	}
	r := uploadRequest(t, nil, []byte("audio"))
	r.URL.Path = "/accounts/transcriptions"
	r.Host = "127.0.0.1:8377"
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Dictate-Request", "1")
	r.AddCookie(cookie)
	if response := serve(p, r); response.Code != 200 {
		t.Fatalf("dashboard test cannot use session: %s", response.Body.String())
	}
	// Browser cookie cannot authorize public consumer endpoint.
	r = uploadRequest(t, nil, []byte("audio"))
	r.AddCookie(cookie)
	assertError(t, serve(p, r), 401, "invalid_api_key")
}
func TestConsumerKeyDoesNotUnlockDashboard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := testProxy(t, successfulUpstream)
	p.managedKeys = true
	p.apiKey = "environment-key"
	r := httptest.NewRequest("GET", "http://127.0.0.1:8377/accounts/keys", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Authorization", "Bearer environment-key")
	if response := serve(p, r); response.Code != 401 {
		t.Fatal("consumer key unlocked dashboard")
	}
	r = uploadRequest(t, nil, []byte("audio"))
	r.Header.Set("Authorization", "Bearer environment-key")
	if response := serve(p, r); response.Code != http.StatusOK {
		t.Fatal("legacy environment consumer key rejected")
	}
}
