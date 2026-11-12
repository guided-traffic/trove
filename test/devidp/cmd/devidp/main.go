// Command devidp runs the sign-in provider of the UI development profile (package devidp). deploy/ui-dev/serve.sh
// starts it next to trove:
//
//	devidp -listen 127.0.0.1:19090 -issuer https://localhost:19090 -tls-cert server.crt -tls-key server.key \
//	       -client-id trove-ui-dev -client-secret-file idp-client-secret -client-base https://localhost:4200
//
// It answers over TLS only; the certificate is read again when its files change.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/guided-traffic/trove/test/devidp"
	"github.com/guided-traffic/trove/test/oidcstub"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devidp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("devidp", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:19090", "address to listen on (TLS); keep it on the loopback address")
	issuer := fs.String("issuer", "https://localhost:19090", "the issuer as the browser and trove reach it")
	certFile := fs.String("tls-cert", "", "PEM certificate file; read again when it changes")
	keyFile := fs.String("tls-key", "", "PEM key file")
	clientID := fs.String("client-id", "trove-ui-dev", "the client ID trove signs in with")
	clientBase := fs.String("client-base", "https://localhost:4200", "trove's base URL: redirect URIs must lie below it")
	secretFile := fs.String("client-secret-file", "", "file with the client secret trove signs in with")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *certFile == "" || *keyFile == "" || *secretFile == "" {
		return errors.New("-tls-cert, -tls-key and -client-secret-file are required")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("-listen %s: the provider signs anyone in without a password and listens on a loopback address only", *listen)
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		return err
	}
	provider, err := devidp.New(devidp.Config{
		Issuer: *issuer, ClientID: *clientID, ClientSecret: strings.TrimSpace(string(secret)), ClientBase: *clientBase,
	})
	if err != nil {
		return err
	}
	get, err := oidcstub.ReloadingKeyPair(*certFile, *keyFile)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           provider.Handler(),
		TLSConfig:         &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *listen)
	if err != nil {
		return err
	}
	slog.Info("devidp listening", "address", ln.Addr().String(), "issuer", *issuer)
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(ln, "", "") }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
