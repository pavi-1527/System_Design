package quantity

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		unit string
		in   string
		want float64
	}{
		{"req/s", "1157", 1157},
		{"req/s", "1.2k", 1200},
		{"req/s", "3.5M req/s", 3.5e6},
		{"req/s", "1,200", 1200},
		{"req/s", "2.5e6", 2.5e6},
		{"req/s", "3x10^7", 3e7},
		{"req/s", "10^6", 1e6},
		{"users", "1.5B", 1.5e9},
		{"users", "2 billion", 2e9},
		{"servers", "12 servers", 12},
		{"bytes", "40TB", 40e12},
		{"bytes", "1.5 GB", 1.5e9},
		{"bytes", "2 GiB", 2 * (1 << 30)},
		{"bytes", "64 KB", 64e3},
		{"TB", "40", 40e12},
		{"TB", "0.04 PB", 40e12},
		{"TB", "40 T", 40e12},
		{"bits/s", "12 Gbps", 12e9},
		{"Gbps", "12", 12e9},
		{"Gbps", "1.5 GB/s", 12e9},
		{"bytes/s", "8 Gbps", 1e9},
		{"MB/s", "100", 100e6},
		{"minutes", "43.8", 43.8 * 60},
		{"minutes", "2 hours", 7200},
		{"ms", "250", 0.25},
		{"ms", "0.25 s", 0.25},
	}
	for _, c := range cases {
		got, _, err := ParseUnit(c.unit).Parse(c.in)
		if err != nil {
			t.Errorf("unit %q, Parse(%q): %v", c.unit, c.in, err)
			continue
		}
		if RelErr(got, c.want) > 1e-9 {
			t.Errorf("unit %q, Parse(%q) = %g, want %g", c.unit, c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ unit, in string }{
		{"req/s", ""},
		{"req/s", "lots"},
		{"req/s", "5 GB"},
		{"bytes", "5 parsecs"},
	} {
		if _, _, err := ParseUnit(c.unit).Parse(c.in); err == nil {
			t.Errorf("unit %q, Parse(%q): expected error", c.unit, c.in)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		v    float64
		d    Dim
		want string
	}{
		{1157, Count, "1.16k"},
		{40e12, Bytes, "40 TB"},
		{12e9, Bits, "12 Gbps"},
		{0.25, Duration, "250 ms"},
		{2628, Duration, "43.8 min"},
		{999, Count, "999"},
	}
	for _, c := range cases {
		if got := Format(c.v, c.d); got != c.want {
			t.Errorf("Format(%g, %v) = %q, want %q", c.v, c.d, got, c.want)
		}
	}
}
