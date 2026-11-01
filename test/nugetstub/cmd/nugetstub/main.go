// Command nugetstub runs the counting NuGet test upstream (package nugetstub) as a process or
// container, and writes the reference solution:
//
//	nugetstub [serve] -listen :8443 -tls-cert f -tls-key f [-control-token-file f] [-public-url u] [-content-url u]
//	nugetstub serve -self-signed localhost,nugetstub -ca-out ca.crt     (a throwaway CA, for local runs)
//	nugetstub reference -out dir                write the reference solution (perf/nuget/reference)
//	nugetstub reference -check dir              fail when the files in dir differ from a regeneration
//
// The server answers over TLS only; without a certificate (or -self-signed) it refuses to start.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/test/nugetstub"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "reference":
		err = reference(args)
	default:
		err = fmt.Errorf("unknown command %q (serve, reference)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nugetstub:", err)
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
	publicURL := fs.String("public-url", "", "base of the API host in documents (default: derived from the request's Host)")
	contentURL := fs.String("content-url", "", "base of the content host: flat container and search (default: the API host)")
	tokenFile := fs.String("control-token-file", "", "file with the bearer token of the control API (/-/stub/); without it the API is off")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := nugetstub.Config{PublicURL: *publicURL, ContentURL: *contentURL, Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
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
	stub, err := nugetstub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg)
	if err != nil {
		return err
	}
	slog.Info("nugetstub listening", "url", run.URL, "packages", nugetstub.Default().Len(), "control", cfg.ControlToken != "")

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
		ss, err := nugetstub.NewSelfSigned(strings.Split(selfSigned, ",")...)
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
		get, err := nugetstub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}

func reference(args []string) error {
	fs := flag.NewFlagSet("reference", flag.ContinueOnError)
	out := fs.String("out", "", "directory to write the reference solution to")
	check := fs.String("check", "", "directory whose files must equal a regeneration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*out == "") == (*check == "") {
		return errors.New("reference: give exactly one of -out and -check")
	}
	files := nugetstub.ReferenceFiles(nugetstub.Default(), nugetstub.ReferenceMinPackages)
	if *check != "" {
		var diff []string
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(*check, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(got, want) {
				diff = append(diff, name)
			}
		}
		if len(diff) > 0 {
			return fmt.Errorf("reference: differs from a regeneration: %s", strings.Join(diff, ", "))
		}
		return nil
	}
	for name, data := range files {
		path := filepath.Join(*out, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // committed project files
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // committed project files
			return err
		}
	}
	return nil
}
