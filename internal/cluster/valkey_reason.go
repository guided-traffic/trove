package cluster

// ReasonCode says in a fixed vocabulary why the client is degraded. It is what
// the unauthenticated ops listener may show (/readyz?verbose=1): the free text
// of ValkeyStatus.DegradedReason names addresses and server replies of the
// Valkey topology and goes to the log and the authenticated replica list only.
type ReasonCode string

// The reason codes. A new code is added here and to ReasonCodes; the text of
// the reason may change, a code may not.
const (
	// ReasonNone is the code of an available client.
	ReasonNone ReasonCode = ""
	// ReasonConnecting: no connection yet, or the last attempt failed for a
	// reason that has no code of its own (refused, reset, closed by the peer).
	ReasonConnecting ReasonCode = "connecting"
	// ReasonNoSentinel: none of the configured Sentinels answered.
	ReasonNoSentinel ReasonCode = "no_sentinel"
	// ReasonTimeout: the last connection attempt ran into a deadline.
	ReasonTimeout ReasonCode = "timeout"
	// ReasonAuthFailed: Valkey or a Sentinel rejected the credential
	// (WRONGPASS, NOAUTH, NOPERM).
	ReasonAuthFailed ReasonCode = "auth_failed"
	// ReasonConfigError: another configuration error of Q-HA-13, a rejected
	// certificate or a primary name no Sentinel knows.
	ReasonConfigError ReasonCode = "config_error"
	// ReasonUnsupported: the server is not Valkey 9.0 or later.
	ReasonUnsupported ReasonCode = "unsupported"
	// ReasonCircuitOpen: the circuit breaker refuses calls.
	ReasonCircuitOpen ReasonCode = "circuit_open"
	// ReasonProbeFailed: the last health probe failed.
	ReasonProbeFailed ReasonCode = "probe_failed"
	// ReasonClosed: the client was closed (shutdown).
	ReasonClosed ReasonCode = "closed"
)

// ReasonCodes lists every code a degraded client can report.
func ReasonCodes() []ReasonCode {
	return []ReasonCode{
		ReasonConnecting, ReasonNoSentinel, ReasonTimeout, ReasonAuthFailed, ReasonConfigError,
		ReasonUnsupported, ReasonCircuitOpen, ReasonProbeFailed, ReasonClosed,
	}
}

// connectReason is the code of a failed connection attempt: the credential, the
// other configuration errors, every Sentinel silent, a deadline, or the rest.
func connectReason(isConfig bool, kind ErrorKind, probes []sentinelProbe) ReasonCode {
	switch {
	case kind == KindAuth:
		return ReasonAuthFailed
	case isConfig:
		return ReasonConfigError
	case len(probes) > 0 && reachableSentinels(probes) == 0:
		return ReasonNoSentinel
	case kind == KindTimeout:
		return ReasonTimeout
	}
	return ReasonConnecting
}

func reachableSentinels(probes []sentinelProbe) int {
	n := 0
	for _, p := range probes {
		if p.Err == nil {
			n++
		}
	}
	return n
}
