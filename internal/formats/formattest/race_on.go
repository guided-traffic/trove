//go:build race

package formattest

// raceDetector reports that the race detector instruments this build: the kit's time bounds are stretched by
// raceSlowdown (checks.go), and the plain run of the kit (make test) holds them unstretched.
const raceDetector = true
