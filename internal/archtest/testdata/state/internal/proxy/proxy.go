// Package proxy holds package-level state that R-ARCH-032 forbids.
package proxy

import (
	"errors"
	"regexp"
	"sync"
)

type iface interface{ M() }
type impl struct{}

func (*impl) M() {}

var (
	registry = map[string]int{}
	current  *int
	items    []string
	shared   sync.Map
	// ErrGone is an interface-typed sentinel and fine.
	ErrGone       = errors.New("gone")
	_       iface = (*impl)(nil)
	counter int
	// pattern is a compiled regular expression: immutable, exempt.
	pattern = regexp.MustCompile("x")
)
