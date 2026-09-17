// Command taillow is an LLM gateway that joins a tailnet as its own node.
//
// It asks the tailnet who is calling, reads what the tailnet policy grants
// them, and only then forwards a prompt to a model. Every request leaves one
// audit line, and every refusal says why.
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

	"github.com/13inks/taillow/internal/audit"
	"github.com/13inks/taillow/internal/budget"
	"github.com/13inks/taillow/internal/grant"
	"github.com/13inks/taillow/internal/ident"
	"github.com/13inks/taillow/internal/upstream"
	"github.com/anthropics/anthropic-sdk-go/option"
	"tailscale.com/tsnet"
)

type config struct {
	hostname  string
	stateDir  string
	ephemeral bool
	addr      string
	upTimeout time.Duration

	auditPath       string
	ollamaURL       string
	upstreamTimeout time.Duration
}

func main() {
	var cfg config
	flag.StringVar(&cfg.hostname, "hostname", "taillow", "machine name to register on the tailnet")
	flag.StringVar(&cfg.stateDir, "state-dir", "", "directory for the node's state (empty: tsnet picks one)")
	flag.BoolVar(&cfg.ephemeral, "ephemeral", true, "register as an ephemeral node, removed after it goes offline")
	flag.StringVar(&cfg.addr, "addr", ":80", "address to listen on, on the tailnet only")
	flag.DurationVar(&cfg.upTimeout, "up-timeout", time.Minute, "how long to wait for the node to join the tailnet")
	flag.StringVar(&cfg.auditPath, "audit-log", "taillow-audit.jsonl", "file the audit lines are appended to")
	flag.StringVar(&cfg.ollamaURL, "ollama-url", "http://127.0.0.1:11434", "base URL of the Ollama that serves every model not named claude-*")
	flag.DurationVar(&cfg.upstreamTimeout, "upstream-timeout", 2*time.Minute, "how long one upstream call may take")
	flag.Parse()

	// run returns an error instead of calling log.Fatal itself, so its
	// deferred cleanup (leaving the tailnet) always happens.
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config) error {
	// Open the audit log before joining the tailnet. A gateway that cannot
	// write its audit trail must not come up and start answering.
	auditLog, err := audit.Open(cfg.auditPath)
	if err != nil {
		return err
	}
	defer auditLog.Close()

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

	// The Anthropic key is read from the environment by the SDK, for the same
	// reason the auth key is: a secret on the command line is a secret in
	// shell history.
	g := &gateway{
		who:    lc,
		ledger: budget.New(nil),
		audit:  auditLog,
		router: upstream.Router{
			Anthropic: upstream.NewAnthropic(option.WithRequestTimeout(cfg.upstreamTimeout)),
			Ollama: &upstream.Ollama{
				BaseURL: cfg.ollamaURL,
				Client:  &http.Client{Timeout: cfg.upstreamTimeout},
			},
		},
	}

	httpSrv := &http.Server{
		Handler:           newMux(g),
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

// gateway is everything a request needs. The fields are interfaces or small
// concrete types so a test can build one without a tailnet or a network.
type gateway struct {
	who    ident.WhoIser
	ledger *budget.Ledger
	audit  *audit.Log
	router upstream.Router
}

// newMux builds the routes. The method in each pattern makes the standard
// library answer other methods with 405 and an Allow header.
func newMux(g *gateway) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /whoami", g.whoamiHandler)
	mux.HandleFunc("POST /v1/complete", g.completeHandler)
	return mux
}

// healthz reports that the process is up and serving.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// refusal is the body of every 4xx and 5xx. Reason is always set: a refusal
// that does not say why is as hard to debug as a silent success.
type refusal struct {
	Status   int           `json:"status"`
	Reason   string        `json:"reason"`
	Caller   *ident.Caller `json:"caller,omitempty"`
	Budget   *budgetInfo   `json:"budget,omitempty"`
	Upstream *upstreamInfo `json:"upstream,omitempty"`
}

// budgetInfo tells a caller who hit their limit which identity was counted,
// what the limit is and when it resets, so "429" is never the whole story.
type budgetInfo struct {
	Identity    string    `json:"identity"`
	DailyTokens int64     `json:"dailyTokens"`
	Remaining   int64     `json:"remaining"`
	ResetAt     time.Time `json:"resetAt"`
}

// upstreamInfo carries the upstream's own status. A 502 that hides whether the
// model provider said 401, 429 or 529 sends people to debug the wrong system.
type upstreamInfo struct {
	Provider string `json:"provider"`
	Status   int    `json:"status"`
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

// authorize answers the two questions every identity-checked route starts
// with: who is calling, and what does the tailnet policy grant them. A non-nil
// refusal is the complete answer to send. The caller is echoed on a grant
// refusal because it is their own identity, and knowing who the tailnet thinks
// they are is how they fix it.
func authorize(ctx context.Context, who ident.WhoIser, remoteAddr string) (ident.Caller, grant.Grant, *refusal) {
	caller, caps, err := ident.Resolve(ctx, who, remoteAddr)
	if err != nil {
		// errors.Is, not ==: Resolve wraps ErrUnknownCaller with the address.
		if errors.Is(err, ident.ErrUnknownCaller) {
			return ident.Caller{}, grant.Grant{}, &refusal{
				Status: http.StatusForbidden,
				Reason: "caller identity not resolvable on this tailnet",
			}
		}
		log.Printf("identity lookup failed for %s: %v", remoteAddr, err)
		return ident.Caller{}, grant.Grant{}, &refusal{
			Status: http.StatusServiceUnavailable,
			Reason: "identity lookup failed; refusing rather than guessing",
		}
	}

	g, err := grant.FromCapMap(caps)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, grant.ErrNoGrant) {
			reason = "no grant for this app: the tailnet policy gives this caller no " + string(grant.Capability) + " capability"
		}
		return caller, grant.Grant{}, &refusal{
			Status: http.StatusForbidden,
			Reason: reason,
			Caller: &caller,
		}
	}
	return caller, g, nil
}

// whoamiHandler reports the caller's identity and grant, or refuses with a
// reason. It spends nothing and calls no model, so it is how someone checks
// their access before using it.
func (g *gateway) whoamiHandler(w http.ResponseWriter, r *http.Request) {
	caller, gr, ref := authorize(r.Context(), g.who, r.RemoteAddr)
	if ref != nil {
		writeJSON(w, ref.Status, ref)
		return
	}
	writeJSON(w, http.StatusOK, whoamiResponse{Caller: caller, Grant: gr})
}
