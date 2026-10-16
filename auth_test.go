package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeAccounts(t *testing.T, initial accountStore) *accountStore {
	t.Helper()
	oldRead, oldSave := readAccounts, saveAccounts
	current := initial
	readAccounts = func() (accountStore, error) {
		b, _ := json.Marshal(current)
		var copy accountStore
		json.Unmarshal(b, &copy)
		return copy, nil
	}
	saveAccounts = func(a accountStore) error { current = a; return nil }
	t.Cleanup(func() { readAccounts, saveAccounts = oldRead, oldSave })
	return &current
}
func TestConcurrentRefreshUsesRotatedTokenOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_DICTATE_AUTH_MODE", "")
	store := fakeAccounts(t, accountStore{Active: "one", Accounts: []account{{ID: "one", Session: session{AccessToken: "expired", RefreshToken: "original", ExpiresAt: 1}}}})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Form.Get("refresh_token") != "original" {
			t.Error("wrong refresh token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"renewed","refresh_token":"rotated","expires_in":3600}`))
	}))
	defer srv.Close()
	old := oauthTokenURL
	oauthTokenURL = srv.URL
	t.Cleanup(func() { oauthTokenURL = old })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, _, err := loadAuth()
			if err != nil || token != "renewed" {
				t.Errorf("loadAuth failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 || store.Accounts[0].Session.RefreshToken != "rotated" {
		t.Fatalf("refreshes=%d; rotation not saved", calls)
	}
}
func TestCallbackRejectsForgedAndDuplicateResponses(t *testing.T) {
	results := make(chan callbackResult, 1)
	handler := callbackHandler("expected", results)
	for _, query := range []string{"state=wrong&code=secret", "state=expected&state=wrong&code=secret", "state=expected"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/callback?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("accepted invalid callback: %s", query)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/callback?state=expected&code=secret", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if (<-results).code != "secret" {
		t.Fatal("lost code")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/callback?state=expected&code=again", nil))
	if w.Code != 409 {
		t.Fatal("duplicate accepted")
	}
}
func TestTokenErrorsNeverExposeCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte("TOP_SECRET_REFRESH"))
	}))
	defer srv.Close()
	old := oauthTokenURL
	oauthTokenURL = srv.URL
	t.Cleanup(func() { oauthTokenURL = old })
	_, err := exchangeTokens(context.Background(), url.Values{}, session{})
	if err == nil || strings.Contains(err.Error(), "TOP_SECRET") {
		t.Fatal("unsafe error")
	}
}
func TestAccountAPIOnlyReturnsPublicFieldsAndPreservesOtherAccounts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := fakeAccounts(t, accountStore{Active: "one", Accounts: []account{{ID: "one", Label: "First", Session: session{AccessToken: "ACCESS_SECRET", RefreshToken: "REFRESH_SECRET", ExpiresAt: time.Now().Add(time.Hour).Unix()}}, {ID: "two", Label: "Second", Session: session{AccessToken: "OTHER_SECRET"}}}})
	w := httptest.NewRecorder()
	accountAPI(w, httptest.NewRequest("GET", "/accounts/api", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("unsafe response: %s", w.Body.String())
	}
	if err := removeAccount(*store, "one"); err != nil {
		t.Fatal(err)
	}
	if store.Active != "two" || len(store.Accounts) != 1 || store.Accounts[0].Session.AccessToken != "OTHER_SECRET" {
		t.Fatal("removal damaged remaining account")
	}
}
func TestDashboardBlocksCrossOriginAndRebinding(t *testing.T) {
	h := dashboardGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, test := range []struct {
		host, origin, header string
		want                 int
	}{{"127.0.0.1:8377", "http://evil.example", "1", 403}, {"evil.example:8377", "", "1", 403}, {"127.0.0.1:8377", "", "", 403}, {"127.0.0.1:8377", "http://127.0.0.1:8377", "1", 204}} {
		r := httptest.NewRequest("POST", "http://"+test.host+"/accounts/api", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Origin", test.origin)
		r.Header.Set("X-Dictate-Request", test.header)
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != test.want {
			t.Errorf("%+v got %d", test, w.Code)
		}
	}
}
func TestRefreshFailureDoesNotFallbackToCodex(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_DICTATE_AUTH_MODE", "")
	old := readAccounts
	readAccounts = func() (accountStore, error) { return accountStore{}, errors.New("keyring locked") }
	t.Cleanup(func() { readAccounts = old })
	_, _, err := loadAuth()
	if err == nil || !strings.Contains(err.Error(), "keyring locked") {
		t.Fatal("unexpected fallback")
	}
}
func TestPKCEAuthorization(t *testing.T) {
	u, _ := url.Parse(authorizationURL("state", "verifier"))
	q := u.Query()
	if q.Get("state") != "state" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != dictateRedirect {
		t.Fatal("invalid OAuth URL")
	}
	challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if err != nil || len(challenge) != 32 {
		t.Fatal("invalid PKCE challenge")
	}
}

func TestStandaloneTranscriptionUsesSelectedKeyringAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := fakeAccounts(t, accountStore{Active: "second", Accounts: []account{
		{ID: "first", Session: session{AccessToken: "first-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}},
		{ID: "second", Session: session{AccessToken: "second-token", AccountID: "second-account", ExpiresAt: time.Now().Add(time.Hour).Unix()}},
	}})
	p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer second-token" || r.Header.Get("ChatGPT-Account-ID") != "second-account" {
			t.Error("did not use selected keyring account")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"hello"}`))
	})
	p.standalone = true
	response := serve(p, uploadRequest(t, nil, []byte("audio")))
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if store.Active != "second" {
		t.Fatal("request changed active account")
	}
}

func TestPastedCallbackValidatesOriginStateAndSingleUse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan callbackResult, 1)
	a := &loginAttempt{state: "fresh-state", result: results, ctx: ctx}
	loginMu.Lock()
	old := activeAttempt
	activeAttempt = a
	loginMu.Unlock()
	t.Cleanup(func() { loginMu.Lock(); activeAttempt = old; loginMu.Unlock() })
	for _, raw := range []string{
		"https://evil.example/auth/callback?state=fresh-state&code=secret",
		"http://127.0.0.1:1455@evil.example/auth/callback?state=fresh-state&code=secret",
		"http://127.0.0.1:1455/auth/callback?state=old-state&code=secret",
		"http://127.0.0.1:1455/auth/callback?state=fresh-state&code=a&code=b",
		"http://127.0.0.1:1455/auth/callback?state=fresh-state&code=secret#fragment",
	} {
		if err := submitCallback(raw); err == nil {
			t.Errorf("accepted invalid callback: %s", raw)
		}
	}
	valid := "http://127.0.0.1:1455/auth/callback?state=fresh-state&code=AUTH_CODE_SECRET"
	if err := submitCallback(valid); err != nil {
		t.Fatal(err)
	}
	if (<-results).code != "AUTH_CODE_SECRET" {
		t.Fatal("lost code")
	}
	if err := submitCallback(valid); !errors.Is(err, errLoginConsumed) {
		t.Fatal("replayed manual callback accepted")
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest("GET", valid, nil))
	if w.Code != 409 {
		t.Fatal("browser callback replay accepted")
	}
}
func TestCancelledCallbackCannotBeReused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &loginAttempt{state: "old-state", result: make(chan callbackResult, 1), ctx: ctx}
	if err := a.submit(url.Values{"state": {"old-state"}, "code": {"secret"}}); err == nil {
		t.Fatal("cancelled callback accepted")
	}
}

func TestDashboardAllowsLocalRecordedAudioPlayback(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1:8377/accounts", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	dashboardGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w, r)
	policy := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "media-src 'self' blob:;") {
		t.Fatalf("recorded blob audio blocked: %s", policy)
	}
	if !strings.Contains(policy, "default-src 'self';") || !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Fatal("unrelated protections lost")
	}
}
