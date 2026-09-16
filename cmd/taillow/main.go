// Command taillow is an LLM gateway that joins a tailnet as its own node.
//
// Milestone 2 adds GET /whoami, which reports who the tailnet says is calling
// and what the tailnet policy grants them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/13inks/taillow/internal/grant"
	"github.com/13inks/taillow/internal/ident"
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

	lc, err := srv.LocalClient()
	if err != nil {
		return fmt.Errorf("getting the tailnet local client: %w", err)
	}

	httpSrv := &http.Server{
		Handler:           newMux(lc),
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
func newMux(who ident.WhoIser) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		whoamiHandler(w, r, who)
	})
	return mux
}

// healthz reports that the process is up and serving.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// refusal is the body of every 4xx and 5xx. Reason is always set: a refusal
// that does not say why is as hard to debug as a silent success.
type refusal struct {
	Status int           `json:"status"`
	Reason string        `json:"reason"`
	Caller *ident.Caller `json:"caller,omitempty"`
}

type whoamiResponse struct {
	Caller ident.Caller `json:"caller"`
	Grant  grant.Grant  `json:"grant"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing JSON response: %v", err)
	}
}

// whoamiHandler reports the caller's identity and grant, or refuses with a
// reason. The caller is echoed on a grant refusal because it is their own
// identity, and knowing who the tailnet thinks they are is how they fix it.
func whoamiHandler(w http.ResponseWriter, r *http.Request, who ident.WhoIser) {
	caller, caps, err := ident.Resolve(r.Context(), who, r.RemoteAddr)
	if err != nil {
		// errors.Is, not ==: Resolve wraps ErrUnknownCaller with the address.
		if errors.Is(err, ident.ErrUnknownCaller) {
			writeJSON(w, http.StatusForbidden, refusal{
				Status: http.StatusForbidden,
				Reason: "caller identity not resolvable on this tailnet",
			})
			return
		}
		log.Printf("whoami: identity lookup failed for %s: %v", r.RemoteAddr, err)
		writeJSON(w, http.StatusServiceUnavailable, refusal{
			Status: http.StatusServiceUnavailable,
			Reason: "identity lookup failed; refusing rather than guessing",
		})
		return
	}

	g, err := grant.FromCapMap(caps)
	if err != nil {
		if errors.Is(err, grant.ErrNoGrant) {
			writeJSON(w, http.StatusForbidden, refusal{
				Status: http.StatusForbidden,
				Reason: "no grant for this app: the tailnet policy gives this caller no " + string(grant.Capability) + " capability",
				Caller: &caller,
			})
			return
		}
		writeJSON(w, http.StatusForbidden, refusal{
			Status: http.StatusForbidden,
			Reason: err.Error(),
			Caller: &caller,
		})
		return
	}

	writeJSON(w, http.StatusOK, whoamiResponse{
		Caller: caller,
		Grant:  g,
	})
}
