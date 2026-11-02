//go:build !race

package claims_test

// raceDetector is true in a build with the race detector, whose instrumentation makes timing
// targets meaningless.
const raceDetector = false
