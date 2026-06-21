// SPDX-License-Identifier: AGPL-3.0-or-later

// Command intraktible runs the platform. The same binary runs the full modular
// monolith or a subset of modules; modules can also run as separate services.
//
//	intraktible serve --modules=all
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
	"strconv"
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
	"github.com/e6qu/intraktible/decision-engine/grants"
	"github.com/e6qu/intraktible/decision-engine/history"
	enginemodels "github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/decision-engine/schedule"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	hellocmd "github.com/e6qu/intraktible/hello/command"
	helloservice "github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/kms"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/openapi"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/scim"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/telemetry"
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
	devKey := fs.String("dev-api-key", "dev-sandbox-key", "seed a dev admin API key (in-memory store only; ignored with a durable store; empty to disable)")
	storeKind := fs.String("store", "memory", "projection store: memory | sqlite (<data-dir>/projections.db) | postgres (INTRAKTIBLE_POSTGRES_DSN)")
	logKind := fs.String("log", "file", "event log: file (single-process WAL) | sqlite (shared across processes, for the split profile) | postgres (networked HA; INTRAKTIBLE_POSTGRES_DSN) | nats (JetStream HA; INTRAKTIBLE_NATS_URL)")
	_ = fs.Parse(args)
	return run(*addr, *dataDir, *modules, *devKey, *storeKind, *logKind)
}

func run(addr, dataDir, modules, devKey, storeKind, logKind string) error {
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

	// Encryption at rest: when INTRAKTIBLE_ENCRYPTION_KEY is set, event payloads and
	// projection-store documents are sealed under the keyring (AES-256-GCM). Off by
	// default; the keyring is built once and shared by the log and store wrappers.
	// Keys must be retained — losing one makes everything sealed under it unreadable.
	atRest, err := secretbox.KeyringFromKeys(
		os.Getenv("INTRAKTIBLE_ENCRYPTION_KEY"),
		splitCSV(os.Getenv("INTRAKTIBLE_ENCRYPTION_KEYS_PREVIOUS"))...,
	)
	if err != nil {
		return fmt.Errorf("encryption-at-rest: %w", err)
	}
	if atRest != nil {
		slog.Info("encryption at rest enabled (event payloads + projection store)")
	}

	log, err := openLog(logKind, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	log = eventlog.Encrypted(log, atRest)

	st, err := openStore(ctx, storeKind, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	st = store.Encrypted(st, atRest)
	keyring := auth.NewKeyring()
	// Sessions live in the projection store, so they persist across restarts when
	// --store=sqlite (and stay in-memory with --store=memory). It is not a
	// projection, so a rebuild never touches it.
	sessions := auth.NewStoreSessions(st)
	apiKeys := auth.NewStoreAPIKeys(st)
	keyring.UseResolver(apiKeys)
	if seedDevKey(keyring, devKey, storeKind) {
		slog.Warn("seeded dev API key (in-memory store, local dev only)", "scope", auth.ScopeAll, "role", auth.RoleAdmin)
	} else if devKey != "" {
		slog.Warn("ignoring --dev-api-key: a durable store does not seed the well-known dev key; issue a managed API key instead", "store", storeKind)
	}

	root := http.NewServeMux()
	root.Handle("/", web.Handler())

	api := http.NewServeMux()

	// The AI provider registry is shared by the Agent Manager and the decision
	// engine's AI node. When INTRAKTIBLE_AI_BASE_URL is set, a real OpenAI-compatible
	// HTTP provider is registered (and becomes the default); the Stub is always
	// available as a fallback for dev/tests.
	// Guardrails wrap every registered provider (rate limit + PII redaction +
	// jailbreak/injection block), so both the Agent Manager and the Copilot are
	// covered uniformly. Inert unless configured.
	guardrails, err := aiGuardrailsFromEnv()
	if err != nil {
		return err
	}
	if guardrails.Enabled() {
		slog.Info("ai: guardrails enabled", "rate_per_sec", guardrails.RatePerSec,
			"redact_pii", guardrails.RedactPII, "block_injection", guardrails.BlockInjection)
	}
	aiRegistry := ai.NewRegistry()
	if base := os.Getenv("INTRAKTIBLE_AI_BASE_URL"); base != "" {
		name := os.Getenv("INTRAKTIBLE_AI_PROVIDER")
		if name == "" {
			name = "openai"
		}
		aiRegistry.Register(ai.Guard(ai.NewHTTP(name, base, os.Getenv("INTRAKTIBLE_AI_API_KEY"), os.Getenv("INTRAKTIBLE_AI_MODEL")), guardrails))
		slog.Info("ai: registered HTTP provider", "name", name, "model", os.Getenv("INTRAKTIBLE_AI_MODEL"))
	}
	aiRegistry.Register(ai.Guard(ai.Stub{}, guardrails))

	// HTTP connectors dial operator-configured URLs; the default egress policy
	// blocks loopback/private targets (SSRF guard). Operators whose connectors
	// legitimately reach internal hosts opt in with INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE.
	egress := connectors.EgressPolicy{AllowPrivate: truthy(os.Getenv("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE"))}
	if egress.AllowPrivate {
		slog.Warn("connectors: egress to private/loopback targets is ALLOWED (INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE)")
	}
	connectorSecrets, err := connectorSecretBoxFromEnv(ctx)
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

	// The erasure vault crypto-shreds PII: it backs the /v1/erasure admin surface
	// and seals the Context Layer's configured PII event fields per subject.
	erasureVault := erasure.NewVault(st)
	erasurePIIFields := splitCSV(os.Getenv("INTRAKTIBLE_ERASURE_PII_FIELDS"))

	if enabled(modules, "hello") {
		helloservice.New(hellocmd.NewHandler(log), st).Routes(api)
	}
	var monitorScheduler *monitor.Scheduler
	var driftScheduler *enginemodels.Scheduler
	var deployScheduler *schedule.Scheduler
	if enabled(modules, "decision-engine") {
		// A decision can fold in a Context Layer entity's features, call Context
		// Layer connectors from Connect nodes, and run Agent Manager agents from AI
		// nodes; each provider reads the shared store (a no-op when that module is
		// not running / nothing is defined).
		decideOpts := []enginecmd.DecideOption{
			enginecmd.WithFeatures(features.Provider{Store: st}),
			enginecmd.WithConnectors(connectorProvider),
			enginecmd.WithAgents(agents.Provider{Store: st, Registry: aiRegistry, Tools: toolbox}),
			enginecmd.WithModels(enginemodels.Provider{Store: st, HTTP: egress.Client(10 * time.Second)}),
		}
		// Crypto-shred recorded decision PII under the entity subject when erasure
		// fields are configured (same set as the Context Layer's event sealing).
		if len(erasurePIIFields) > 0 {
			decideOpts = append(decideOpts, enginecmd.WithPIISealer(newPIISealer(erasureVault, erasurePIIFields)))
		}
		decide := enginecmd.NewDecideHandler(log, st, decideOpts...)
		// The pre-approval write side is shared: the engine service uses it to
		// promote an approved batch into grants; the pre-approval service exposes
		// the standalone grant/list/revoke surface.
		paCmd := preapproval.NewHandler(log)
		engineCmd := enginecmd.NewHandler(log)
		engineSvc := engineservice.New(engineCmd, decide, paCmd, st)
		if len(erasurePIIFields) > 0 {
			engineSvc.UseEraser(erasureVault)
		}
		engineSvc.UseCopilot(aiCompleter{reg: aiRegistry})
		engineSvc.Routes(api)
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
		// Model drift: the same scheduler cadence sweeps every model's PSI vs its
		// configured threshold and pushes the firing edge to webhooks. The window is
		// cumulative by default; INTRAKTIBLE_MODEL_DRIFT_WINDOW (days) narrows it to a
		// recent slice so a fresh shift isn't diluted by all-time history.
		driftScheduler = enginemodels.NewScheduler(st, engineCmd, notifier, driftWindowDays())
		// Deploy scheduler: activates due scheduled deploys and reverts expired
		// time-boxed ones on the same cadence as the monitor sweep.
		deployScheduler = schedule.NewScheduler(st, engineCmd)
		// Flow assertions: input→expected test cases, run through the pure core and
		// used as a pre-promote gate.
		assertions.New(assertions.NewHandler(log), st).Routes(api)
		// Per-flow access grants: fine-grained, opt-in restriction of change-control
		// on a specific flow/environment, layered over the global RBAC roles.
		grants.New(grants.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "case-manager") {
		caseservice.New(casecmd.NewHandler(log), st).Routes(api)
	}
	if enabled(modules, "context-layer") {
		contextservice.New(contextcmd.NewHandler(log), st,
			contextservice.WithEgress(egress),
			contextservice.WithSecrets(connectorSecrets),
			contextservice.WithErasure(erasureVault, erasurePIIFields),
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
		// Per-model prices (USD per million tokens) derive run cost in the run
		// summary / observability surface; absent, only token counts are reported.
		pricing, err := agents.ParsePricing(os.Getenv("INTRAKTIBLE_AI_PRICES"))
		if err != nil {
			return err
		}
		agentservice.New(agentHandler, st, agentservice.WithPricing(pricing)).Routes(api)
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
	// Right-to-erasure (crypto-shredding) + retention, admin-gated. erasureVault is
	// built earlier and shared with the Context Layer's PII field sealing.
	erasure.NewService(erasureVault).Routes(api)
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
			// Model-drift push and the deploy scheduler share the cadence: one
			// interval drives all the timed sweeps.
			if driftScheduler != nil {
				go driftScheduler.Run(ctx, d)
			}
			if deployScheduler != nil {
				go deployScheduler.Run(ctx, d)
			}
		}
	}

	// The API contract (OpenAPI 3.1) + a reference page, served publicly so
	// integrators and code generators can fetch it without a key.
	openapi.Routes(root)

	// /healthz reflects projection health: degraded (503) if a live apply error
	// stopped the consumer, so an orchestrator does not keep routing to a node
	// serving stale read models.
	root.HandleFunc("GET /healthz", httpx.Health(rt.Err))
	// /version reports the build (VCS revision + Go) so ops can confirm what's live.
	root.HandleFunc("GET /version", httpx.Version())
	// /metrics is the Prometheus scrape endpoint (unauthenticated like /healthz —
	// aggregate operational counters only, no tenant data).
	root.Handle("GET /metrics", metrics.Handler())

	// Public auth endpoints — exchange an API key for a session cookie (and clear
	// it). Registered on root with exact patterns so they win over the /v1/ chain.
	root.HandleFunc("POST /v1/login", httpx.LoginHandler(keyring, sessions))
	root.HandleFunc("POST /v1/logout", httpx.LogoutHandler(sessions))
	// SSO: OIDC login for the configured providers (Google, AWS Cognito, …). Each
	// provider's discovery runs now; a provider that fails to initialize is skipped
	// (logged) rather than blocking startup.
	// SCIM user provisioning (the SSO companion): an IdP creates/deactivates users
	// here, and the OIDC login consults it so a deactivated user is refused.
	var scimStore *scim.Store
	if token := os.Getenv("INTRAKTIBLE_SCIM_TOKEN"); token != "" {
		scimStore = scim.NewStore(st)
		org := envOr("INTRAKTIBLE_SCIM_ORG", "demo")
		ws := envOr("INTRAKTIBLE_SCIM_WORKSPACE", "main")
		scim.NewService(scimStore, token, org, ws).Routes(root)
		slog.Info("scim: user provisioning enabled", "org", org, "workspace", ws)
	}
	// The SCIM gate + role augmenter are shared by both SSO protocols.
	var ssoGate httpx.LoginGate
	var ssoAugment httpx.RoleAugmenter
	if scimStore != nil {
		// Adapt the SCIM Store's identity-typed deprovisioning gate to the login
		// hook's (org, workspace, email) shape at the composition root.
		ssoGate = func(ctx context.Context, org, workspace, email string) bool {
			return scimStore.Allowed(ctx, identity.Identity{Org: org, Workspace: workspace}, email)
		}
		if groupRoles := parseGroupRoles(os.Getenv("INTRAKTIBLE_SCIM_GROUP_ROLES")); len(groupRoles) > 0 {
			ssoAugment = scimRoleAugmenter(scimStore, groupRoles)
		}
	}
	if authers := oidcAuthenticators(ctx); len(authers) > 0 {
		oh := httpx.NewOIDCHandler(sessions, authers...)
		oh.SetGate(ssoGate)
		oh.SetRoleAugmenter(ssoAugment)
		oh.Routes(root)
		slog.Info("sso: OIDC enabled", "providers", oidcNames(authers))
	}
	if samlers := samlAuthenticators(); len(samlers) > 0 {
		sh := httpx.NewSAMLHandler(sessions, samlers...)
		sh.SetGate(ssoGate)
		sh.SetRoleAugmenter(ssoAugment)
		sh.Routes(root)
		slog.Info("sso: SAML enabled", "providers", samlNames(samlers))
	}
	root.Handle("/v1/", httpx.Chain(api, httpx.Authenticate(keyring, sessions), httpx.AuthorizeRoutes(api)))
	handler := httpx.Chain(root, httpx.Recover, httpx.RequestID, httpx.Tracing, httpx.Logger, httpx.Metrics)

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

// aiGuardrailsFromEnv reads the AI guardrail config: per-provider rate limit
// (INTRAKTIBLE_AI_RATE_LIMIT_RPS / _BURST), free-text PII redaction
// (INTRAKTIBLE_AI_GUARDRAIL_PII), structured-output field redaction
// (INTRAKTIBLE_AI_GUARDRAIL_REDACT_FIELDS, CSV), and jailbreak/injection blocking
// (INTRAKTIBLE_AI_GUARDRAIL_BLOCK_INJECTION). All off by default.
func aiGuardrailsFromEnv() (ai.Guardrails, error) {
	g := ai.Guardrails{
		RedactPII:      truthy(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_PII")),
		RedactFields:   splitCSV(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_REDACT_FIELDS")),
		BlockInjection: truthy(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_BLOCK_INJECTION")),
	}
	if v := strings.TrimSpace(os.Getenv("INTRAKTIBLE_AI_RATE_LIMIT_RPS")); v != "" {
		rps, err := strconv.ParseFloat(v, 64)
		if err != nil || rps < 0 {
			return ai.Guardrails{}, fmt.Errorf("INTRAKTIBLE_AI_RATE_LIMIT_RPS %q: want a non-negative number", v)
		}
		g.RatePerSec = rps
	}
	if v := strings.TrimSpace(os.Getenv("INTRAKTIBLE_AI_RATE_LIMIT_BURST")); v != "" {
		b, err := strconv.Atoi(v)
		if err != nil || b < 0 {
			return ai.Guardrails{}, fmt.Errorf("INTRAKTIBLE_AI_RATE_LIMIT_BURST %q: want a non-negative integer", v)
		}
		g.Burst = b
	}
	return g, nil
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

// seedDevKey registers the well-known dev admin key on keyring, but ONLY with the
// non-durable in-memory store. Any durable store (sqlite/postgres) — the only kind a
// real deployment can use — refuses to seed it regardless of the flag value, so
// production can never boot with a known admin credential. Returns whether it seeded.
func seedDevKey(keyring *auth.Keyring, devKey, storeKind string) bool {
	if devKey == "" || storeKind != "memory" {
		return false
	}
	keyring.Add(devKey, auth.APIKey{
		ID:       "dev",
		Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
		Scope:    auth.ScopeAll, // local dev key decides against any environment
		Role:     auth.RoleAdmin,
	})
	return true
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
		return nil, fmt.Errorf("unknown --log %q (file|sqlite|postgres|nats)", kind)
	}
}

// oidcAuthenticators builds an OIDC authenticator per name in
// INTRAKTIBLE_OIDC_PROVIDERS (e.g. "google,aws"), reading each provider's config
// from INTRAKTIBLE_OIDC_<NAME>_* env vars. Discovery is a network call; a
// provider that fails to initialize is skipped, not fatal.
func oidcAuthenticators(ctx context.Context) []*auth.OIDCAuthenticator {
	raw := strings.TrimSpace(os.Getenv("INTRAKTIBLE_OIDC_PROVIDERS"))
	if raw == "" {
		return nil
	}
	var out []*auth.OIDCAuthenticator
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		a, err := auth.NewOIDCAuthenticator(ctx, oidcConfigFromEnv(name))
		if err != nil {
			slog.Warn("sso: skipping OIDC provider", "provider", name, "err", err)
			continue
		}
		out = append(out, a)
	}
	return out
}

func oidcConfigFromEnv(name string) auth.OIDCConfig {
	p := "INTRAKTIBLE_OIDC_" + strings.ToUpper(name) + "_"
	cfg := auth.OIDCConfig{
		Name:         name,
		Issuer:       os.Getenv(p + "ISSUER"),
		ClientID:     os.Getenv(p + "CLIENT_ID"),
		ClientSecret: os.Getenv(p + "CLIENT_SECRET"),
		RedirectURL:  os.Getenv(p + "REDIRECT_URL"),
		Org:          os.Getenv(p + "ORG"),
		Workspace:    os.Getenv(p + "WORKSPACE"),
		GroupsClaim:  os.Getenv(p + "GROUPS_CLAIM"),
		GroupRoles:   parseGroupRoles(os.Getenv(p + "GROUP_ROLES")),
		DefaultRole:  auth.Role(os.Getenv(p + "DEFAULT_ROLE")),
	}
	// Sensible per-provider defaults so operators set the minimum.
	if cfg.Issuer == "" && name == "google" {
		cfg.Issuer = "https://accounts.google.com"
	}
	if cfg.GroupsClaim == "" {
		switch name {
		case "google":
			cfg.GroupsClaim = "groups"
		case "aws":
			cfg.GroupsClaim = "cognito:groups"
		}
	}
	return cfg
}

// parseGroupRoles parses "group1:role1,group2:role2" into a group→role map.
func parseGroupRoles(s string) map[string]auth.Role {
	m := map[string]auth.Role{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, v, ok := strings.Cut(pair, ":"); ok {
			m[strings.TrimSpace(k)] = auth.Role(strings.TrimSpace(v))
		}
	}
	return m
}

// samlAuthenticators builds a SAML SP authenticator per name in
// INTRAKTIBLE_SAML_PROVIDERS, reading each provider's config (incl. the IdP
// metadata, SP cert, and SP key from files) from INTRAKTIBLE_SAML_<NAME>_* env.
// A provider that fails to initialize is skipped, not fatal.
func samlAuthenticators() []*auth.SAMLAuthenticator {
	raw := strings.TrimSpace(os.Getenv("INTRAKTIBLE_SAML_PROVIDERS"))
	if raw == "" {
		return nil
	}
	var out []*auth.SAMLAuthenticator
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cfg, err := samlConfigFromEnv(name)
		if err == nil {
			var a *auth.SAMLAuthenticator
			a, err = auth.NewSAMLAuthenticator(cfg)
			if err == nil {
				out = append(out, a)
				continue
			}
		}
		slog.Warn("sso: skipping SAML provider", "provider", name, "err", err)
	}
	return out
}

func samlConfigFromEnv(name string) (auth.SAMLConfig, error) {
	p := "INTRAKTIBLE_SAML_" + strings.ToUpper(name) + "_"
	idpMeta, err := readFileEnv(p + "IDP_METADATA_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	cert, err := readFileEnv(p + "CERT_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	keyPEM, err := readFileEnv(p + "KEY_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	return auth.SAMLConfig{
		Name:            name,
		EntityID:        os.Getenv(p + "ENTITY_ID"),
		ACSURL:          os.Getenv(p + "ACS_URL"),
		MetadataURL:     os.Getenv(p + "METADATA_URL"),
		IDPMetadataXML:  idpMeta,
		CertPEM:         cert,
		KeyPEM:          keyPEM,
		Org:             os.Getenv(p + "ORG"),
		Workspace:       os.Getenv(p + "WORKSPACE"),
		EmailAttribute:  os.Getenv(p + "EMAIL_ATTR"),
		GroupsAttribute: os.Getenv(p + "GROUPS_ATTR"),
		GroupRoles:      parseGroupRoles(os.Getenv(p + "GROUP_ROLES")),
		DefaultRole:     auth.Role(os.Getenv(p + "DEFAULT_ROLE")),
	}, nil
}

// readFileEnv reads the file named by an env var, or returns "" when unset.
func readFileEnv(key string) (string, error) {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path) // #nosec G304 G703 -- operator-provided config file path (env), not user input
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return string(b), nil
}

func samlNames(as []*auth.SAMLAuthenticator) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name())
	}
	return out
}

// scimRoleAugmenter raises a verified user's role to the highest role mapped
// from their SCIM group memberships (never below the token-derived base).
func scimRoleAugmenter(users *scim.Store, groupRoles map[string]auth.Role) httpx.RoleAugmenter {
	return func(ctx context.Context, org, workspace, email string, base auth.Role) auth.Role {
		names, err := users.GroupsForUser(ctx, identity.Identity{Org: org, Workspace: workspace}, email)
		if err != nil {
			return base
		}
		role := base
		for _, name := range names {
			if r, ok := groupRoles[name]; ok && r.Rank() > role.Rank() {
				role = r
			}
		}
		return role
	}
}

func oidcNames(as []*auth.OIDCAuthenticator) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name())
	}
	return out
}

// connectorSecretBoxFromEnv builds the connector secret keyring. The primary
// (encrypting) key is INTRAKTIBLE_CONNECTOR_SECRET_KEY; optional prior keys for
// decrypting already-sealed values during a rotation are a comma-separated list
// in INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS. Returns nil when no key is set.
func connectorSecretBoxFromEnv(ctx context.Context) (*connectors.Keyring, error) {
	// An external KMS (AWS/GCP), when configured, takes precedence: the key never
	// leaves the provider and the local env key is not needed.
	managed, err := kms.FromEnv(ctx)
	if err != nil {
		return nil, err
	}
	if k, ok := managed.Get(); ok {
		slog.Info("connectors: using external KMS", "provider", os.Getenv("INTRAKTIBLE_KMS_PROVIDER"))
		return connectors.NewKMSKeyring("kms:"+os.Getenv("INTRAKTIBLE_KMS_PROVIDER"), k), nil
	}
	// Otherwise a local key (base64/hex 32 bytes) seals connector credentials, with
	// any previous keys retained for decrypt during rotation. Shares the key-decoding
	// + keyring construction with encryption-at-rest (platform/secretbox).
	kr, err := secretbox.KeyringFromKeys(
		os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEY"),
		splitCSV(os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS"))...,
	)
	if err != nil {
		return nil, fmt.Errorf("connectors: %w", err)
	}
	return kr, nil
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
		ps = append(ps, flows.Projector{}, history.Projector{}, analytics.Projector{}, policy.Projector{}, preapproval.Projector{}, monitor.Projector{}, notify.Projector{}, assertions.Projector{}, shadow.Projector{}, schedule.Projector{}, grants.Projector{}, enginemodels.Projector{}, enginemodels.DriftProjector{})
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

// enabled reports whether module m should run given the --modules selection.
func enabled(modules, m string) bool {
	if modules == "all" || modules == "" {
		return true
	}
	// splitCSV trims each part + drops empties, so `--modules="a, b"` or a trailing
	// comma can't silently leave a module's routes/projectors unmounted.
	for _, part := range splitCSV(modules) {
		if part == m {
			return true
		}
	}
	return false
}

// truthy reports whether an env value reads as enabled (1/true/yes/on).
// piiSealer adapts the erasure vault + a PII field set to the decision engine's
// PIISealer port, sealing a recorded decision's PII fields under the entity
// subject. It keeps the engine free of direct erasure/privacy imports.
type piiSealer struct {
	vault  *erasure.Vault
	fields map[string]bool
}

// aiCompleter adapts the AI registry to the engine's copilot AICompleter port (a
// single system+user text completion via the default provider).
type aiCompleter struct{ reg *ai.Registry }

func (c aiCompleter) Complete(ctx context.Context, system, prompt string) (string, error) {
	p, err := c.reg.Get("")
	if err != nil {
		return "", err
	}
	resp, err := p.Complete(ctx, ai.Request{System: system, Prompt: prompt})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func (c aiCompleter) CompleteJSON(ctx context.Context, system, prompt string, schema json.RawMessage) (json.RawMessage, error) {
	p, err := c.reg.Get("")
	if err != nil {
		return nil, err
	}
	resp, err := p.Complete(ctx, ai.Request{System: system, Prompt: prompt, Schema: schema})
	if err != nil {
		return nil, err
	}
	return resp.Structured, nil
}

func newPIISealer(v *erasure.Vault, fields []string) piiSealer {
	set := make(map[string]bool, len(fields))
	for _, f := range fields {
		set[f] = true
	}
	return piiSealer{vault: v, fields: set}
}

func (p piiSealer) SealPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error) {
	return p.vault.SealFields(ctx, id, subject, doc, p.fields)
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// driftWindowDays reads INTRAKTIBLE_MODEL_DRIFT_WINDOW (in days) for the drift
// scheduler's firing window. 0 (absent/invalid/non-positive) means all-time.
func driftWindowDays() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("INTRAKTIBLE_MODEL_DRIFT_WINDOW")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
