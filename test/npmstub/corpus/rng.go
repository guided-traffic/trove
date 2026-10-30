package corpus

// rng is splitmix64: a generator whose output is fixed by its algorithm and seed, so the corpus
// does not depend on the standard library's generators (whose streams are not a stable contract).
type rng struct{ state uint64 }

func newRNG(seed uint64) *rng { return &rng{state: seed} }

func (r *rng) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intn returns a value in [0, n); n must be positive.
func (r *rng) intn(n int) int {
	return int(r.next() % uint64(n)) //nolint:gosec // n is a small positive count and the result is below it
}

// chance reports true with the given probability in percent.
func (r *rng) chance(percent int) bool { return r.intn(100) < percent }

// mix derives a sub-seed from a seed and a number, so every package has its own stream and a change
// to one package's generation does not move the others.
func mix(seed uint64, n int) uint64 {
	r := newRNG(seed ^ (uint64(n)+1)*0xd6e8feb86659fd93) //nolint:gosec // n is a non-negative index
	return r.next()
}

// fnv64 hashes a string (FNV-1a) into a seed.
func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
