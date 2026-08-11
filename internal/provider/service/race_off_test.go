//go:build !race

package service_test

// withRaceDetector says whether this build carries the race detector inside.
// The time bars are not measured with it: it multiplies the cost by more than
// an order of magnitude, so measuring there would be measuring the detector.
const withRaceDetector = false
