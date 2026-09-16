// Command taillow is an LLM gateway that joins a tailnet as its own node.
//
// Milestone 1: join the tailnet and answer GET /healthz. Nothing listens on
// the host's network interfaces. The tailnet listener is the only way in.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

type config struct {
	hostname  string
	stateDir  string
	ephemeral bool
	addr      string
	upTimeout time.Duration
}

func main() {
	var cfg config
	flag.StringVar(&cfg.hostname, "hostname", "taillow", "machine name to register on the tailnet")
	flag.StringVar(&cfg.stateDir, "state-dir", "", "directory for the node's state (empty: tsnet picks one)")
	flag.BoolVar(&cfg.ephemeral, "ephemeral", true, "register as an ephemeral node, removed after it goes offline")
	flag.StringVar(&cfg.addr, "addr", ":80", "address to listen on, on the tailnet only")
	flag.DurationVar(&cfg.upTimeout, "up-timeout", time.Minute, "how long to wait for the node to join the tailnet")
	flag.Parse()

	// run returns an error instead of calling log.Fatal itself, so its
	// deferred cleanup (leaving the tailnet) always happens.
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config) error {
	// The auth key comes from the TS_AUTHKEY environment variable. It is
	// never a flag, so it cannot land in shell history or a process listing.
	srv := &tsnet.Server{
		Hostname:  cfg.hostname,
		Dir:       cfg.stateDir,
		Ephemeral: cfg.ephemeral,
		UserLogf:  log.Printf,
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Up blocks until the node has joined. A missing or rejected auth key
	// becomes a startup error here, not a server that silently never appears.
	upCtx, cancel := context.WithTimeout(ctx, cfg.upTimeout)
	defer cancel()
	if _, err := srv.Up(upCtx); err != nil {
		return fmt.Errorf("joining the tailnet as %q: %w", cfg.hostname, err)
	}

	// Listen on the tailnet, not on the host.
	ln, err := srv.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listening on tailnet address %s: %w", cfg.addr, err)
	}

	httpSrv := &http.Server{
		Handler:           newMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	log.Printf("taillow: serving on tailnet address %s as %q", cfg.addr, cfg.hostname)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server stopped: %w", err)
	case <-ctx.Done():
	}

	log.Printf("taillow: shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	return httpSrv.Shutdown(shutdownCtx)
}

// newMux builds the routes. The method in each pattern makes the standard
// library answer other methods with 405 and an Allow header.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	return mux
}

// healthz reports that the process is up and serving.
func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
