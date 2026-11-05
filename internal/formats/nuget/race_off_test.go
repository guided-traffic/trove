//go:build !race

package nuget

// raceEnabled is false without the race detector; see race_on_test.go.
const raceEnabled = false
