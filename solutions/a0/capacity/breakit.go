//go:build breakit

package capacity

import "math"

// NaiveEstimate is the fixed version of the hurried estimator in labs/a0/capacity/breakit.go. Each fix is marked
// with the bug number from the lab copy.
func NaiveEstimate(s Spec) Sheet {
	const secondsPerDay = SecondsPerDay // (1) 86,400, not 1e5: a 13.6% under-estimate of every rate

	var sh Sheet
	sh.WritesPerDay = s.DAU * s.WritesPerUserPerDay
	sh.ReadsPerDay = sh.WritesPerDay * s.ReadWriteRatio

	sh.AvgWriteQPS = sh.WritesPerDay / secondsPerDay
	sh.AvgReadQPS = sh.ReadsPerDay / secondsPerDay
	sh.AvgQPS = sh.AvgWriteQPS + sh.AvgReadQPS

	sh.PeakWriteQPS = sh.AvgWriteQPS * s.PeakFactor
	sh.PeakReadQPS = sh.AvgReadQPS * s.PeakFactor
	sh.PeakQPS = sh.PeakWriteQPS + sh.PeakReadQPS

	sh.StorageBytes = sh.WritesPerDay * s.WriteBytes * s.RetentionDays * s.Replication * (1 + s.IndexOverhead) // (3)

	sh.PeakIngressBytesPerSec = sh.PeakWriteQPS * s.WriteBytes // (2) sizes are already bytes
	sh.PeakEgressBytesPerSec = sh.PeakReadQPS * s.ReadBytes    // (2)

	sh.CacheBytes = s.CacheHotFraction * sh.ReadsPerDay * s.ReadBytes

	// (4) size for peak, at the target utilisation, with a float-noise-tolerant ceiling (same as ServersNeeded).
	sh.Servers = int(math.Ceil(sh.PeakQPS/(s.PerServerQPS*s.TargetUtil)*(1-1e-9))) + s.Spares
	sh.PeakConcurrency = sh.PeakQPS * s.Latency.Seconds()
	return sh
}
