package testenv

import "runtime"

// DockerReachHost returns the `docker run` arguments that let a client container reach a replica of this process under the host
// name name. A replica listens on the loopback address. On Linux the container joins the host's network and name resolves to
// 127.0.0.1; the bridge gateway (host-gateway) would reach the host's other addresses only, where no replica listens. Docker
// Desktop (macOS) forwards host-gateway to the host's loopback, and host networking does not reach it there.
func DockerReachHost(name string) []string {
	if runtime.GOOS == "linux" {
		return []string{"--network", "host", "--add-host", name + ":127.0.0.1"}
	}
	return []string{"--add-host", name + ":host-gateway"}
}
