// Package bastionshared tests the Bastion's shared state and abuse guard (R-BAS-034 to R-BAS-037) against the
// integration stack's Valkey with Sentinel and TLS: two replicas that share what Valkey holds, and the same
// two replicas without Valkey, simulated by an address nothing listens on. A test never stops Valkey and never
// triggers a failover (the stack is shared); every test has a key prefix of its own, and the time of the
// replicas is an injected clock, so a minute or a quarter of an hour costs no waiting.
package bastionshared
