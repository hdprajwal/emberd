package main

import "testing"

func TestLeakStatus(t *testing.T) {
	cases := []struct {
		name      string
		fcDelta   int
		fdDelta   int
		fdChecked bool
		want      string
	}{
		{"clean", 0, 0, true, "pass"},
		{"fd growth under threshold passes", 0, 8, true, "pass"},
		{"fd growth over threshold warns", 0, 9, true, "warn"},
		{"leaked firecracker process fails", 1, 0, true, "fail"},
		{"firecracker leak outranks fd warning", 2, 100, true, "fail"},
		{"fd growth ignored when fd check skipped", 0, 100, false, "pass"},
		{"negative fd delta passes", 0, -3, true, "pass"},
	}
	for _, c := range cases {
		if got := leakStatus(c.fcDelta, c.fdDelta, c.fdChecked); got != c.want {
			t.Errorf("%s: leakStatus(%d, %d, %v) = %q, want %q", c.name, c.fcDelta, c.fdDelta, c.fdChecked, got, c.want)
		}
	}
}

func TestFirstLastQuartileP50(t *testing.T) {
	// 8 samples, cycle order: no upward trend => the last-quartile p50 is not
	// above the first, so no warning. q = 8/4 = 2; first window = [10, 12],
	// last window = [11, 13]. p50 nearest-rank of a 2-element window is the
	// second element.
	steady := []float64{10, 12, 20, 20, 20, 20, 11, 13}
	first, last, warn := firstLastQuartileP50(steady)
	if first != 12 || last != 13 {
		t.Errorf("steady: first/last p50 = %v/%v, want 12/13", first, last)
	}
	if warn {
		t.Errorf("steady: warn = true, want false (13 is not >20%% above 12)")
	}
}

func TestFirstLastQuartileP50Degrades(t *testing.T) {
	// A clear upward trend: first window p50 well below last window p50 by
	// more than 20%.
	degrading := []float64{10, 10, 50, 50, 50, 50, 30, 30}
	first, last, warn := firstLastQuartileP50(degrading)
	if first != 10 || last != 30 {
		t.Errorf("degrading: first/last p50 = %v/%v, want 10/30", first, last)
	}
	if !warn {
		t.Errorf("degrading: warn = false, want true (30 is >20%% above 10)")
	}
}

func TestFirstLastQuartileP50TooFewSamples(t *testing.T) {
	// Fewer than 4 samples: q < 1, so the windows are empty and there is no
	// degradation signal.
	first, last, warn := firstLastQuartileP50([]float64{5, 6, 7})
	if first != 0 || last != 0 || warn {
		t.Errorf("too-few: (%v, %v, %v), want (0, 0, false)", first, last, warn)
	}
}
