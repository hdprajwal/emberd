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
	flag.Parse()

	mgr, err := firecracker.New(firecracker.Config{})
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
	_ = srv.Shutdown(shutdownCtx)
}
