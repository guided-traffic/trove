//go:build !race

package avatar

// raceDetector is true in a build with the race detector, whose instrumentation makes timing
// targets meaningless.
const raceDetector = false
