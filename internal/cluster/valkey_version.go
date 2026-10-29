package cluster

import (
	"fmt"
	"strconv"
	"strings"
)

// Minimum supported Valkey version (Q-ARCH-13): the flight lock releases with
// DELIFEQ, which exists since 9.0.
const (
	minValkeyMajor = 9
	minValkeyMinor = 0
)

// parseVersion reads "major.minor[.patch][-suffix]".
func parseVersion(s string) (major, minor int, err error) {
	parts := strings.SplitN(strings.TrimSpace(s), ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("version %q is not major.minor", s)
	}
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("version %q: %w", s, err)
	}
	minorText, _, _ := strings.Cut(parts[1], "-")
	if minor, err = strconv.Atoi(minorText); err != nil {
		return 0, 0, fmt.Errorf("version %q: %w", s, err)
	}
	return major, minor, nil
}

// checkServer decides from the server and version fields of the HELLO reply
// whether trove supports the server. The empty string means supported; any
// other value is the reason, in the wording R-ARCH-031 fixes.
func checkServer(server, version string) string {
	if server != "valkey" {
		return fmt.Sprintf("server %q is not supported; trove requires Valkey %d.%d or later", server, minValkeyMajor, minValkeyMinor)
	}
	major, minor, err := parseVersion(version)
	if err != nil {
		return fmt.Sprintf("valkey version %q cannot be read; trove requires %d.%d or later", version, minValkeyMajor, minValkeyMinor)
	}
	if major < minValkeyMajor || (major == minValkeyMajor && minor < minValkeyMinor) {
		return fmt.Sprintf("valkey %s is not supported; trove requires %d.%d or later", version, minValkeyMajor, minValkeyMinor)
	}
	return ""
}

// helloFields extracts server and version from a HELLO reply, given as the
// flattened key/value map of the reply.
func helloFields(m map[string]string) (server, version string, err error) {
	server, version = m["server"], m["version"]
	if server == "" || version == "" {
		return "", "", fmt.Errorf("HELLO reply has no server or version field")
	}
	return server, version, nil
}
