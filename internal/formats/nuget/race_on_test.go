//go:build race

package nuget

// raceEnabled: the race detector's runtime makes the JSON decoder allocate about 1 KiB per decoded string, so a test that measures
// the bytes of a decode compares against a bound that is larger by that factor and still far below an unbounded decode's.
const raceEnabled = true
