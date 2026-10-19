package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Experimental Codex-compatible login, not a registered Dictate OAuth client.
const dictateClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
const dictateRedirect = "http://127.0.0.1:1455/auth/callback"

var oauthTokenURL = "https://auth.openai.com/oauth/token"
var oauthHTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var sessionMu sync.Mutex
var errNoSession = errors.New("no Dictate keyring session")

type session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	ExpiresAt    int64  `json:"expires_at"`
}

// No credential values appear in argv, errors or logs. secret-tool delegates
// persistence and at-rest protection to the desktop Secret Service.
func keyring(action string, input []byte) ([]byte, error) {
	if os.Getenv("CODEX_DICTATE_CREDENTIAL_STORE") == "file" {
		return fileCredentials(action, input)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{action}
	if action == "store" {
		args = append(args, "--label=Codex Dictate ChatGPT (experimental)")
	}
	args = append(args, "application", "codex-dictate", "session", "chatgpt")
	cmd := exec.CommandContext(ctx, "secret-tool", args...)
	if input != nil {
		cmd.Stdin = strings.NewReader(string(input))
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if action == "lookup" && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 && len(exitErr.Stderr) == 0 {
			return nil, errNoSession
		}
		return nil, fmt.Errorf("credential store unavailable or session missing; unlock your desktop keyring and run codex-transcribe auth login")
	}
	return out, nil
}

type account struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Session session `json:"session"`
}
type accountStore struct {
	Active   string    `json:"active"`
	Accounts []account `json:"accounts"`
}

var readAccounts = func() (accountStore, error) {
	b, err := keyring("lookup", nil)
	if err != nil {
		// secret-tool exit 1 with no output means no matching item; all other
		// failures must remain visible instead of overwriting an inaccessible store.
		if errors.Is(err, errNoSession) {
			return accountStore{Accounts: []account{}}, nil
		}
		return accountStore{}, err
	}
	var a accountStore
	if json.Unmarshal(b, &a) != nil {
		return a, errors.New("invalid Dictate credential record")
	}
	if a.Accounts == nil {
		a.Accounts = []account{}
	}
	return a, nil
}
var saveAccounts = func(a accountStore) error {
	if len(a.Accounts) == 0 {
		_, err := keyring("clear", nil)
		return err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = keyring("store", b)
	return err
}
var readSession = func() (session, error) {
	a, err := readAccounts()
	if err != nil {
		return session{}, err
	}
	for _, v := range a.Accounts {
		if v.ID == a.Active {
			return v.Session, nil
		}
	}
	return session{}, errors.New("no active Dictate account; open /accounts to sign in")
}
var writeSession = func(s session) error {
	a, err := readAccounts()
	if err != nil {
		return err
	}
	for i := range a.Accounts {
		if a.Accounts[i].ID == a.Active {
			a.Accounts[i].Session = s
			return saveAccounts(a)
		}
	}
	return errors.New("active account no longer exists")
}

func addAccount(s session) error {
	a, err := readAccounts()
	if err != nil {
		return err
	}
	label := fmt.Sprintf("ChatGPT account %d", len(a.Accounts)+1)
	for i := range a.Accounts {
		if s.AccountID != "" && a.Accounts[i].Session.AccountID == s.AccountID {
			a.Accounts[i].Session = s
			a.Active = a.Accounts[i].ID
			return saveAccounts(a)
		}
	}
	id, err := randomCode()
	if err != nil {
		return err
	}
	a.Accounts = append(a.Accounts, account{ID: id, Label: label, Session: s})
	a.Active = id
	return saveAccounts(a)
}

// Serialize across goroutines AND processes before reading rotating refresh tokens.
func sessionLock() (func(), error) {
	sessionMu.Lock()
	dir, err := os.UserConfigDir()
	if err == nil {
		dir = filepath.Join(dir, "codex-dictate")
		err = os.MkdirAll(dir, 0700)
	}
	if err != nil {
		sessionMu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "session.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		sessionMu.Unlock()
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (err != syscall.EWOULDBLOCK && err != syscall.EAGAIN) || time.Now().After(deadline) {
			f.Close()
			sessionMu.Unlock()
			return nil, errors.New("Dictate session busy; try again")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close(); sessionMu.Unlock() }, nil
}

func loadAuth() (string, string, error) {
	unlock, err := sessionLock()
	if err != nil {
		return "", "", err
	}
	defer unlock()
	s, err := readSession()
	if err != nil {
		return "", "", err
	}
	if s.ExpiresAt <= time.Now().Add(time.Minute).Unix() {
		if s.RefreshToken == "" {
			return "", "", errors.New("session cannot renew; run auth login")
		}
		next, err := exchangeTokens(context.Background(), url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}, "client_id": {dictateClientID}}, s)
		if err != nil {
			return "", "", err
		}
		if err = writeSession(next); err != nil {
			return "", "", err
		}
		s = next
	}
	return s.AccessToken, s.AccountID, nil
}

func exchangeTokens(ctx context.Context, form url.Values, previous session) (session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return session{}, errors.New("OpenAI login connection failed; try again")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return session{}, fmt.Errorf("OpenAI token exchange returned HTTP %d; run auth login if your session expired", resp.StatusCode)
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t) != nil || t.AccessToken == "" {
		return session{}, errors.New("invalid OpenAI token response")
	}
	// These JWT claims are metadata only, never identity/authorization validation.
	// The token is received directly from OpenAI over TLS; the upstream validates it.
	var claims struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	parts := strings.Split(t.AccessToken, ".")
	if len(parts) == 3 {
		if b, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
			json.Unmarshal(b, &claims)
		}
	}
	s := session{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, AccountID: claims.Auth.AccountID, ExpiresAt: claims.Exp}
	if t.ExpiresIn > 0 {
		s.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).Unix()
	}
	if s.ExpiresAt <= time.Now().Unix() {
		return session{}, errors.New("token expiry missing or expired")
	}
	if s.RefreshToken == "" {
		s.RefreshToken = previous.RefreshToken
	}
	if s.AccountID == "" {
		s.AccountID = previous.AccountID
	}
	return s, nil
}

func randomCode() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func authorizationURL(state, verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {dictateClientID}, "redirect_uri": {dictateRedirect}, "scope": {"openid profile email offline_access"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}, "originator": {"codex_cli_rs"}}
	return "https://auth.openai.com/oauth/authorize?" + q.Encode()
}

type callbackResult struct {
	code string
	err  error
}

var errLoginConsumed = errors.New("Callback already received; wait for sign-in to complete")

type loginAttempt struct {
	state  string
	link   string
	result chan<- callbackResult
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
	server *http.Server
}

func (a *loginAttempt) submit(q url.Values) error {
	if a.ctx != nil && a.ctx.Err() != nil {
		return errors.New("This sign-in attempt has expired; start a new one")
	}
	if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
		return errors.New("Callback does not match this sign-in attempt; use the current sign-in link")
	}
	res := callbackResult{code: q.Get("code")}
	if len(q["error"]) > 1 || len(q["code"]) > 1 {
		return errors.New("Invalid callback")
	}
	if q.Get("error") != "" {
		res.err = errors.New("ChatGPT sign-in was declined or failed")
	}
	if (res.code == "" && res.err == nil) || (res.code != "" && res.err != nil) {
		return errors.New("Callback contains no valid sign-in result")
	}
	accepted := false
	a.once.Do(func() { a.result <- res; accepted = true })
	if !accepted {
		return errLoginConsumed
	}
	return nil
}
func (a *loginAttempt) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err == nil {
			err = a.submit(query)
		}
		if err != nil {
			status := 400
			if errors.Is(err, errLoginConsumed) {
				status = 409
			}
			http.Error(w, "Callback rejected. Return to the dashboard and check the active sign-in attempt.", status)
			return
		}
		fmt.Fprintln(w, "Callback received. Return to the Dictate dashboard to check sign-in status. You can close this tab.")
	})
}
func callbackHandler(state string, result chan<- callbackResult) http.Handler {
	return (&loginAttempt{state: state, result: result}).handler()
}

var loginMu sync.Mutex
var loginPending bool
var loginMessage string
var activeAttempt *loginAttempt

// A pending flow belongs to this local server, not a particular browser tab.
func pendingLogin() (pending bool, message, link, id string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	pending, message = loginPending, loginMessage
	if activeAttempt != nil {
		link, id = activeAttempt.link, activeAttempt.state
	}
	return
}
func cancelLogin(id string) error {
	loginMu.Lock()
	defer loginMu.Unlock()
	if activeAttempt == nil || subtle.ConstantTimeCompare([]byte(id), []byte(activeAttempt.state)) != 1 {
		return errors.New("The sign-in attempt has changed; refresh the dashboard")
	}
	activeAttempt.cancel()
	activeAttempt.server.Close()
	activeAttempt = nil
	loginPending = false
	loginMessage = "Sign-in cancelled. You can start again in any browser."
	return nil
}
func submitCallback(raw string) error {
	// Parse locally; never fetch a pasted URL or send its code to another host.
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || (u.Host != "127.0.0.1:1455" && u.Host != "localhost:1455") || u.Path != "/auth/callback" || u.User != nil || u.Fragment != "" {
		return errors.New("Paste the full callback URL: http://127.0.0.1:1455/auth/callback?...")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("Invalid callback format")
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	if activeAttempt == nil {
		return errors.New("No active sign-in attempt; create a new sign-in link")
	}
	return activeAttempt.submit(q)
}
func beginLogin() (string, <-chan error, error) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if loginPending {
		return "", nil, errors.New("A sign-in is pending. Copy its link or cancel it from the dashboard.")
	}
	if _, err := keyringProbe(); err != nil {
		return "", nil, err
	}
	state, err := randomCode()
	if err != nil {
		return "", nil, err
	}
	verifier, err := randomCode()
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:1455")
	if err != nil {
		return "", nil, errors.New("Sign-in port 1455 is busy; finish another Codex sign-in first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	result := make(chan callbackResult, 1)
	attempt := &loginAttempt{state: state, link: authorizationURL(state, verifier), result: result, ctx: ctx, cancel: cancel}
	server := &http.Server{Handler: attempt.handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	attempt.server = server
	activeAttempt = attempt
	loginPending = true
	loginMessage = "Waiting for browser sign-in. You can open the link in another browser."
	done := make(chan error, 1)
	go server.Serve(listener)
	go func() {
		err := func() error {
			defer cancel()
			defer server.Close()
			select {
			case <-ctx.Done():
				return errors.New("Sign-in expired. Create a new link to try again.")
			case res := <-result:
				if res.err != nil {
					return res.err
				}
				unlock, err := sessionLock()
				if err != nil {
					return err
				}
				defer unlock()
				if ctx.Err() != nil {
					return errors.New("Sign-in cancelled")
				}
				s, err := exchangeTokens(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {res.code}, "redirect_uri": {dictateRedirect}, "client_id": {dictateClientID}, "code_verifier": {verifier}}, session{})
				if err != nil {
					return err
				}
				if s.RefreshToken == "" {
					return errors.New("OpenAI returned no renewable session; account not saved")
				}
				loginMu.Lock()
				defer loginMu.Unlock()
				if ctx.Err() != nil || activeAttempt != attempt {
					return errors.New("Sign-in cancelled; account not saved")
				}
				return addAccount(s)
			}
		}()
		loginMu.Lock()
		if activeAttempt == attempt {
			activeAttempt = nil
			loginPending = false
			loginMessage = "Account saved in the OS keyring."
			if err != nil {
				loginMessage = err.Error()
			}
		}
		loginMu.Unlock()
		done <- err
	}()
	return attempt.link, done, nil
}
func login() error {
	link, done, err := beginLogin()
	if err != nil {
		return err
	}
	fmt.Println("Experimental login using the Codex public OAuth client. Complete sign-in on the OpenAI browser page.")
	if err := exec.Command("xdg-open", link).Run(); err != nil {
		fmt.Println("Open:", link)
	} else {
		fmt.Println("Browser opened; waiting for login...")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		return errors.New("login cancelled")
	case err := <-done:
		if err != nil {
			return err
		}
		fmt.Println("Signed in. Credentials saved in OS keyring.")
		return nil
	}
}

func keyringProbe() ([]byte, error) {
	if os.Getenv("CODEX_DICTATE_CREDENTIAL_STORE") == "file" {
		path, err := credentialFilePath()
		if err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".credential-probe-*")
		if err != nil {
			return nil, errors.New("credential directory is not writable")
		}
		name := f.Name()
		closeErr := f.Close()
		removeErr := os.Remove(name)
		return nil, errors.Join(closeErr, removeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "secret-tool", "store", "--label=Codex Dictate keyring check", "application", "codex-dictate", "session", "probe")
	cmd.Stdin = strings.NewReader("keyring-check")
	if err := cmd.Run(); err != nil {
		return nil, errors.New("desktop keyring unavailable; unlock it before auth login")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if err := exec.CommandContext(ctx2, "secret-tool", "clear", "application", "codex-dictate", "session", "probe").Run(); err != nil {
		return nil, errors.New("could not remove keyring probe")
	}
	return nil, nil
}

func authCommand(args []string) error {
	if len(args) != 2 || args[0] != "auth" {
		return errors.New("usage: codex-transcribe [auth login|status|logout]")
	}
	if args[1] == "login" {
		return login()
	}
	if args[1] != "status" && args[1] != "logout" {
		return errors.New("usage: codex-transcribe auth login|status|logout")
	}
	unlock, err := sessionLock()
	if err != nil {
		return err
	}
	defer unlock()
	switch args[1] {
	case "status":
		s, err := readSession()
		if err != nil {
			return err
		}
		state := "valid"
		if s.ExpiresAt <= time.Now().Unix() {
			state = "expired; will renew on next request"
		}
		fmt.Printf("Dictate keyring session: %s. Token expiry: %s\n", state, time.Unix(s.ExpiresAt, 0).Format(time.RFC3339))
		if legacyAuthMode {
			fmt.Println("Warning: proxy is configured for legacy Codex cache instead of this session.")
		}
	case "logout":
		a, err := readAccounts()
		if err != nil {
			return err
		}
		if err = removeAccount(a, a.Active); err != nil {
			return err
		}
		fmt.Println("Local Dictate credentials cleared. This does not revoke the remote OpenAI session or log Codex out.")
	}
	return nil
}
