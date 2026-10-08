package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doctorAuthFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents string
	}{
		{"malformed", `{"tokens":"private-file-content"`},
		{"missing token", `{"tokens":{"account_id":"private-account"}}`},
		{"empty token", `{"tokens":{"access_token":"  "}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runDoctor(doctorAuthFile(t, tc.contents), "127.0.0.1:0", "", &output)
			if err == nil || !strings.Contains(output.String(), "FAIL auth:") {
				t.Fatalf("expected auth failure: %v, %s", err, &output)
			}
			if strings.Contains(output.String(), "private-file-content") || strings.Contains(output.String(), "private-account") {
				t.Fatal("doctor exposed credential contents")
			}
		})
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.json"), t.TempDir()} {
		var output bytes.Buffer
		if err := runDoctor(path, "127.0.0.1:0", "", &output); err == nil || !strings.Contains(output.String(), "FAIL auth:") {
			t.Fatalf("expected unreadable auth failure: %v, %s", err, &output)
		}
	}
}

func TestDoctorSuccessAndOccupiedPort(t *testing.T) {
	path := doctorAuthFile(t, `{"tokens":{"access_token":"private-token","account_id":"private-account"}}`)
	var output bytes.Buffer
	if err := runDoctor(path, "127.0.0.1:0", "", &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PASS auth:", "PASS listen:", "Login validity is not checked offline"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, &output)
		}
	}
	if strings.Contains(output.String(), "private-token") || strings.Contains(output.String(), "private-account") {
		t.Fatal("doctor exposed credentials")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	output.Reset()
	if err := runDoctor(path, listener.Addr().String(), "", &output); err == nil || !strings.Contains(output.String(), "FAIL listen:") {
		t.Fatalf("expected occupied port failure: %v, %s", err, &output)
	}
}

func TestDoctorListenValidation(t *testing.T) {
	path := doctorAuthFile(t, `{"tokens":{"access_token":"token"}}`)
	for _, listen := range []string{"localhost:8378", "0.0.0.0:8378", "not-an-address"} {
		var output bytes.Buffer
		if err := runDoctor(path, listen, "", &output); err == nil || !strings.Contains(output.String(), "FAIL listen:") {
			t.Fatalf("expected listen validation failure for %q: %v, %s", listen, err, &output)
		}
	}
}

func TestDoctorAuthPrecedence(t *testing.T) {
	t.Setenv("CODEX_TRANSCRIBE_API_KEY", "")
	home := t.TempDir()
	codexHome := t.TempDir()
	envAuth := doctorAuthFile(t, `{"tokens":{"access_token":"token"}}`)
	explicitAuth := doctorAuthFile(t, `{"tokens":{"access_token":"token"}}`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEX_TRANSCRIBE_AUTH_FILE", envAuth)
	for _, tc := range []struct {
		name     string
		env      string
		codex    string
		explicit string
		want     string
	}{
		{"explicit", envAuth, codexHome, explicitAuth, explicitAuth},
		{"environment", envAuth, codexHome, "", envAuth},
		{"codex home", "", codexHome, "", filepath.Join(codexHome, "auth.json")},
		{"user home", "", "", "", filepath.Join(home, ".codex", "auth.json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_TRANSCRIBE_AUTH_FILE", tc.env)
			t.Setenv("CODEX_HOME", tc.codex)
			if err := os.MkdirAll(filepath.Dir(tc.want), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tc.want, []byte(`{"tokens":{"access_token":"token"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"doctor", "-listen", "127.0.0.1:0"}
			if tc.explicit != "" {
				args = append(args, "-auth-file", tc.explicit)
			}
			var output, stderr bytes.Buffer
			if err := runCLI(args, &output, &stderr); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Auth path: "+tc.want+"\n") {
				t.Fatalf("wrong auth path: %s", &output)
			}
		})
	}
}

func TestVersionBeforeAuthResolution(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_TRANSCRIBE_AUTH_FILE", "")
	var output, stderr bytes.Buffer
	if err := runCLI([]string{"-version"}, &output, &stderr); err != nil {
		t.Fatal(err)
	}
	if output.String() != "codex-transcribe "+version+"\n" {
		t.Fatalf("unexpected version output %q", output.String())
	}
}
