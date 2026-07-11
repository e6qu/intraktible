// SPDX-License-Identifier: AGPL-3.0-or-later

// Command intraktible runs the platform. The same binary runs the full modular
// monolith or a subset of modules; modules can also run as separate services.
//
//	intraktible serve --modules=all
//
// The backend itself is assembled by the importable server package (the shared
// composition root for the native and js/wasm targets); this command owns the
// native-only shell: flag parsing, the WAL/sqlite/postgres/nats backends,
// telemetry, signal handling, and the HTTP listener.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"syscall"
	"time"

	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/telemetry"
	"github.com/e6qu/intraktible/server"
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
	devKey := fs.String("dev-api-key", "dev-sandbox-key", "seed a dev admin API key (in-memory store only; ignored with a durable store; empty to disable)")
	storeKind := fs.String("store", "memory", "projection store: memory | sqlite (<data-dir>/projections.db) | postgres (INTRAKTIBLE_POSTGRES_DSN)")
	logKind := fs.String("log", "file", "event log: file (single-process WAL) | memory (in-RAM, NOT durable; tests/wasm) | sqlite (shared across processes, for the split profile) | postgres (networked HA; INTRAKTIBLE_POSTGRES_DSN) | nats (JetStream HA; INTRAKTIBLE_NATS_URL)")
	env := fs.String("env", envOr("INTRAKTIBLE_ENV", "development"), "deployment environment: development | production (production refuses insecure config and forces secure cookies)")
	_ = fs.Parse(args)
	return run(*addr, *dataDir, *modules, *devKey, *storeKind, *logKind, *env)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(addr, dataDir, modules, devKey, storeKind, logKind, env string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Distributed tracing. Off unless INTRAKTIBLE_OTEL_EXPORTER is set; the shutdown
	// flushes buffered spans on a bounded context so a clean exit doesn't drop them.
	shutdownTracing, err := telemetry.Init(ctx, buildRevision())
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(sctx)
	}()

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

	// The shared composition root assembles the whole backend on the selected
	// log/store; this shell only wraps it with the listener + signal-driven
	// shutdown. Close is deferred after the log/store closers so (LIFO) the
	// agent workers drain before the log closes.
	backend, err := server.New(ctx, server.Config{Modules: modules, DevAPIKey: devKey, StoreKind: storeKind, LogKind: logKind, Env: env}, log, st)
	if err != nil {
		return err
	}
	defer backend.Close()

	// ReadHeaderTimeout + ReadTimeout + IdleTimeout bound slow/idle clients
	// (Slowloris). WriteTimeout is deliberately unset: the SSE/WebSocket streaming
	// routes (/decide/stream, agent run streams) are long-lived and a global write
	// deadline would cut them; those handlers carry their own per-request deadlines.
	srv := &http.Server{
		Addr:              addr,
		Handler:           backend.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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
	// Give in-flight requests (a decide fanning out to connectors/AI, a draining SSE
	// stream) time to finish before the listener closes. Tunable so an operator can
	// match it to the orchestrator's terminationGracePeriod.
	timeout := 30 * time.Second
	if v := os.Getenv("INTRAKTIBLE_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
		}
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// buildRevision returns the VCS revision embedded at build time (matching what
// /version reports), used as the tracing resource's service.version.
func buildRevision() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
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
// shared SQLite log lets one box's split-services profile share an ordered log;
// the Postgres log is the networked backbone for true multi-node HA (every node
// appends to and reads from one database).
func openLog(kind, dataDir string) (eventlog.Log, error) {
	switch kind {
	case "", "file":
		return eventlog.OpenWAL(dataDir)
	case "memory":
		// Unlike the memory STORE (ephemeral by design, rebuilt from the log), a
		// memory LOG is the source of truth — losing it loses everything.
		slog.Warn("event log is in-memory: events are NOT durable and are lost on exit (tests/wasm only)")
		return eventlog.NewMemory(), nil
	case "sqlite":
		return eventlog.OpenSQLiteLog(dataDir, eventlog.DefaultPollInterval)
	case "postgres":
		dsn := os.Getenv("INTRAKTIBLE_POSTGRES_DSN")
		if dsn == "" {
			return nil, fmt.Errorf("--log=postgres requires INTRAKTIBLE_POSTGRES_DSN")
		}
		return eventlog.OpenPostgresLog(context.Background(), dsn, eventlog.DefaultPollInterval)
	case "nats":
		url := os.Getenv("INTRAKTIBLE_NATS_URL")
		if url == "" {
			return nil, fmt.Errorf("--log=nats requires INTRAKTIBLE_NATS_URL (a JetStream-enabled server)")
		}
		return eventlog.OpenNATSLog(url)
	default:
		return nil, fmt.Errorf("unknown --log %q (file|memory|sqlite|postgres|nats)", kind)
	}
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
	rt := projection.New(log, st, server.Projectors(*modules)...)
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
		recs, err := st.List(context.Background(), c, "") // whole collection: count every tenant's docs
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
