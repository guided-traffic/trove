package cluster

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// register registers collectors on reg; a nil registerer registers nothing.
// A family that is already there means two instances share a registerer, which
// the callers report as a configuration error.
func register(reg prometheus.Registerer, cs ...prometheus.Collector) error {
	if reg == nil {
		return nil
	}
	for _, c := range cs {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return fmt.Errorf("cluster: metrics are already registered on this registerer: %w", err)
			}
			return err
		}
	}
	return nil
}
