//go:build breakit

package capacity

import "math"

// NaiveEstimate is the estimator a teammate wrote in a hurry the night before a capacity review. Every line looks
// reasonable on its own. It contains four bugs, each a classic back-of-envelope mistake:
//
//	(1) a day is "about 1e5 seconds"
//	(2) sizes are "converted" from bits to bytes, but they were already bytes
//	(3) storage forgets replication
//	(4) servers are sized for average traffic at 100% utilisation
//
// The break-it exercise (labs/a0/README.md): before running anything, predict the ratio naive/correct for every
// field of the sheet and write it down. Then run
//
//	go test -tags breakit -run Breakit -v ./labs/a0/capacity/
//
// compare the logged ratios with your prediction, and fix this function until the test passes. Do not change the
// test. Your fixed version should be a few lines different from this one, not a rewrite.
func NaiveEstimate(s Spec) Sheet {
	const secondsPerDay = 1e5 // (1) "close enough"

	var sh Sheet
	sh.WritesPerDay = s.DAU * s.WritesPerUserPerDay
	sh.ReadsPerDay = sh.WritesPerDay * s.ReadWriteRatio

	sh.AvgWriteQPS = sh.WritesPerDay / secondsPerDay
	sh.AvgReadQPS = sh.ReadsPerDay / secondsPerDay
	sh.AvgQPS = sh.AvgWriteQPS + sh.AvgReadQPS

	sh.PeakWriteQPS = sh.AvgWriteQPS * s.PeakFactor
	sh.PeakReadQPS = sh.AvgReadQPS * s.PeakFactor
	sh.PeakQPS = sh.PeakWriteQPS + sh.PeakReadQPS

	sh.StorageBytes = sh.WritesPerDay * s.WriteBytes * s.RetentionDays * (1 + s.IndexOverhead) // (3)

	sh.PeakIngressBytesPerSec = sh.PeakWriteQPS * s.WriteBytes / 8 // (2) "bits to bytes"
	sh.PeakEgressBytesPerSec = sh.PeakReadQPS * s.ReadBytes / 8    // (2)

	sh.CacheBytes = s.CacheHotFraction * sh.ReadsPerDay * s.ReadBytes

	sh.Servers = int(math.Ceil(sh.AvgQPS/s.PerServerQPS)) + s.Spares // (4)
	sh.PeakConcurrency = sh.PeakQPS * s.Latency.Seconds()
	return sh
}
