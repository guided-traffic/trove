package ansiblestub

import "sync"

// Inflight is what the stub says of the requests it is serving at this moment, for the tests of the mirror's client-first rule
// (R-REPO-055): how many requests are open now and the most that were open at once since the last reset. A request is open from
// the moment the stub has counted it, which includes the delay of Mode.DelayMS, until its handler returns. The control API is not
// counted. Requests are counted, not TCP connections: a client that multiplexes on one connection or keeps idle connections alive
// would make a connection count say nothing about concurrent fetches.
type Inflight struct {
	Open int `json:"open"`
	Max  int `json:"max"`
	// Requests is the number of requests counted since the last reset.
	Requests int `json:"requests"`
}

type inflightState struct {
	mu sync.Mutex
	v  Inflight
}

// enter counts one open request and returns the function that ends it.
func (f *inflightState) enter() func() {
	f.mu.Lock()
	f.v.Open++
	f.v.Requests++
	f.v.Max = max(f.v.Max, f.v.Open)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.v.Open--
		f.mu.Unlock()
	}
}

// reset starts the maximum afresh from the requests open now.
func (f *inflightState) reset() {
	f.mu.Lock()
	f.v.Max, f.v.Requests = f.v.Open, 0
	f.mu.Unlock()
}

func (f *inflightState) read() Inflight {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.v
}

// Inflight returns the open requests now, their maximum since the last reset and their number.
func (s *Server) Inflight() Inflight { return s.fl.read() }
