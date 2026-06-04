// emberd-init is the PID 1 process inside each sandbox microVM. It listens on
// vsock for exec requests from the host daemon, runs the submitted code under
// the language pack's interpreter, and streams the result back.
//
// PID 1 duties (mounting /proc, the overlay root, child reaping, the
// switch_root chain from the initramfs) arrive with the custom-initramfs
// milestone; for now this is the vsock control-plane agent.
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

	// When launched as PID 1 inside the guest, set up the rootfs before serving.
	// Run on the host (tests, manual runs) skips this.
	interp := *interpreter
	if os.Getpid() == 1 {
		if err := bootstrapPID1(); err != nil {
			log.Fatalf("guest bootstrap: %v", err)
		}
		// The language pack's interpreter is passed by the host on the kernel
		// command line; fall back to the flag default if absent.
		if v := kernelParam("emberd.interpreter"); v != "" {
			interp = v
		}
	}

	handle := func(req proto.ExecRequest) proto.ExecResult {
		return runExec(context.Background(), interp, req)
	}
	if err := serveVsock(uint32(*port), handle); err != nil {
		log.Fatalf("vsock server: %v", err)
	}
}
