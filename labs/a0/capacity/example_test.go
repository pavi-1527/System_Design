package capacity

import (
	"fmt"
	"time"
)

// The example lives in package capacity (not capacity_test) so this file is byte-identical in labs/ and
// solutions/, whose import paths differ.
func ExampleEstimate() {
	sheet, err := Estimate(Spec{
		Name: "pastebin", DAU: 10e6, WritesPerUserPerDay: 0.1, ReadWriteRatio: 10,
		WriteBytes: 10e3, ReadBytes: 10e3, RetentionDays: 730, Replication: 3, IndexOverhead: 0.1,
		PeakFactor: 3, CacheHotFraction: 0.2, PerServerQPS: 1000, TargetUtil: 0.6, Spares: 1,
		Latency: 50 * time.Millisecond,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(sheet)
	// Output:
	// per day      1M writes, 10M reads
	// avg QPS      127.3 (w 11.6, r 115.7)
	// peak QPS     381.9 (w 34.7, r 347.2)
	// storage      24.1 TB
	// peak ingress 347.2 KB/s (2.8 Mbps)
	// peak egress  3.5 MB/s (27.8 Mbps)
	// cache        20 GB
	// servers      2
	// in flight    19.1
}
