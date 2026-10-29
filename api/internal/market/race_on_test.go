//go:build race

package market

// raceDetector: the tests run under -race, about ten times slower; a test
// that simulates a whole day of polls one after the other (no goroutines to
// race) simulates the minutes its checks need instead.
const raceDetector = true
