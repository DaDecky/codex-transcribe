package main

import (
	"errors"
	"os"
	"path/filepath"
)

// File storage is an explicit opt-in for headless containers, never a fallback
// from an unavailable desktop keyring. Callers hold sessionLock for account I/O.
func credentialFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "codex-dictate")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func fileCredentials(action string, input []byte) ([]byte, error) {
	path, err := credentialFilePath()
	if err != nil {
		return nil, errors.New("credential directory unavailable")
	}
	switch action {
	case "lookup":
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errNoSession
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("credential file must be a regular file with owner-only permissions")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("cannot read credential file")
		}
		return b, nil
	case "clear":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("cannot remove credential file")
		}
		return nil, nil
	case "store":
		f, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
		if err != nil {
			return nil, errors.New("cannot create credential file")
		}
		defer os.Remove(f.Name())
		_, err = f.Write(input)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(f.Name(), path)
		}
		if err != nil {
			return nil, errors.New("cannot save credential file")
		}
		return nil, nil
	default:
		return nil, errors.New("unsupported credential operation")
	}
}
