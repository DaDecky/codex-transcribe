package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileCredentialLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_DICTATE_CREDENTIAL_STORE", "file")
	// Headless login must not require a desktop Secret Service executable.
	t.Setenv("PATH", t.TempDir())
	if _, err := keyringProbe(); err != nil {
		t.Fatal(err)
	}
	a, err := readAccounts()
	if err != nil || len(a.Accounts) != 0 {
		t.Fatalf("empty store: %v", err)
	}
	if err := addAccount(session{AccessToken: "test-access", RefreshToken: "test-refresh", AccountID: "test-account"}); err != nil {
		t.Fatal(err)
	}
	s, err := readSession()
	if err != nil || s.RefreshToken != "test-refresh" {
		t.Fatalf("reload session failed: %v", err)
	}
	path, err := credentialFilePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("expected private credential file: %v", err)
	}
	s.RefreshToken = "rotated"
	if err := writeSession(s); err != nil {
		t.Fatal(err)
	}
	s, err = readSession()
	if err != nil || s.RefreshToken != "rotated" {
		t.Fatalf("refresh not persisted: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAccounts(); err == nil {
		t.Fatal("accepted world-readable credentials")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveAccounts(accountStore{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("logout did not remove credentials")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("temporary credential files remain")
	}
}

func TestMalformedFileCredentialsAreNotOverwritten(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_DICTATE_CREDENTIAL_STORE", "file")
	if _, err := keyring("store", []byte("invalid-json")); err != nil {
		t.Fatal(err)
	}
	if err := addAccount(session{AccessToken: "replacement"}); err == nil {
		t.Fatal("overwrote inaccessible credential store")
	}
	b, err := keyring("lookup", nil)
	if err != nil || string(b) != "invalid-json" {
		t.Fatal("existing record was changed")
	}
}
