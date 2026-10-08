package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

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
	if apiKey == "" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("non-loopback listeners require CODEX_TRANSCRIBE_API_KEY; use a literal loopback IP for unauthenticated local access")
	}
	return nil
}

func run() error {
	authFile, err := defaultAuthFile()
	if err != nil {
		return fmt.Errorf("resolve Codex auth file: %w", err)
	}
	listen := flag.String("listen", "127.0.0.1:8378", "HTTP listen address")
	auth := flag.String("auth-file", authFile, "Codex ChatGPT auth.json path (read per request)")
	timeout := flag.Duration("timeout", 90*time.Second, "upstream transcription timeout")
	maxUpload := flag.Int64("max-upload-mib", 25, "maximum entire multipart request size in MiB")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments; run codex-transcribe -help")
	}
	if *timeout <= 0 || *timeout > time.Hour {
		return errors.New("timeout must be greater than zero and at most 1h")
	}
	if *maxUpload <= 0 || *maxUpload > 1024 {
		return errors.New("max-upload-mib must be between 1 and 1024")
	}
	apiKey := os.Getenv("CODEX_TRANSCRIBE_API_KEY")
	if err := validateListen(*listen, apiKey); err != nil {
		return err
	}
	client := &http.Client{
		Timeout: *timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	proxy := &proxy{
		authFile:    *auth,
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
