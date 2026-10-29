package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Identity names this process (R-OPS-003).
type Identity struct {
	// Instance is the replica's name: the pod name, or hostname-pid outside Kubernetes.
	Instance string
	// UID is random per process start (replica.instance_uid).
	UID string
	// Role is web, worker or all.
	Role string
	// Node is the Kubernetes node name (TROVE_NODE_NAME); empty outside Kubernetes.
	Node string
}

// NewIdentity derives the identity from the environment. The instance name is
// the first of TROVE_POD_NAME (what the chart sets from the downward API) and
// POD_NAME; HOSTNAME counts only inside Kubernetes (KUBERNETES_SERVICE_HOST is
// set), where it is the pod name. Anywhere else it is "<hostname>-<pid>", so two
// processes on one machine never share an instance name.
func NewIdentity(environ []string, role string) Identity {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	id := Identity{Role: role, Node: env["TROVE_NODE_NAME"], UID: randomUID()}
	switch {
	case env["TROVE_POD_NAME"] != "":
		id.Instance = env["TROVE_POD_NAME"]
	case env["POD_NAME"] != "":
		id.Instance = env["POD_NAME"]
	case env["KUBERNETES_SERVICE_HOST"] != "" && env["HOSTNAME"] != "":
		id.Instance = env["HOSTNAME"]
	default:
		host := env["HOSTNAME"]
		if h, err := os.Hostname(); err == nil && h != "" {
			host = h
		}
		if host == "" {
			host = "trove"
		}
		id.Instance = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return id
}

func randomUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
