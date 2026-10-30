// Command npmstub runs the counting test upstream (package npmstub) as a process or container, and
// carries the two tools around it:
//
//	npmstub [serve] -listen :8443 -tls-cert f -tls-key f [-control-token-file f] [-public-url u]
//	npmstub serve -self-signed localhost,npmstub -ca-out ca.crt     (a throwaway CA, for local runs)
//	npmstub reference -out dir          write the reference project's package.json (perf/npm/reference)
//	npmstub record ...                  by hand: record trimmed documents and tarballs from a live registry
//
// The server answers over TLS only; without a certificate (or -self-signed) it refuses to start.
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
	"path/filepath"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
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
	case "record":
		err = record(args)
	default:
		err = fmt.Errorf("unknown command %q (serve, reference, record)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "npmstub:", err)
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
	publicURL := fs.String("public-url", "", "base of the tarball addresses in documents (default: https:// and the request's Host)")
	tokenFile := fs.String("control-token-file", "", "file with the bearer token of the control API (/-/stub/); without it the API is off")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := npmstub.Config{PublicURL: *publicURL, Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
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
	stub, err := npmstub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg)
	if err != nil {
		return err
	}
	slog.Info("npmstub listening", "url", run.URL, "corpus", corpus.Default().String(), "control", cfg.ControlToken != "")

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
		ss, err := npmstub.NewSelfSigned(strings.Split(selfSigned, ",")...)
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
		get, err := npmstub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}

func reference(args []string) error {
	fs := flag.NewFlagSet("reference", flag.ContinueOnError)
	out := fs.String("out", "", "directory to write package.json to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("reference: -out is required")
	}
	ref := corpus.Default().Reference(corpus.ReferenceMinNames)
	return os.WriteFile(filepath.Join(*out, "package.json"), ref.PackageJSON(), 0o644) //nolint:gosec // a committed project file
}
