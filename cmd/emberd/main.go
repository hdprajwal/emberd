package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/hdprajwal/emberd/pkg/api"
	"github.com/hdprajwal/emberd/pkg/sandbox/firecracker"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "HTTP listen address")
	poolSize := flag.Int("pool-size", 0, "pre-warmed sandboxes per pack (0 = default 3, -1 disables the pool so Create restores directly)")
	skipWarm := flag.Bool("skip-warm", false, "skip building/filling snapshots at startup; defer to the first Create per pack")
	snapshotDir := flag.String("snapshot-dir", "", "directory for template snapshots (empty = <workdir>/snapshots)")
	flag.Parse()

	mgr, err := firecracker.New(firecracker.Config{
		PoolSize:        *poolSize,
		SkipWarmOnStart: *skipWarm,
		SnapshotDir:     *snapshotDir,
	})
	if err != nil {
		log.Fatalf("init sandbox manager: %v", err)
	}

	mux := http.NewServeMux()
	api.NewServer(mgr).Register(mux)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("emberd listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ordering is load-bearing: drain the HTTP server FIRST, then tear down the
	// manager. srv.Shutdown blocks until every in-flight request handler has
	// returned, so once it completes no Create can still be running. Only then is
	// it safe to call mgr.Close(), which snapshots m.vms and deletes each VM: a
	// Create that raced Close could otherwise register (and leak) a Firecracker
	// process after Close had already scanned the VM set. Closing the server up
	// front removes that race for the daemon path entirely.
	_ = srv.Shutdown(shutdownCtx)

	if err := mgr.Close(); err != nil {
		log.Printf("manager shutdown: %v", err)
	}
	log.Println("shutdown complete")
}
