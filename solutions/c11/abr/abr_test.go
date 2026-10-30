package abr

import (
	"errors"
	"testing"
)

var tLadder = []Rendition{
	{Name: "1080p", BitrateBps: 5_000_000},
	{Name: "360p", BitrateBps: 800_000},
	{Name: "720p", BitrateBps: 2_500_000},
	{Name: "480p", BitrateBps: 1_200_000},
}

func TestStartup_PicksHighestAffordableOrLowest(t *testing.T) {
	var p Policy
	cases := []struct {
		throughput float64
		want       string
	}{
		{0, "360p"},         // unknown throughput: start low, ramp later
		{500_000, "360p"},   // nothing fits: lowest
		{1_000_000, "360p"}, // 0.8 × 1 Mbps = 800 kbps: exactly 360p
		{1_600_000, "480p"}, // 1.28 Mbps affords 480p, not 720p
		{4_000_000, "720p"}, // 3.2 Mbps
		{10_000_000, "1080p"},
	}
	for _, c := range cases {
		got, err := p.Startup(tLadder, c.throughput)
		if err != nil || got.Name != c.want {
			t.Errorf("Startup(%.0f) = %s, %v; want %s", c.throughput, got.Name, err, c.want)
		}
	}
	if _, err := p.Startup(nil, 1e6); !errors.Is(err, ErrNoRenditions) {
		t.Fatalf("Startup(no renditions): %v, want ErrNoRenditions", err)
	}
}

func TestNext_SwitchesDownImmediatelyButUpOnlyWithBuffer(t *testing.T) {
	var p Policy
	// Throughput collapses while playing 1080p: drop at once, whatever the buffer.
	got, _ := p.Next(tLadder, State{Current: "1080p", ThroughputBps: 2_000_000, BufferSec: 20})
	if got.Name != "480p" {
		t.Fatalf("throughput 2 Mbps while on 1080p with a full buffer: picked %s, want 480p (switch down immediately)", got.Name)
	}
	// Throughput recovers but the buffer is thin: stay put.
	got, _ = p.Next(tLadder, State{Current: "480p", ThroughputBps: 10_000_000, BufferSec: 6})
	if got.Name != "480p" {
		t.Fatalf("10 Mbps with a 6 s buffer while on 480p: picked %s, want 480p (don't switch up until the buffer is comfortable)", got.Name)
	}
	// Buffer comfortable: switch up to the affordable rung.
	got, _ = p.Next(tLadder, State{Current: "480p", ThroughputBps: 10_000_000, BufferSec: 12})
	if got.Name != "1080p" {
		t.Fatalf("10 Mbps with a 12 s buffer while on 480p: picked %s, want 1080p", got.Name)
	}
	// Steady state: same rung.
	got, _ = p.Next(tLadder, State{Current: "720p", ThroughputBps: 3_500_000, BufferSec: 15})
	if got.Name != "720p" {
		t.Fatalf("steady 3.5 Mbps on 720p: picked %s, want 720p", got.Name)
	}
}

func TestNext_PanicBufferDropsToLowest(t *testing.T) {
	var p Policy
	got, _ := p.Next(tLadder, State{Current: "1080p", ThroughputBps: 50_000_000, BufferSec: 3})
	if got.Name != "360p" {
		t.Fatalf("3 s of buffer left: picked %s, want 360p (a rebuffer is worse than a blurry segment)", got.Name)
	}
	got, _ = p.Next(tLadder, State{Current: "1080p", ThroughputBps: 50_000_000, BufferSec: 4})
	if got.Name != "1080p" {
		t.Fatalf("exactly 4 s of buffer: picked %s, want 1080p (panic applies below the threshold)", got.Name)
	}
}

func TestNext_UnknownCurrentBehavesLikeStartupAndPolicyIsTunable(t *testing.T) {
	var p Policy
	got, _ := p.Next(tLadder, State{Current: "", ThroughputBps: 4_000_000, BufferSec: 0})
	if got.Name != "720p" {
		t.Fatalf("Next with no current rendition = %s, want Startup's 720p", got.Name)
	}
	cautious := Policy{Safety: 0.5, UpSwitchBufferSec: 20, PanicBufferSec: 8}
	got, _ = cautious.Next(tLadder, State{Current: "360p", ThroughputBps: 4_000_000, BufferSec: 15})
	if got.Name != "360p" {
		t.Fatalf("cautious policy with a 15 s buffer: picked %s, want 360p (UpSwitchBufferSec 20)", got.Name)
	}
	got, _ = cautious.Next(tLadder, State{Current: "360p", ThroughputBps: 4_000_000, BufferSec: 25})
	if got.Name != "480p" {
		t.Fatalf("cautious policy, 4 Mbps × 0.5 = 2 Mbps: picked %s, want 480p", got.Name)
	}
	if _, err := p.Next(nil, State{}); !errors.Is(err, ErrNoRenditions) {
		t.Fatalf("Next(no renditions): %v, want ErrNoRenditions", err)
	}
}
