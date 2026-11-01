package host

import "example.test/fix/internal/formats"

func assertGate(a formats.Adapter) bool {
	g, ok := a.(formats.Gate)
	return ok && g.Allow()
}
