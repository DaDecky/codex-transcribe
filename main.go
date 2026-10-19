package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var version = "dev"
var legacyAuthMode bool

func defaultAuthFile() (string, error) {
	if path := os.Getenv("CODEX_TRANSCRIBE_AUTH_FILE"); path != "" {
		return path, nil
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func validateListen(addr, apiKey string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	origin, err := publicOrigin()
	if err != nil {
		return err
	}
	if apiKey == "" && origin == "" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("non-loopback listeners require CODEX_TRANSCRIBE_API_KEY; use a literal loopback IP for unauthenticated local access")
	}
	return nil
}

func run() error {
	return runCLI(os.Args[1:], os.Stdout, os.Stderr)
}

func runCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "auth" {
		return authCommand(args)
	}
	doctor := len(args) > 0 && args[0] == "doctor"
	if doctor {
		args = args[1:]
	}
	flags := flag.NewFlagSet("codex-transcribe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:8378", "HTTP listen address")
	authMode := flags.String("auth-mode", "keyring", "credential source: keyring (standalone) or codex (legacy auth file)")
	auth := flags.String("auth-file", "", "Codex ChatGPT auth.json path (default: CODEX_TRANSCRIBE_AUTH_FILE, CODEX_HOME/auth.json, or ~/.codex/auth.json; read per request)")
	timeout := flags.Duration("timeout", 90*time.Second, "upstream transcription timeout")
	maxUpload := flags.Int64("max-upload-mib", 25, "maximum entire multipart request size in MiB")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; run codex-transcribe -help")
	}
	if *showVersion {
		fmt.Fprintf(stdout, "codex-transcribe %s\n", version)
		return nil
	}
	if store := os.Getenv("CODEX_DICTATE_CREDENTIAL_STORE"); store != "" && store != "keyring" && store != "file" {
		return errors.New("CODEX_DICTATE_CREDENTIAL_STORE must be keyring or file")
	}
	explicitAuth := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "auth-file" {
			explicitAuth = true
		}
	})
	if !explicitAuth {
		var err error
		*auth, err = defaultAuthFile()
		if err != nil {
			return fmt.Errorf("resolve Codex auth file: %w", err)
		}
	}
	if *timeout <= 0 || *timeout > time.Hour {
		return errors.New("timeout must be greater than zero and at most 1h")
	}
	if *maxUpload <= 0 || *maxUpload > 1024 {
		return errors.New("max-upload-mib must be between 1 and 1024")
	}
	apiKey := os.Getenv("CODEX_TRANSCRIBE_API_KEY")
	if *authMode != "keyring" && *authMode != "codex" {
		return errors.New("auth-mode must be keyring or codex")
	}
	legacyAuthMode = *authMode == "codex"
	if doctor && !legacyAuthMode {
		return runKeyringDoctor(*listen, apiKey, stdout)
	}
	if doctor {
		return runDoctor(*auth, *listen, apiKey, stdout)
	}
	if err := validateListen(*listen, apiKey); err != nil {
		return err
	}
	unlock, err := sessionLock()
	if err != nil {
		return errors.New("cannot initialize dashboard configuration")
	}
	c, err := readDashboardConfig()
	if err == nil {
		err = initializeDashboardPassword(c)
	}
	unlock()
	if err != nil {
		return fmt.Errorf("cannot initialize dashboard configuration: %w", err)
	}
	client := &http.Client{
		Timeout: *timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	proxy := &proxy{
		authFile:    *auth,
		standalone:  !legacyAuthMode,
		managedKeys: true,
		apiKey:      apiKey,
		maxUpload:   *maxUpload << 20,
		upstreamURL: "https://chatgpt.com/backend-api/transcribe",
		userAgent:   os.Getenv("CODEX_TRANSCRIBE_USER_AGENT"),
		client:      client,
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           proxy.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       *timeout + 10*time.Second,
		WriteTimeout:      *timeout + 15*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	log.Printf("listening on http://%s (ChatGPT dictation; audio is uploaded to OpenAI)", listener.Addr())
	err = server.Serve(listener)
	stop()
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
