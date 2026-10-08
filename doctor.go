package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
)

// runDoctor only reads local credentials and briefly binds the configured port.
// It cannot establish whether a token is accepted by ChatGPT.
func runDoctor(authFile, listen, apiKey string, output io.Writer) error {
	fmt.Fprintln(output, "Offline doctor: no upstream requests, credential writes, or microphone access.")
	fmt.Fprintf(output, "Auth path: %s\n", authFile)
	failed := false
	if _, err := loadCredentials(authFile); err != nil {
		failed = true
		switch {
		case errors.Is(err, os.ErrNotExist):
			fmt.Fprintln(output, "FAIL auth: file does not exist; run codex login or select the correct path with -auth-file.")
		case errors.Is(err, os.ErrPermission):
			fmt.Fprintln(output, "FAIL auth: file is not readable; check file and directory permissions for this user.")
		default:
			// Do not print parsing errors: they may contain credential content.
			fmt.Fprintln(output, "FAIL auth: cannot read valid credentials with a nonempty access token; run codex login and check -auth-file and file permissions.")
		}
	} else {
		fmt.Fprintln(output, "PASS auth: file is readable and an access token is present.")
	}
	fmt.Fprintln(output, "Login validity is not checked offline; token presence does not mean the login is valid or unexpired.")
	if err := validateListen(listen, apiKey); err != nil {
		failed = true
		fmt.Fprintf(output, "FAIL listen: %v; check -listen and CODEX_TRANSCRIBE_API_KEY.\n", err)
	} else {
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			failed = true
			fmt.Fprintf(output, "FAIL listen: cannot bind %s; stop any process using this port or choose another -listen address.\n", listen)
		} else {
			err = listener.Close()
			if err != nil {
				failed = true
				fmt.Fprintln(output, "FAIL listen: could not release the probe listener.")
			} else {
				fmt.Fprintf(output, "PASS listen: %s can be bound (probe released; availability can change).\n", listen)
			}
		}
	}
	if failed {
		return errors.New("doctor found failing checks")
	}
	return nil
}
