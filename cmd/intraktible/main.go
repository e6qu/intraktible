// SPDX-License-Identifier: AGPL-3.0-or-later

// Command intraktible runs the platform. The same binary runs the full modular
// monolith or a subset of modules; modules can also run as separate services.
//
//	intraktible serve --modules=all
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/e6qu/intraktible/decision-engine/assertions"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	hellocmd "github.com/e6qu/intraktible/hello/command"
	helloservice "github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/openapi"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/web"
)

// asyncRunWorkers is the size of the Agent Manager's async-run worker pool.
const asyncRunWorkers = 4

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
	case "export":
		err = exportCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: intraktible <serve|log|replay|export> [flags]")
	os.Exit(2)
}

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	modules := fs.String("modules", "all", "comma-separated modules (or 'all')")
	devKey := fs.String("dev-api-key", "dev-sandbox-key", "seed a sandbox API key for local dev (empty to disable)")
	storeKind := fs.String("store", "memory", "projection store: memory | sqlite (<data-dir>/projections.db) | postgres (INTRAKTIBLE_POSTGRES_DSN)")
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

	st, err := openStore(ctx, storeKind, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	keyring := auth.NewKeyring()
	// Sessions live in the projection store, so they persist across restarts when
	// --store=sqlite (and stay in-memory with --store=memory). It is not a
	// projection, so a rebuild never touches it.
	sessions := auth.NewStoreSessions(st)
	apiKeys := auth.NewStoreAPIKeys(st)
	keyring.UseResolver(apiKeys)
	if devKey != "" {
		keyring.Add(devKey, auth.APIKey{
			ID:       "dev",
			Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
			Scope:    auth.Sandbox,
			Role:     auth.RoleAdmin,
		})
		slog.Warn("seeded dev API key — do not use in production", "scope", auth.Sandbox, "role", auth.RoleAdmin)
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
	connectorSecrets, err := connectorSecretBoxFromEnv()
	if err != nil {
		return err
	}
	if connectorSecrets != nil {
		slog.Info("connectors: credential-field encryption enabled")
	}

	// Agents that declare tools call Context Layer connectors through this toolbox
	// during a tool-calling run (shared by the Agent Manager and the engine's AI node).
	connectorProvider := connectors.Provider{Store: st, Egress: egress, Secrets: connectorSecrets}
	toolbox := tools.ConnectorToolbox{Fetcher: connectorProvider}

	if enabled(modules, "hello") {
		helloservice.New(hellocmd.NewHandler(log), st).Routes(api)
	}
	var monitorScheduler *monitor.Scheduler
	if enabled(modules, "decision-engine") {
		// A decision can fold in a Context Layer entity's features, call Context
		// Layer connectors from Connect nodes, and run Agent Manager agents from AI
		// nodes; each provider reads the shared store (a no-op when that module is
		// not running / nothing is defined).
		decide := enginecmd.NewDecideHandler(log, st,
			enginecmd.WithFeatures(features.Provider{Store: st}),
			enginecmd.WithConnectors(connectorProvider),
			enginecmd.WithAgents(agents.Provider{Store: st, Registry: aiRegistry, Tools: toolbox}))
		// The pre-approval write side is shared: the engine service uses it to
		// promote an approved batch into grants; the pre-approval service exposes
		// the standalone grant/list/revoke surface.
		paCmd := preapproval.NewHandler(log)
		engineservice.New(enginecmd.NewHandler(log), decide, paCmd, st).Routes(api)
		// Policies are the operational disposition layer over flows (auto-approve/
		// decline/refer); a first-class artifact alongside the flow registry.
		policy.New(policy.NewHandler(log), st).Routes(api)
		// Pre-approvals: durable pre-decisions honored instantly at decide time.
		preapproval.New(paCmd, st).Routes(api)
		// Webhooks: outbound notification channel. Delivery reuses the connector
		// egress guard (SSRF-safe) so a monitor check can push firing rules out.
		notify.New(notify.NewHandler(log), st).Routes(api)
		notifier := notify.NewNotifier(log, st, egress.Client(15*time.Second))
		// Monitors: thresholds over a flow's metrics, evaluated live (failure/refer
		// rate, automation rate, latency, volume); a check pushes firing rules to webhooks.
		monCmd := monitor.NewHandler(log)
		monitor.New(monCmd, st, notifier).Routes(api)
		monitorScheduler = monitor.NewScheduler(st, monCmd, notifier)
		// Flow assertions: input→expected test cases, run through the pure core and
		// used as a pre-promote gate.
		assertions.New(assertions.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "case-manager") {
		caseservice.New(casecmd.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "context-layer") {
		contextservice.New(contextcmd.NewHandler(log), st,
			contextservice.WithEgress(egress),
			contextservice.WithSecrets(connectorSecrets),
		).Routes(api)
	}
	var agentHandler *agentcmd.Handler
	if enabled(modules, "agent-manager") {
		agentHandler = agentcmd.NewHandler(log, st, aiRegistry, agentcmd.WithToolbox(toolbox))
		// Async agent runs: a worker pool drains the queue. DrainWorkers is deferred
		// so (LIFO, after the log-close defer) it runs first — in-flight runs finish
		// on shutdown before the log closes.
		agentHandler.StartWorkers(ctx, asyncRunWorkers)
		defer agentHandler.DrainWorkers()
		agentservice.New(agentHandler, st).Routes(api)
	}

	// Audit surface (platform capability, independent of the enabled modules): a
	// tenant-scoped, filterable, exportable read over the event log.
	audit.New(log).Routes(api)

	// Privacy: per-workspace sensitive-field masking, applied at read boundaries
	// (decision history/exports). A platform capability, independent of modules.
	privacy.New(privacy.NewHandler(log), st).Routes(api)

	// Comments: general discussion threads attached to any subject (deployment
	// requests, decisions, cases) so workflow surfaces carry an explanation trail.
	comments.New(comments.NewHandler(log), st).Routes(api)

	// Notifications: a per-user inbox derived from @-mentions in comments.
	notifications.New(notifications.NewHandler(log), st).Routes(api)

	// Authenticated caller introspection (inside the /v1 auth chain).
	httpx.NewAPIKeysHandler(apiKeys, log).Routes(api)
	api.HandleFunc("GET /v1/me", httpx.MeHandler())

	rt := projection.New(log, st, moduleProjectors(modules)...)
	if err := rt.Start(ctx); err != nil {
		return fmt.Errorf("projection start: %w", err)
	}
	// Recover async runs left "running" by a previous crash/shutdown — only after
	// the projections are rebuilt, so a worker can resolve the agent from the store.
	if agentHandler != nil {
		if n, err := agentHandler.RecoverRunning(ctx); err != nil {
			return fmt.Errorf("agent-manager: recover running runs: %w", err)
		} else if n > 0 {
			slog.Info("agent-manager: re-enqueued interrupted runs", "count", n)
		}
	}
	// Monitor scheduler: if INTRAKTIBLE_MONITOR_INTERVAL is set (e.g. "1m"), sweep
	// monitors on that cadence and push firing-edge transitions to webhooks. Off by
	// default — the /monitors/check endpoint is the on-demand alternative.
	if monitorScheduler != nil {
		if iv := os.Getenv("INTRAKTIBLE_MONITOR_INTERVAL"); iv != "" {
			d, err := time.ParseDuration(iv)
			if err != nil || d <= 0 {
				return fmt.Errorf("INTRAKTIBLE_MONITOR_INTERVAL %q: must be a positive duration", iv)
			}
			go monitorScheduler.Run(ctx, d)
		}
	}

	// The API contract (OpenAPI 3.1) + a reference page, served publicly so
	// integrators and code generators can fetch it without a key.
	openapi.Routes(root)

	// /healthz reflects projection health: degraded (503) if a live apply error
	// stopped the consumer, so an orchestrator does not keep routing to a node
	// serving stale read models.
	root.HandleFunc("GET /healthz", httpx.Health(rt.Err))

	// Public auth endpoints — exchange an API key for a session cookie (and clear
	// it). Registered on root with exact patterns so they win over the /v1/ chain.
	root.HandleFunc("POST /v1/login", httpx.LoginHandler(keyring, sessions))
	root.HandleFunc("POST /v1/logout", httpx.LogoutHandler(sessions))
	root.Handle("/v1/", httpx.Chain(api, httpx.Authenticate(keyring, sessions), httpx.Authorize))
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
// (still rebuilt from the log, which resets collections first so it stays correct);
// postgres is the durable/shared option (DSN from INTRAKTIBLE_POSTGRES_DSN).
func openStore(ctx context.Context, kind, dataDir string) (store.Store, error) {
	switch kind {
	case "", "memory":
		return store.NewMemory(), nil
	case "sqlite":
		return store.NewSQLite(filepath.Join(dataDir, "projections.db"))
	case "postgres":
		dsn := os.Getenv("INTRAKTIBLE_POSTGRES_DSN")
		if dsn == "" {
			return nil, fmt.Errorf("--store=postgres requires INTRAKTIBLE_POSTGRES_DSN")
		}
		return store.NewPostgres(ctx, dsn)
	default:
		return nil, fmt.Errorf("unknown --store %q (memory|sqlite|postgres)", kind)
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

// connectorSecretBoxFromEnv builds the connector secret keyring. The primary
// (encrypting) key is INTRAKTIBLE_CONNECTOR_SECRET_KEY; optional prior keys for
// decrypting already-sealed values during a rotation are a comma-separated list
// in INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS. Returns nil when no key is set.
func connectorSecretBoxFromEnv() (*connectors.Keyring, error) {
	raw := strings.TrimSpace(os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEY"))
	if raw == "" {
		return nil, nil
	}
	primary, err := decodeConnectorSecretKey(raw)
	if err != nil {
		return nil, err
	}
	keys := [][]byte{primary}
	for _, p := range strings.Split(os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		prev, err := decodeConnectorSecretKey(p)
		if err != nil {
			return nil, fmt.Errorf("INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS: %w", err)
		}
		keys = append(keys, prev)
	}
	return connectors.NewKeyring(keys...)
}

func decodeConnectorSecretKey(raw string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		key, err := decode(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, fmt.Errorf("INTRAKTIBLE_CONNECTOR_SECRET_KEY must be a 32-byte key encoded as base64 or hex")
}

// moduleProjectors returns the read-model projectors for the enabled modules —
// the single source of truth shared by `serve` (live projections) and `replay`
// (rebuild from the log).
func moduleProjectors(modules string) []projection.Projector {
	// Privacy masking config is a platform capability, projected regardless of
	// which modules are enabled (so masking works in every profile).
	ps := []projection.Projector{privacy.Projector{}, comments.Projector{}, notifications.Projector{}}
	if enabled(modules, "hello") {
		ps = append(ps, stats.Projector{})
	}
	if enabled(modules, "decision-engine") {
		ps = append(ps, flows.Projector{}, history.Projector{}, analytics.Projector{}, policy.Projector{}, preapproval.Projector{}, monitor.Projector{}, notify.Projector{}, assertions.Projector{}, shadow.Projector{})
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

// exportCmd rebuilds the relevant projection from the log and renders to stdout:
// a flow (by id or slug) as mermaid | mermaid-state | bpmn | dot | json, or a
// recorded decision run (by id) as mermaid | dot | json.
func exportCmd(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "event-log data directory")
	org := fs.String("org", "demo", "tenant org")
	workspace := fs.String("workspace", "main", "tenant workspace")
	flow := fs.String("flow", "", "flow id or slug to export")
	decision := fs.String("decision", "", "decision id to export (a recorded run)")
	format := fs.String("format", "mermaid", "flows: mermaid|mermaid-state|bpmn|dot|json — runs: mermaid|dot|json")
	version := fs.Int("version", 0, "flow version to export (0 = latest)")
	_ = fs.Parse(args)
	if (*flow == "") == (*decision == "") {
		return fmt.Errorf("export: exactly one of --flow <id-or-slug> or --decision <id> is required")
	}

	ctx := context.Background()
	log, err := eventlog.OpenWAL(*dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	st := store.NewMemory()
	id := identity.Identity{Org: *org, Workspace: *workspace, Actor: "export"}

	if *decision != "" {
		if _, err := projection.New(log, st, history.Projector{}).RebuildTo(ctx, 0); err != nil {
			return err
		}
		rec, found, err := history.Read(ctx, st, id, *decision)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("export: no decision %q for %s/%s", *decision, *org, *workspace)
		}
		out, err := renderRun(rec, *format)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}

	if _, err := projection.New(log, st, flows.Projector{}).RebuildTo(ctx, 0); err != nil {
		return err
	}
	fv, found, err := findFlow(ctx, st, id, *flow)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("export: no flow with id or slug %q for %s/%s", *flow, *org, *workspace)
	}
	ver, err := pickFlowVersion(fv, *version)
	if err != nil {
		return err
	}
	out, err := renderFlow(fv, ver, *format)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// renderRun renders a recorded decision to the requested run format.
func renderRun(rec history.Record, format string) (string, error) {
	steps := make([]export.RunStep, 0, len(rec.Nodes))
	for _, n := range rec.Nodes {
		steps = append(steps, export.RunStep{NodeID: n.NodeID, Type: string(n.Type)})
	}
	switch format {
	case "mermaid", "sequence":
		return export.MermaidSequence(rec.Slug, steps, rec.Status), nil
	case "dot", "graphviz":
		return export.RunDOT(rec.Slug, steps, rec.Status), nil
	case "json":
		b, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b) + "\n", nil
	default:
		return "", fmt.Errorf("export: unknown run format %q (mermaid|dot|json)", format)
	}
}

// findFlow looks a flow up by id, falling back to a slug match.
func findFlow(ctx context.Context, st store.Store, id identity.Identity, ref string) (flows.FlowView, bool, error) {
	if fv, ok, err := flows.Read(ctx, st, id, ref); err != nil || ok {
		return fv, ok, err
	}
	all, err := flows.List(ctx, st, id)
	if err != nil {
		return flows.FlowView{}, false, err
	}
	for _, fv := range all {
		if fv.Slug == ref {
			return fv, true, nil
		}
	}
	return flows.FlowView{}, false, nil
}

func pickFlowVersion(fv flows.FlowView, version int) (flows.VersionView, error) {
	want := fv.Latest
	if version != 0 {
		want = version
	}
	for _, v := range fv.Versions {
		if v.Version == want {
			return v, nil
		}
	}
	return flows.VersionView{}, fmt.Errorf("export: flow %q has no version %d", fv.Slug, want)
}

func renderFlow(fv flows.FlowView, ver flows.VersionView, format string) (string, error) {
	switch format {
	case "mermaid", "flowchart":
		return export.MermaidFlowchart(ver.Graph), nil
	case "mermaid-state", "state":
		return export.MermaidState(ver.Graph), nil
	case "bpmn":
		return export.BPMN(ver.Graph, fv.Name), nil
	case "dot", "graphviz":
		return export.DOT(ver.Graph), nil
	case "json":
		return export.JSON(export.FlowExport{
			Slug: fv.Slug, Name: fv.Name, Version: ver.Version, Etag: ver.Etag,
			Graph: ver.Graph, InputSchema: ver.InputSchema,
		})
	default:
		return "", fmt.Errorf("export: unknown format %q (mermaid|mermaid-state|bpmn|dot|json)", format)
	}
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
