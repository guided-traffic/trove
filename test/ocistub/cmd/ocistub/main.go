// Command ocistub runs the counting OCI test upstream (package ocistub) as a process or container:
//
//	ocistub [serve] -listen :8443 -tls-cert f -tls-key f [-control-token-file f] [-public-url u] [-realm-url u] [-cdn-url u]
//	ocistub serve -self-signed localhost,127.0.0.1,::1 -ca-out ca.crt     (a throwaway CA, for local runs)
//
// The server answers over TLS only; without a certificate (or -self-signed) it refuses to start. The three URLs are the bases of
// the registry, the token realm and the blob CDN in the challenges and redirects; left out they are derived from the request.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/test/ocistub"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if err := serve(args); err != nil {
		fmt.Fprintln(os.Stderr, "ocistub:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "address to listen on (TLS)")
	certFile := fs.String("tls-cert", "", "PEM certificate file; read again when it changes")
	keyFile := fs.String("tls-key", "", "PEM key file")
	selfSigned := fs.String("self-signed", "", "comma-separated host names and addresses: make a throwaway CA and certificate instead of -tls-cert")
	caOut := fs.String("ca-out", "", "with -self-signed: write the CA certificate here")
	publicURL := fs.String("public-url", "", "base of the registry host (default: derived from the request's Host)")
	realmURL := fs.String("realm-url", "", "base of the token realm host (default: derived)")
	cdnURL := fs.String("cdn-url", "", "base of the blob CDN host (default: derived)")
	service := fs.String("service", "", "service name of the token handshake (default ocistub)")
	pageSize := fs.Int("page-size", 0, "cap of a page of the tags list and the catalog (default 700)")
	tokenFile := fs.String("control-token-file", "", "file with the bearer token of the control API (/-/stub/); without it the API is off")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg := ocistub.Config{
		PublicURL: *publicURL, RealmURL: *realmURL, CDNURL: *cdnURL, Service: *service, PageSize: *pageSize,
		Logger: logger,
	}
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		cfg.ControlToken = strings.TrimSpace(string(raw))
		if cfg.ControlToken == "" {
			return errors.New("the control token file is empty")
		}
	}
	tlsCfg, err := tlsConfig(*certFile, *keyFile, *selfSigned, *caOut)
	if err != nil {
		return err
	}
	stub, err := ocistub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg)
	if err != nil {
		return err
	}
	logger.Info("ocistub listening", "addr", run.Addr, "public_url", cfg.PublicURL, "realm_url", cfg.RealmURL, "cdn_url", cfg.CDNURL, "repositories", len(stub.Repositories()), "control", cfg.ControlToken != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return run.Close()
}

func tlsConfig(certFile, keyFile, selfSigned, caOut string) (*tls.Config, error) {
	switch {
	case selfSigned != "" && (certFile != "" || keyFile != ""):
		return nil, errors.New("-self-signed and -tls-cert/-tls-key are exclusive")
	case selfSigned != "":
		ss, err := ocistub.NewSelfSigned(strings.Split(selfSigned, ",")...)
		if err != nil {
			return nil, err
		}
		if caOut != "" {
			// Through a rename, so a file left by an earlier run under another user is replaced.
			tmp := caOut + ".tmp"
			if err := os.WriteFile(tmp, ss.CAPEM, 0o644); err != nil { //nolint:gosec // a CA certificate is public
				return nil, err
			}
			if err := os.Rename(tmp, caOut); err != nil {
				return nil, err
			}
		}
		return ss.ServerConfig(), nil
	case certFile != "" && keyFile != "":
		get, err := ocistub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}
