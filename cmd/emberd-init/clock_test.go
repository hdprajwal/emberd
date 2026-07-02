package main

import (
	"testing"
	"time"
)

func TestClockNeedsSync(t *testing.T) {
	const guest = int64(1_700_000_000) * int64(time.Second) // arbitrary fixed guest instant

	tests := []struct {
		name      string
		hostNano  int64
		guestNano int64
		want      bool
	}{
		{"zero host means do not touch", 0, guest, false},
		{"exactly equal", guest, guest, false},
		{"sub-second ahead", guest + int64(400*time.Millisecond), guest, false},
		{"sub-second behind", guest - int64(400*time.Millisecond), guest, false},
		{"just under one second", guest + int64(999*time.Millisecond), guest, false},
		{"host far ahead", guest + int64(5*time.Second), guest, true},
		{"host far behind", guest - int64(5*time.Second), guest, true},
		{"just over one second ahead", guest + int64(1100*time.Millisecond), guest, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clockNeedsSync(tt.hostNano, tt.guestNano); got != tt.want {
				t.Errorf("clockNeedsSync(%d, %d) = %v, want %v", tt.hostNano, tt.guestNano, got, tt.want)
			}
		})
	}
}
