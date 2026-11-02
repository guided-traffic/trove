// Command oidcstub runs the GitLab-shaped OIDC issuer double (package oidcstub) as a process or
// container:
//
//	oidcstub [serve] -listen :8443 -plain-listen :8080 -control-listen :8444 -tls-cert f -tls-key f
//	                 -public-url https://oidcstub.e2e.svc [-plain-public-url http://oidcstub.e2e.svc:8080]
//	oidcstub serve -self-signed localhost,oidcstub -ca-out ca.crt     (a throwaway CA, for local runs)
//
// The issuer endpoints answer over TLS only (the plain listener exists to be the target of the
// redirect-out-of-TLS fault). The control API listens on its own address and needs the bearer
// token in the environment variable OIDCSTUB_CONTROL_TOKEN (or the file of -control-token-file);
// without a token the control listener is not started.
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

	"github.com/guided-traffic/trove/test/oidcstub"
)

// tokenEnv is the environment variable that carries the control token.
const tokenEnv = "OIDCSTUB_CONTROL_TOKEN" //nolint:gosec // the name of a variable, not a credential

func main() {
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != "serve" {
			fmt.Fprintf(os.Stderr, "oidcstub: unknown command %q (serve)\n", args[0])
			os.Exit(1)
		}
		args = args[1:]
	}
	if err := serve(args); err != nil {
		fmt.Fprintln(os.Stderr, "oidcstub:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "address of the issuer endpoints (TLS)")
	plainListen := fs.String("plain-listen", ":8080", "address of the plain-HTTP listener, the target of the redirect-out-of-TLS fault")
	controlListen := fs.String("control-listen", ":8444", "address of the control API (TLS); off without a control token")
	certFile := fs.String("tls-cert", "", "PEM certificate file; read again when it changes")
	keyFile := fs.String("tls-key", "", "PEM key file")
	selfSigned := fs.String("self-signed", "", "comma-separated host names and addresses: make a throwaway CA and certificate instead of -tls-cert")
	caOut := fs.String("ca-out", "", "with -self-signed: write the CA certificate here")
	publicURL := fs.String("public-url", "", "the issuer as clients reach it, for example https://oidcstub.e2e.svc (default: the listen address)")
	plainPublicURL := fs.String("plain-public-url", "", "the plain-HTTP base as clients reach it (default: the listen address)")
	audience := fs.String("audience", "", "default aud of minted tokens (default "+oidcstub.DefaultAudience+")")
	tokenFile := fs.String("control-token-file", "", "file with the control token, instead of "+tokenEnv)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := oidcstub.Config{
		PublicURL: *publicURL, PlainURL: *plainPublicURL, Audience: *audience,
		ControlToken: os.Getenv(tokenEnv), Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		cfg.ControlToken = strings.TrimSpace(string(raw))
	}
	if (*tokenFile != "" || os.Getenv(tokenEnv) != "") && cfg.ControlToken == "" {
		return errors.New("the control token is empty")
	}
	tlsCfg, err := tlsConfig(*certFile, *keyFile, *selfSigned, *caOut)
	if err != nil {
		return err
	}
	stub, err := oidcstub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg, *plainListen)
	if err != nil {
		return err
	}
	closers := []func() error{run.Close}
	slog.Info("oidcstub listening", "url", run.URL, "plain", run.PlainURL, "issuer", stub.Issuer())
	if cfg.ControlToken != "" {
		ctl, err := stub.StartControl(*controlListen, tlsCfg)
		if err != nil {
			return err
		}
		closers = append(closers, ctl.Close)
		slog.Info("oidcstub control API listening", "url", ctl.URL)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	var errs error
	for _, c := range closers {
		errs = errors.Join(errs, c())
	}
	return errs
}

func tlsConfig(certFile, keyFile, selfSigned, caOut string) (*tls.Config, error) {
	switch {
	case selfSigned != "" && (certFile != "" || keyFile != ""):
		return nil, errors.New("-self-signed and -tls-cert/-tls-key are exclusive")
	case selfSigned != "":
		ss, err := oidcstub.NewSelfSigned(strings.Split(selfSigned, ",")...)
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
		get, err := oidcstub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}
