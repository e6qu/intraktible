// SPDX-License-Identifier: AGPL-3.0-or-later

// Command intraktible runs the platform. The same binary runs the full modular
// monolith or a subset of modules; modules can also run as separate services.
//
//	intraktible serve --modules=all
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	agentcmd "github.com/e6qu/intraktible/agent-manager/command"
	agentservice "github.com/e6qu/intraktible/agent-manager/service"
	"github.com/e6qu/intraktible/agent-manager/tools"
	"github.com/e6qu/intraktible/case-manager/cases"
	casecmd "github.com/e6qu/intraktible/case-manager/command"
	caseservice "github.com/e6qu/intraktible/case-manager/service"
	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	contextservice "github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	hellocmd "github.com/e6qu/intraktible/hello/command"
	helloservice "github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serveCmd(os.Args[2:])
	case "log":
		err = logCmd(os.Args[2:])
	case "replay":
		err = replayCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intraktible <serve|log|replay> [flags]")
	os.Exit(2)
}

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	modules := fs.String("modules", "all", "comma-separated modules (or 'all')")
	devKey := fs.String("dev-api-key", "dev-sandbox-key", "seed a sandbox API key for local dev (empty to disable)")
	storeKind := fs.String("store", "memory", "projection store: memory | sqlite (sqlite persists to <data-dir>/projections.db)")
	logKind := fs.String("log", "file", "event log: file (single-process WAL) | sqlite (shared across processes, for the split profile)")
	_ = fs.Parse(args)
	return run(*addr, *dataDir, *modules, *devKey, *storeKind, *logKind)
}

func run(addr, dataDir, modules, devKey, storeKind, logKind string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log, err := openLog(logKind, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	st, err := openStore(storeKind, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	keyring := auth.NewKeyring()
	// Sessions live in the projection store, so they persist across restarts when
	// --store=sqlite (and stay in-memory with --store=memory). It is not a
	// projection, so a rebuild never touches it.
	sessions := auth.NewStoreSessions(st)
	if devKey != "" {
		keyring.Add(devKey, auth.APIKey{
			ID:       "dev",
			Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
			Scope:    auth.Sandbox,
		})
		slog.Warn("seeded dev API key — do not use in production", "scope", auth.Sandbox)
	}

	root := http.NewServeMux()
	root.Handle("/", web.Handler())

	api := http.NewServeMux()

	// The AI provider registry is shared by the Agent Manager and the decision
	// engine's AI node. When INTRAKTIBLE_AI_BASE_URL is set, a real OpenAI-compatible
	// HTTP provider is registered (and becomes the default); the Stub is always
	// available as a fallback for dev/tests.
	aiRegistry := ai.NewRegistry()
	if base := os.Getenv("INTRAKTIBLE_AI_BASE_URL"); base != "" {
		name := os.Getenv("INTRAKTIBLE_AI_PROVIDER")
		if name == "" {
			name = "openai"
		}
		aiRegistry.Register(ai.NewHTTP(name, base, os.Getenv("INTRAKTIBLE_AI_API_KEY"), os.Getenv("INTRAKTIBLE_AI_MODEL")))
		slog.Info("ai: registered HTTP provider", "name", name, "model", os.Getenv("INTRAKTIBLE_AI_MODEL"))
	}
	aiRegistry.Register(ai.Stub{})

	// HTTP connectors dial operator-configured URLs; the default egress policy
	// blocks loopback/private targets (SSRF guard). Operators whose connectors
	// legitimately reach internal hosts opt in with INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE.
	egress := connectors.EgressPolicy{AllowPrivate: truthy(os.Getenv("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE"))}
	if egress.AllowPrivate {
		slog.Warn("connectors: egress to private/loopback targets is ALLOWED (INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE)")
	}

	// Agents that declare tools call Context Layer connectors through this toolbox
	// during a tool-calling run (shared by the Agent Manager and the engine's AI node).
	toolbox := tools.ConnectorToolbox{Fetcher: connectors.Provider{Store: st, Egress: egress}}

	if enabled(modules, "hello") {
		helloservice.New(hellocmd.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "decision-engine") {
		// A decision can fold in a Context Layer entity's features, call Context
		// Layer connectors from Connect nodes, and run Agent Manager agents from AI
		// nodes; each provider reads the shared store (a no-op when that module is
		// not running / nothing is defined).
		decide := enginecmd.NewDecideHandler(log, st,
			enginecmd.WithFeatures(features.Provider{Store: st}),
			enginecmd.WithConnectors(connectors.Provider{Store: st, Egress: egress}),
			enginecmd.WithAgents(agents.Provider{Store: st, Registry: aiRegistry, Tools: toolbox}))
		engineservice.New(enginecmd.NewHandler(log), decide, st).Routes(api)
	}
	if enabled(modules, "case-manager") {
		caseservice.New(casecmd.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "context-layer") {
		contextservice.New(contextcmd.NewHandler(log), st, contextservice.WithEgress(egress)).Routes(api)
	}
	if enabled(modules, "agent-manager") {
		agentservice.New(agentcmd.NewHandler(log, st, aiRegistry, agentcmd.WithToolbox(toolbox)), st).Routes(api)
	}

	// Authenticated caller introspection (inside the /v1 auth chain).
	api.HandleFunc("GET /v1/me", httpx.MeHandler())

	rt := projection.New(log, st, moduleProjectors(modules)...)
	if err := rt.Start(ctx); err != nil {
		return fmt.Errorf("projection start: %w", err)
	}
	// /healthz reflects projection health: degraded (503) if a live apply error
	// stopped the consumer, so an orchestrator does not keep routing to a node
	// serving stale read models.
	root.HandleFunc("GET /healthz", httpx.Health(rt.Err))

	// Public auth endpoints — exchange an API key for a session cookie (and clear
	// it). Registered on root with exact patterns so they win over the /v1/ chain.
	root.HandleFunc("POST /v1/login", httpx.LoginHandler(keyring, sessions))
	root.HandleFunc("POST /v1/logout", httpx.LogoutHandler(sessions))
	root.Handle("/v1/", httpx.Chain(api, httpx.Authenticate(keyring, sessions)))
	handler := httpx.Chain(root, httpx.Recover, httpx.RequestID, httpx.Logger)

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "modules", modules)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case err := <-errc:
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// openStore builds the projection store. memory is ephemeral (rebuilt from the
// log at boot); sqlite persists to <data-dir>/projections.db and survives restarts
// (still rebuilt from the log, which resets collections first so it stays correct).
func openStore(kind, dataDir string) (store.Store, error) {
	switch kind {
	case "", "memory":
		return store.NewMemory(), nil
	case "sqlite":
		return store.NewSQLite(filepath.Join(dataDir, "projections.db"))
	default:
		return nil, fmt.Errorf("unknown --store %q (memory|sqlite)", kind)
	}
}

// openLog selects the event-log backend. The file WAL is single-process; the
// shared SQLite log lets the split-services profile (one process per module) all
// append to and read from one ordered log.
func openLog(kind, dataDir string) (eventlog.Log, error) {
	switch kind {
	case "", "file":
		return eventlog.OpenWAL(dataDir)
	case "sqlite":
		return eventlog.OpenSQLiteLog(dataDir, eventlog.DefaultPollInterval)
	default:
		return nil, fmt.Errorf("unknown --log %q (file|sqlite)", kind)
	}
}

// moduleProjectors returns the read-model projectors for the enabled modules —
// the single source of truth shared by `serve` (live projections) and `replay`
// (rebuild from the log).
func moduleProjectors(modules string) []projection.Projector {
	var ps []projection.Projector
	if enabled(modules, "hello") {
		ps = append(ps, stats.Projector{})
	}
	if enabled(modules, "decision-engine") {
		ps = append(ps, flows.Projector{}, history.Projector{}, analytics.Projector{})
	}
	if enabled(modules, "case-manager") {
		ps = append(ps, cases.Projector{})
	}
	if enabled(modules, "context-layer") {
		ps = append(ps, entities.Projector{}, features.Projector{}, connectors.Projector{})
	}
	if enabled(modules, "agent-manager") {
		ps = append(ps, agents.Projector{})
	}
	return ps
}

// logCmd prints the durable event log (one line per event) and a summary —
// the operator's audit view of the backbone.
func logCmd(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	_ = fs.Parse(args)

	log, err := eventlog.OpenWAL(*dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	events, err := log.Read(context.Background(), 0)
	if err != nil {
		return err
	}
	byStream := map[string]int{}
	for _, e := range events {
		byStream[e.Stream]++
		fmt.Printf("%6d  %-26s  %-30s  %s/%s  %s\n",
			e.Seq, e.Time.UTC().Format(time.RFC3339), e.Type, e.Org, e.Workspace, e.Actor)
	}
	fmt.Printf("\n%d events, head seq %d\n", len(events), log.Head())
	for _, s := range sortedKeys(byStream) {
		fmt.Printf("  %-16s %d\n", s, byStream[s])
	}
	return nil
}

// replayCmd rebuilds the enabled modules' projections from the log into a fresh
// store — optionally as of an earlier seq (log-based rollback / time-travel) — and
// reports the rebuilt state. It mutates nothing: the append-only log is read-only.
func replayCmd(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	modules := fs.String("modules", "all", "comma-separated modules (or 'all')")
	asOf := fs.Uint64("as-of", 0, "rebuild as of this event seq (0 = the whole log)")
	_ = fs.Parse(args)

	log, err := eventlog.OpenWAL(*dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	st := store.NewMemory()
	rt := projection.New(log, st, moduleProjectors(*modules)...)
	applied, err := rt.RebuildTo(context.Background(), *asOf)
	if err != nil {
		return err
	}
	fmt.Printf("replayed %d events (as-of seq %d, head %d); rebuilt collections:\n", applied, *asOf, log.Head())
	cols := st.Collections()
	if len(cols) == 0 {
		fmt.Println("  (none)")
	}
	for _, c := range cols {
		recs, err := st.List(context.Background(), c)
		if err != nil {
			return err
		}
		fmt.Printf("  %-26s %d docs\n", c, len(recs))
	}
	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// enabled reports whether module m should run given the --modules selection.
func enabled(modules, m string) bool {
	if modules == "all" || modules == "" {
		return true
	}
	for _, part := range splitComma(modules) {
		if part == m {
			return true
		}
	}
	return false
}

// truthy reports whether an env value reads as enabled (1/true/yes/on).
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}
