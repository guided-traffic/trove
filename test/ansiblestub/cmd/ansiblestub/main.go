// Command ansiblestub runs the counting Ansible Galaxy test upstream (package ansiblestub) as a process or container:
//
//	ansiblestub [serve] -listen :8443 -tls-cert f -tls-key f [-control-token-file f] [-fillers n]
//	            [-github-host h] [-codeload-host h] [-unlisted-host h] [-realm-host h] [-realm-path p]
//	ansiblestub serve -self-signed galaxy.localhost,github.localhost,... -ca-out ca.crt     (a throwaway CA, for local runs)
//
// The server answers over TLS only; without a certificate (or -self-signed) it refuses to start. The Galaxy host is every name
// that is none of the four others; the certificate has to carry all five.
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

	"github.com/guided-traffic/trove/test/ansiblestub"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if err := serve(args); err != nil {
		fmt.Fprintln(os.Stderr, "ansiblestub:", err)
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
	fillers := fs.Int("fillers", 0, "one-version collections to add to the index, to make it longer than a page")
	github := fs.String("github-host", "", "host name of the GitHub stand-in (default "+ansiblestub.DefaultGitHubHost+")")
	codeload := fs.String("codeload-host", "", "host name the GitHub stand-in redirects to (default "+ansiblestub.DefaultCodeloadHost+")")
	unlisted := fs.String("unlisted-host", "", "host name of the unlisted role host (default "+ansiblestub.DefaultUnlistedHost+")")
	realm := fs.String("realm-host", "", "host name of the single-sign-on realm (default "+ansiblestub.DefaultRealmHost+")")
	realmPath := fs.String("realm-path", "", "path of the realm's token endpoint (default "+ansiblestub.DefaultRealmPath+")")
	tokenFile := fs.String("control-token-file", "", "file with the bearer token of the control API (/-/stub/); without it the API is off")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg := ansiblestub.Config{
		Fillers: *fillers, GitHubHost: *github, CodeloadHost: *codeload, UnlistedHost: *unlisted, RealmHost: *realm, RealmPath: *realmPath,
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
	stub, err := ansiblestub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg)
	if err != nil {
		return err
	}
	logger.Info("ansiblestub listening", "addr", run.Addr, "control", cfg.ControlToken != "")

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
		ss, err := ansiblestub.NewSelfSigned(strings.Split(selfSigned, ",")...)
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
		get, err := ansiblestub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}
