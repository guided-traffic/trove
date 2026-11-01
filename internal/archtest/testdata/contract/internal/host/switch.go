package host

import "example.test/fix/internal/formats"

func switchGate(a formats.Adapter) bool {
	switch a.(type) {
	case formats.Gate:
		return true
	}
	return false
}
