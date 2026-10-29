package cluster

import "github.com/valkey-io/valkey-go"

func authCtx(a peerAddr) valkey.AuthCredentialsContext {
	return valkey.AuthCredentialsContext{Address: a}
}
