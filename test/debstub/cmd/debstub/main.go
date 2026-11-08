// Command debstub runs the counting Debian archive test upstream (package debstub) as a process or container:
//
//	debstub [serve] -listen :8443 -tls-cert f -tls-key f [-control-token-file f] [-bulk-paragraphs n]
//	debstub serve -self-signed localhost,127.0.0.1,::1 -ca-out ca.crt     (a throwaway CA, for local runs)
//	debstub keyring [names]                                                (the public test keys, armored, on stdout)
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
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/test/debstub"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && (args[0] == "serve" || args[0] == "keyring") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "keyring":
		err = keyring(args)
	default:
		err = serve(args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "debstub:", err)
		os.Exit(1)
	}
}

// keyring prints the public certificates of the named test keys (default: all) as one armored block.
func keyring(names []string) error {
	stub, err := debstub.New(debstub.Config{})
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = debstub.KeyNames()
	}
	ring, err := stub.Keyring(names...)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(ring)
	return err
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "address to listen on (TLS)")
	certFile := fs.String("tls-cert", "", "PEM certificate file; read again when it changes")
	keyFile := fs.String("tls-key", "", "PEM key file")
	selfSigned := fs.String("self-signed", "", "comma-separated host names and addresses: make a throwaway CA and certificate instead of -tls-cert")
	caOut := fs.String("ca-out", "", "with -self-signed: write the CA certificate here")
	bulk := fs.Int("bulk-paragraphs", 0, "paragraphs of the large index in the suite bulk (default 70000)")
	tokenFile := fs.String("control-token-file", "", "file with the bearer token of the control API (/-/stub/); without it the API is off")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg := debstub.Config{BulkParagraphs: *bulk, Logger: logger}
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
	stub, err := debstub.New(cfg)
	if err != nil {
		return err
	}
	run, err := stub.Start(*listen, tlsCfg)
	if err != nil {
		return err
	}
	logger.Info("debstub listening", "addr", run.Addr, "archives", len(stub.Archives()), "control", cfg.ControlToken != "")

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
		ss, err := debstub.NewSelfSigned(strings.Split(selfSigned, ",")...)
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
		get, err := debstub.ReloadingKeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12}, nil
	}
	return nil, errors.New("TLS only: give -tls-cert and -tls-key (or -self-signed for a throwaway certificate)")
}
