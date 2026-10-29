package cluster

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// peerKind says whether a connection goes to a data node or to a Sentinel, so
// that AuthCredentialsFn can hand out the matching credential.
type peerKind int

const (
	peerData peerKind = iota
	peerSentinel
)

// peerAddr is the remote address that a connection of this package reports.
// valkey-go passes it to AuthCredentialsFn, which is how one function serves
// two credentials.
type peerAddr struct {
	net.Addr
	kind peerKind
}

type peerConn struct {
	net.Conn
	remote peerAddr
}

func (c peerConn) RemoteAddr() net.Addr { return c.remote }

// credentials reads the username and password files of data nodes and
// Sentinels. It reads on every call: valkey-go calls AuthCredentialsFn for
// every connection attempt, so a rotated Secret takes effect without a restart
// (R-ARCH-031).
type credentials struct {
	dataUser, dataPass         string // file names
	sentinelUser, sentinelPass string
}

func newCredentials(cfg config.Valkey) credentials {
	return credentials{
		dataUser: cfg.UsernameFile, dataPass: cfg.PasswordFile,
		sentinelUser: cfg.Sentinel.UsernameFile, sentinelPass: cfg.Sentinel.PasswordFile,
	}
}

// read returns the username and password for kind. An empty file name gives an
// empty value.
func (c credentials) read(kind peerKind) (user, pass string, err error) {
	uf, pf := c.dataUser, c.dataPass
	if kind == peerSentinel {
		uf, pf = c.sentinelUser, c.sentinelPass
	}
	if user, err = readSecretFile(uf); err != nil {
		return "", "", err
	}
	if pass, err = readSecretFile(pf); err != nil {
		return "", "", err
	}
	return user, pass, nil
}

// fn is the valkey.ClientOption.AuthCredentialsFn.
func (c credentials) fn(ctx valkey.AuthCredentialsContext) (valkey.AuthCredentials, error) {
	kind := peerData
	if pa, ok := ctx.Address.(peerAddr); ok {
		kind = pa.kind
	}
	user, pass, err := c.read(kind)
	if err != nil {
		return valkey.AuthCredentials{}, err
	}
	return valkey.AuthCredentials{Username: user, Password: pass}, nil
}

// readSecretFile returns the trimmed content of a credential file. The error
// names the file, never its content.
func readSecretFile(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	b, err := os.ReadFile(name) //nolint:gosec // operator-configured credential path
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// dialFn builds the DialCtxFn: TLS as configured for the kind of peer, and the
// remote address tagged with that kind.
func dialFn(sentinelResolver *net.Resolver) func(context.Context, string, *outbound.Dialer, *tls.Config) (net.Conn, error) {
	return func(ctx context.Context, dst string, d *outbound.Dialer, cfg *tls.Config) (net.Conn, error) {
		kind := peerData
		if d.Resolver != nil && d.Resolver == sentinelResolver {
			kind = peerSentinel
		}
		var (
			conn net.Conn
			err  error
		)
		if cfg != nil {
			td := tls.Dialer{NetDialer: d, Config: cfg}
			conn, err = td.DialContext(ctx, "tcp", dst)
		} else {
			conn, err = d.DialContext(ctx, "tcp", dst)
		}
		if err != nil {
			return nil, err
		}
		return peerConn{Conn: conn, remote: peerAddr{Addr: conn.RemoteAddr(), kind: kind}}, nil
	}
}
