package main

import (
	"log"
	"time"

	"golang.org/x/sys/unix"
)

// clockDriftThreshold is the wall-clock drift above which a restored guest's
// clock is worth correcting. Sub-second skew is left alone: it is within the
// noise of normal exec round trips and not worth a syscall.
const clockDriftThreshold = time.Second

// clockNeedsSync reports whether the guest clock should be stepped to the host
// clock. A zero hostNano means the host did not stamp the request (an older
// host, or the field omitted), so the clock must not be touched. Otherwise a
// resync is needed only when the two clocks differ by more than one second in
// either direction.
func clockNeedsSync(hostNano, guestNano int64) bool {
	if hostNano == 0 {
		return false
	}
	drift := hostNano - guestNano
	if drift < 0 {
		drift = -drift
	}
	return drift > int64(clockDriftThreshold)
}

// maybeSyncClock steps CLOCK_REALTIME to hostNano when the guest has drifted
// more than clockDriftThreshold from it. A restored microVM wakes with the
// snapshot's wall clock, so this self-heals the guest time on every exec with
// no restore-time signaling. Callers must gate this on PID 1 (guest) — it must
// never run on the host, where it would step the machine clock.
func maybeSyncClock(hostNano int64) {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &now); err != nil {
		log.Printf("clock resync: read guest clock: %v", err)
		return
	}
	if !clockNeedsSync(hostNano, now.Nano()) {
		return
	}
	ts := unix.NsecToTimespec(hostNano)
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		log.Printf("clock resync: set guest clock: %v", err)
	}
}
