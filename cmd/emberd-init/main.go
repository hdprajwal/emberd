// emberd-init is the PID 1 process inside each sandbox microVM. It listens on
// vsock for exec requests from the host daemon, runs the submitted code under
// the language pack's interpreter, and streams the result back.
//
// As PID 1 it also performs the init duties: bootstrapping the overlay root and
// switch_root'ing into it (boot.go) and reaping orphaned children (reaper.go).
// When run off the guest (host tests, manual runs) it is just the control-plane
// agent — Getpid() != 1, so the init duties are skipped.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/hdprajwal/emberd/pkg/proto"
)

func main() {
	port := flag.Uint("port", uint(proto.GuestPort), "vsock port to listen on")
	interpreter := flag.String("interpreter", "python3", "interpreter used to run submitted code")
	flag.Parse()

	log.SetPrefix("emberd-init: ")
	log.SetFlags(0)

	// When launched as PID 1 inside the guest, set up the rootfs and start
	// reaping orphaned children before serving. Run on the host (tests, manual
	// runs) skips both — there is no rootfs to build and no PID 1 duty.
	interp := *interpreter
	var reaper *childReaper
	if os.Getpid() == 1 {
		if err := bootstrapPID1(); err != nil {
			log.Fatalf("guest bootstrap: %v", err)
		}
		// The language pack's interpreter is passed by the host on the kernel
		// command line; fall back to the flag default if absent.
		if v := kernelParam("emberd.interpreter"); v != "" {
			interp = v
		}
		// As PID 1, inherit and reap any process a workload double-forks, while
		// still collecting interpreter exit codes ourselves.
		reaper = newChildReaper()
		reaper.start()
	}

	handle := func(req proto.ExecRequest) proto.ExecResult {
		return runExec(context.Background(), reaper, interp, req)
	}
	if err := serveVsock(uint32(*port), handle); err != nil {
		log.Fatalf("vsock server: %v", err)
	}
}
