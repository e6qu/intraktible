// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server assembles the intraktible HTTP backend from an injected event
// log and projection store: every enabled module's routes, the platform
// capabilities (audit, MRM, privacy, comments, notifications, erasure,
// auth/SSO/SCIM), the projection runtime, and the middleware chain.
//
// It is the composition root shared by both deployment targets — the native
// binary (cmd/intraktible) supplies the WAL/sqlite/postgres backends selected
// by flags, while a js/wasm host supplies in-memory implementations — so this
// package builds under GOOS=js by design. Environment-driven configuration
// (AI providers, guardrails, egress policy, encryption at rest, pricing, SSO)
// is read here, identically on both targets.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	agentcmd "github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/eval"
	agentservice "github.com/e6qu/intraktible/agent-manager/service"
	"github.com/e6qu/intraktible/agent-manager/tools"
	"github.com/e6qu/intraktible/case-manager/cases"
	casecmd "github.com/e6qu/intraktible/case-manager/command"
	caseschedule "github.com/e6qu/intraktible/case-manager/schedule"
	caseservice "github.com/e6qu/intraktible/case-manager/service"
	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	contextservice "github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
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
	"github.com/e6qu/intraktible/mrm"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/openapi"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/scim"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/web"
)

// asyncRunWorkers is the size of the Agent Manager's async-run worker pool.
const asyncRunWorkers = 4

// Config carries the caller-selected knobs New needs beyond the injected log
// and store. Everything else (AI providers, guardrails, egress, encryption,
// SSO, SCIM) comes from the environment inside New, so both deployment
// targets read it identically.
type Config struct {
	// Modules is the comma-separated module selection (or "all").
	Modules string
	// DevAPIKey seeds the well-known dev admin key. Honored only with the
	// non-durable in-memory store (StoreKind "memory"); empty disables.
	DevAPIKey string
	// StoreKind names the projection-store backend the caller selected
	// (memory | sqlite | postgres). It gates dev-key seeding: a durable store
	// never seeds the well-known key.
	StoreKind string
	// LogKind names the event-log backend (file | memory | sqlite | postgres |
	// nats), used by the production preflight to reject non-durable/HA-unsafe
	// choices. Empty skips the log-kind checks.
	LogKind string
	// Env is the deployment environment ("production" turns on the preflight that
	// refuses insecure config, and defaults secure cookies on). Empty/"development"
	// keeps the permissive local-dev behavior.
	Env string
	// Now, when non-nil, overrides the clock every command handler stamps event
	// times with (and the SLA/expiry math reads). Nil means the UTC system clock,
	// so native behavior is unchanged. Deterministic tests and the demo seeder
	// script this.
	Now func() time.Time
	// AIProvider, when non-nil, is registered as the sole (default) AI provider,
	// replacing the environment-driven registration (INTRAKTIBLE_AI_BASE_URL /
	// INTRAKTIBLE_AI_STUB). The demo seeder injects a scripted provider here so
	// seeded agent runs record real provider round-trips. Nil keeps env behavior.
	AIProvider ai.Provider
}

// Server is the assembled backend: the full HTTP handler (middleware included)
// plus the projection runtime driving its read models.
type Server struct {
	Handler http.Handler
	// Projections is the running projection runtime; /healthz already reflects
	// its health, it is exposed for hosts that need direct access (e.g. tests
	// or a wasm shell reporting rebuild progress).
	Projections *projection.Runtime

	agents *agentcmd.Handler
}

// Close waits for the Agent Manager's async-run workers to finish. The workers
// stop on ctx cancellation, so cancel the ctx passed to New first (an in-flight
// run finishes; call Close before closing the injected log so it can record).
func (s *Server) Close() {
	if s.agents != nil {
		s.agents.DrainWorkers()
	}
}

// New assembles the backend on the injected event log and projection store.
// It registers every enabled module's routes, starts the projection runtime
// (and, when configured, the timed sweeps and async-run workers), and returns
// the fully wrapped root handler. The caller owns log and st (and closes them
// after Close); New owns everything it builds on top.
func New(ctx context.Context, cfg Config, log eventlog.Log, st store.Store) (*Server, error) {
	// Normalize the clock once: every handler below is constructed with it, so a
	// scripted clock (tests, the demo seeder) governs every stamped event time.
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	// Encryption at rest: when INTRAKTIBLE_ENCRYPTION_KEY is set, event payloads and
	// projection-store documents are sealed under the keyring (AES-256-GCM). Off by
	// default; the keyring is built once and shared by the log and store wrappers.
	// Keys must be retained — losing one makes everything sealed under it unreadable.
	atRest, err := secretbox.KeyringFromKeys(
		os.Getenv("INTRAKTIBLE_ENCRYPTION_KEY"),
		splitCSV(os.Getenv("INTRAKTIBLE_ENCRYPTION_KEYS_PREVIOUS"))...,
	)
	if err != nil {
		return nil, fmt.Errorf("encryption-at-rest: %w", err)
	}
	if atRest != nil {
		slog.Info("encryption at rest enabled (event payloads + projection store)")
	}
	if err := preflight(cfg, atRest != nil); err != nil {
		return nil, err
	}
	configureCookieSecurity(cfg.Env)
	log = eventlog.Encrypted(log, atRest)
	st = store.Encrypted(st, atRest)

	keyring := auth.NewKeyring()
	// Sessions live in the projection store, so they persist across restarts when
	// --store=sqlite (and stay in-memory with --store=memory). It is not a
	// projection, so a rebuild never touches it.
	sessions := auth.NewStoreSessions(st).WithNow(now)
	apiKeys := auth.NewStoreAPIKeys(st).WithNow(now)
	keyring.UseResolver(apiKeys)
	if seedDevKey(keyring, cfg.DevAPIKey, cfg.StoreKind) {
		slog.Warn("seeded dev API key (in-memory store, local dev only)", "scope", auth.ScopeAll, "role", auth.RoleAdmin)
	} else if cfg.DevAPIKey != "" {
		slog.Warn("ignoring --dev-api-key: a durable store does not seed the well-known dev key; issue a managed API key instead", "store", cfg.StoreKind)
	}
	// Production bootstrap: an operator-chosen admin key from the environment (a real
	// secret, not a well-known value), seeded on ANY store so a self-host install can
	// obtain its first credential without SSO and without the dev key. Re-added each
	// boot (idempotent); rotate it to a managed key and unset the env var thereafter.
	if boot := os.Getenv("INTRAKTIBLE_BOOTSTRAP_API_KEY"); boot != "" {
		if len(boot) < 16 {
			return nil, fmt.Errorf("INTRAKTIBLE_BOOTSTRAP_API_KEY must be at least 16 characters")
		}
		keyring.Add(boot, auth.APIKey{
			ID:       "bootstrap",
			Identity: identity.Identity{Org: "default", Workspace: "main", Actor: "bootstrap"},
			Scope:    auth.ScopeAll,
			Role:     auth.RoleAdmin,
		})
		slog.Warn("seeded bootstrap admin API key from INTRAKTIBLE_BOOTSTRAP_API_KEY; rotate to a managed key and unset the variable")
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
		return nil, err
	}
	if guardrails.Enabled() {
		slog.Info("ai: guardrails enabled", "rate_per_sec", guardrails.RatePerSec,
			"redact_pii", guardrails.RedactPII, "block_injection", guardrails.BlockInjection)
	}
	// HTTP connectors dial operator-configured URLs; the default egress policy
	// blocks loopback/private targets (SSRF guard). Operators whose connectors
	// legitimately reach internal hosts opt in with INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE.
	// The same policy guards the AI provider client below, so a provider URL that
	// redirects to a metadata IP is blocked too.
	egress := connectors.EgressPolicy{AllowPrivate: truthy(os.Getenv("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE"))}
	if egress.AllowPrivate {
		slog.Warn("connectors: egress to private/loopback targets is ALLOWED (INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE)")
	}

	aiRegistry := ai.NewRegistry()
	if cfg.AIProvider != nil {
		aiRegistry.Register(ai.Guard(cfg.AIProvider, guardrails))
	} else {
		aiRegistry = buildAIRegistry(guardrails, egress)
	}

	connectorSecrets, err := connectorSecretBoxFromEnv(ctx)
	if err != nil {
		return nil, err
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

	if enabled(cfg.Modules, "hello") {
		helloservice.New(hellocmd.NewHandler(log).WithNow(now), st).Routes(api)
	}
	var monitorScheduler *monitor.Scheduler
	var driftScheduler *enginemodels.Scheduler
	var deployScheduler *schedule.Scheduler
	var caseScheduler *caseschedule.Scheduler
	// Outbound webhook delivery (egress-guarded, retried, recorded) — shared by the
	// monitor/drift pushes and the case SLA escalation, so both reach the same
	// operator-configured webhooks.
	notifier := notify.NewNotifier(log, st, egress.Client(15*time.Second)).WithNow(now)
	if enabled(cfg.Modules, "decision-engine") {
		// A decision can fold in a Context Layer entity's features, call Context
		// Layer connectors from Connect nodes, and run Agent Manager agents from AI
		// nodes; each provider reads the shared store (a no-op when that module is
		// not running / nothing is defined).
		decideOpts := []enginecmd.DecideOption{
			enginecmd.WithNow(now),
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
		// Override the per-decide expression/Code evaluation budget (default is a few
		// seconds) so an operator can tune the wall-clock cap on flow-author logic.
		if v := strings.TrimSpace(os.Getenv("INTRAKTIBLE_DECIDE_EVAL_TIMEOUT")); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("INTRAKTIBLE_DECIDE_EVAL_TIMEOUT %q: %w", v, err)
			}
			decideOpts = append(decideOpts, enginecmd.WithEvalTimeout(d))
		}
		decide := enginecmd.NewDecideHandler(log, st, decideOpts...)
		// The pre-approval write side is shared: the engine service uses it to
		// promote an approved batch into grants; the pre-approval service exposes
		// the standalone grant/list/revoke surface.
		paCmd := preapproval.NewHandler(log).WithNow(now)
		engineCmd := enginecmd.NewHandler(log).WithNow(now)
		engineSvc := engineservice.New(engineCmd, decide, paCmd, st)
		if len(erasurePIIFields) > 0 {
			engineSvc.UseEraser(erasureVault)
		}
		engineSvc.UseCopilot(aiCompleter{reg: aiRegistry})
		engineSvc.Routes(api)
		// Policies are the operational disposition layer over flows (auto-approve/
		// decline/refer); a first-class artifact alongside the flow registry.
		policy.New(policy.NewHandler(log).WithNow(now), st).Routes(api)
		// Pre-approvals: durable pre-decisions honored instantly at decide time.
		preapproval.New(paCmd, st).Routes(api)
		// Webhooks: outbound notification channel. Delivery reuses the connector
		// egress guard (SSRF-safe) so a monitor check can push firing rules out.
		notify.New(notify.NewHandler(log).WithNow(now), st).Routes(api)
		// Monitors: thresholds over a flow's metrics, evaluated live (failure/refer
		// rate, automation rate, latency, volume); a check pushes firing rules to webhooks.
		monCmd := monitor.NewHandler(log).WithNow(now)
		monitor.New(monCmd, st, notifier).WithNow(now).Routes(api)
		monitorScheduler = monitor.NewScheduler(st, monCmd, notifier).WithNow(now)
		// Model drift: the same scheduler cadence sweeps every model's PSI vs its
		// configured threshold and pushes the firing edge to webhooks. The window is
		// cumulative by default; INTRAKTIBLE_MODEL_DRIFT_WINDOW (days) narrows it to a
		// recent slice so a fresh shift isn't diluted by all-time history.
		driftScheduler = enginemodels.NewScheduler(st, engineCmd, notifier, driftWindowDays()).WithNow(now)
		// Deploy scheduler: activates due scheduled deploys and reverts expired
		// time-boxed ones on the same cadence as the monitor sweep.
		deployScheduler = schedule.NewScheduler(st, engineCmd).WithNow(now)
		// Flow assertions: input→expected test cases, run through the pure core and
		// used as a pre-promote gate.
		assertions.New(assertions.NewHandler(log).WithNow(now), st).Routes(api)
		// Per-flow access grants: fine-grained, opt-in restriction of change-control
		// on a specific flow/environment, layered over the global RBAC roles.
		grants.New(grants.NewHandler(log).WithNow(now), st).Routes(api)
	}
	if enabled(cfg.Modules, "case-manager") {
		caseCmd := casecmd.NewHandler(log).WithNow(now)
		caseservice.New(caseCmd, st).WithNow(now).Routes(api)
		// SLA sweeper: records breaches for open cases past deadline on the same
		// cadence as the monitor/drift/deploy sweeps (the /cases/sla-sweep endpoint
		// is the on-demand alternative).
		// Push an overdue human task to the operator-configured webhooks (the in-app
		// inbox is driven separately off the same events) so reviewers are pulled to it.
		caseScheduler = caseschedule.NewScheduler(st, caseCmd).WithNow(now).WithNotify(
			func(ctx context.Context, id identity.Identity, caseIDs []string) {
				_, _ = notifier.Deliver(ctx, id, "case.sla_breached",
					map[string]any{"event": "case.overdue", "case_ids": caseIDs})
			})
	}
	if enabled(cfg.Modules, "context-layer") {
		contextservice.New(contextcmd.NewHandler(log).WithNow(now), st,
			contextservice.WithNow(now),
			contextservice.WithEgress(egress),
			contextservice.WithSecrets(connectorSecrets),
			contextservice.WithErasure(erasureVault, erasurePIIFields),
		).Routes(api)
	}
	srv := &Server{}
	var agentHandler *agentcmd.Handler
	if enabled(cfg.Modules, "agent-manager") {
		agentHandler = agentcmd.NewHandler(log, st, aiRegistry, agentcmd.WithToolbox(toolbox), agentcmd.WithNow(now))
		// Async agent runs: a worker pool drains the queue until ctx is cancelled;
		// Server.Close waits for it, so in-flight runs finish on shutdown before
		// the caller closes the log.
		agentHandler.StartWorkers(ctx, asyncRunWorkers)
		srv.agents = agentHandler
		// Per-model prices (USD per million tokens) derive run cost in the run
		// summary / observability surface; absent, only token counts are reported.
		pricing, err := agents.ParsePricing(os.Getenv("INTRAKTIBLE_AI_PRICES"))
		if err != nil {
			return nil, err
		}
		agentservice.New(agentHandler, st, agentservice.WithPricing(pricing)).Routes(api)
	}

	// Audit surface (platform capability, independent of the enabled modules): a
	// tenant-scoped, filterable, exportable read over the event log.
	audit.New(st).Routes(api)

	// Model-risk report (SR 11-7 / SS1/23): a read-only aggregation of the model
	// inventory + validation evidence + monitoring across flows, predictive models,
	// and agents, exportable as JSON / CSV / Markdown.
	mrm.New(st).Routes(api)

	// Privacy: per-workspace sensitive-field masking, applied at read boundaries
	// (decision history/exports). A platform capability, independent of modules.
	privacy.New(privacy.NewHandler(log).WithNow(now), st).Routes(api)

	// Comments: general discussion threads attached to any subject (deployment
	// requests, decisions, cases) so workflow surfaces carry an explanation trail.
	comments.New(comments.NewHandler(log).WithNow(now), st).Routes(api)

	// Notifications: a per-user inbox derived from @-mentions in comments.
	notifications.New(notifications.NewHandler(log).WithNow(now), st).Routes(api)

	// Authenticated caller introspection (inside the /v1 auth chain).
	httpx.NewAPIKeysHandler(apiKeys, log).Routes(api)
	// Right-to-erasure (crypto-shredding) + retention, admin-gated. erasureVault is
	// built earlier and shared with the Context Layer's PII field sealing.
	erasure.NewService(erasureVault).Routes(api)
	api.HandleFunc("GET /v1/me", httpx.MeHandler())

	rt := projection.New(log, st, Projectors(cfg.Modules)...)
	if err := rt.Start(ctx); err != nil {
		return nil, fmt.Errorf("projection start: %w", err)
	}
	srv.Projections = rt
	// Recover async runs left "running" by a previous crash/shutdown — only after
	// the projections are rebuilt, so a worker can resolve the agent from the store.
	if agentHandler != nil {
		if n, err := agentHandler.RecoverRunning(ctx); err != nil {
			return nil, fmt.Errorf("agent-manager: recover running runs: %w", err)
		} else if n > 0 {
			slog.Info("agent-manager: re-enqueued interrupted runs", "count", n)
		}
	}
	// Timed sweeps: if INTRAKTIBLE_MONITOR_INTERVAL is set (e.g. "1m"), every
	// enabled scheduler sweeps on that shared cadence — monitor alerts, model
	// drift, scheduled deploys, case SLAs. Off by default — the /monitors/check
	// endpoint is the on-demand alternative. Each scheduler is gated only on its
	// own module, so a split-services profile (e.g. --modules=case-manager) still
	// runs its SLA sweeps without the decision-engine module.
	var sweeps []timedSweeper
	if monitorScheduler != nil {
		sweeps = append(sweeps, monitorScheduler)
	}
	if driftScheduler != nil {
		sweeps = append(sweeps, driftScheduler)
	}
	if deployScheduler != nil {
		sweeps = append(sweeps, deployScheduler)
	}
	if caseScheduler != nil {
		sweeps = append(sweeps, caseScheduler)
	}
	if err := startTimedSweeps(ctx, os.Getenv("INTRAKTIBLE_MONITOR_INTERVAL"), sweeps); err != nil {
		return nil, err
	}

	// The API contract (OpenAPI 3.1) + a reference page, served publicly so
	// integrators and code generators can fetch it without a key.
	openapi.Routes(root)

	// /healthz reflects projection health: degraded (503) if a live apply error
	// stopped the consumer, so an orchestrator does not keep routing to a node
	// serving stale read models.
	root.HandleFunc("GET /healthz", httpx.Health(rt.Err))
	// /readyz gates traffic during a rolling deploy: 503 until this replica's
	// projections have caught up to the log head, so a freshly-started pod does not
	// serve empty read models while it rebuilds. Liveness (/healthz) vs readiness.
	root.HandleFunc("GET /readyz", httpx.Ready(rt.Applied, log.Head, rt.Err))
	// /version reports the build (VCS revision + Go) so ops can confirm what's live.
	root.HandleFunc("GET /version", httpx.Version())
	// /metrics is the Prometheus scrape endpoint (unauthenticated like /healthz —
	// aggregate operational counters only, no tenant data).
	root.Handle("GET /metrics", metrics.Handler())

	// Public auth endpoints — exchange an API key for a session cookie (and clear
	// it). Registered on root with exact patterns so they win over the /v1/ chain.
	// Rate-limit the credential-verifying endpoint per client IP so a durable store
	// (real users) can't be brute-forced or credential-stuffed. The default is
	// generous enough for humans behind a shared NAT yet tight enough to make
	// guessing infeasible; tune with INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS/_BURST, or set
	// rps to 0 to disable (behind a proxy that already rate-limits). With
	// INTRAKTIBLE_TRUST_PROXY the bucket is per X-Forwarded-For, so shared egress IPs
	// don't collide.
	rps := envFloat("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", 10)
	burst := envInt("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", 30)
	loginHandler := httpx.LoginHandler(keyring, sessions)
	if rps > 0 {
		loginHandler = httpx.NewRateLimit(rps, burst)(loginHandler)
	}
	root.HandleFunc("POST /v1/login", loginHandler)
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
		// Re-run the deprovisioning gate on every Resolve of an SSO session, so a
		// SCIM-deactivated user loses access within the request cycle instead of
		// keeping a valid session until the 24h TTL expires.
		sessions.SetValidator(func(id identity.Identity) bool {
			return scimStore.Allowed(context.Background(), id, id.Actor)
		})
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
	srv.Handler = httpx.Chain(root, httpx.SecurityHeaders, httpx.Recover, httpx.RequestID, httpx.Tracing, httpx.Logger, httpx.Metrics)
	return srv, nil
}

// timedSweeper is the shared shape of the module schedulers (monitor, drift,
// deploy, case SLA): a loop that sweeps on a fixed cadence until ctx is done.
type timedSweeper interface {
	Run(ctx context.Context, interval time.Duration)
}

// startTimedSweeps starts each scheduler on the interval given by
// INTRAKTIBLE_MONITOR_INTERVAL (a no-op when unset). The schedulers start
// independently of one another, so enabling only one module still gets its
// timed sweeps.
func startTimedSweeps(ctx context.Context, interval string, sweeps []timedSweeper) error {
	if interval == "" {
		return nil
	}
	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		return fmt.Errorf("INTRAKTIBLE_MONITOR_INTERVAL %q: must be a positive duration", interval)
	}
	for _, s := range sweeps {
		go s.Run(ctx, d)
	}
	return nil
}

// seedDevKey registers the well-known dev admin key on keyring, but ONLY with the
// non-durable in-memory store. Any durable store (sqlite/postgres) — the only kind a
// real deployment can use — refuses to seed it regardless of the flag value, so
// production can never boot with a known admin credential. Returns whether it seeded.
// preflight refuses to boot on config that is unsafe for production. It is a no-op
// outside production so local dev stays permissive; in production it fails loud
// (the repo's no-fallbacks rule) rather than silently serving an insecure system.
func preflight(cfg Config, encryptionEnabled bool) error {
	if !strings.EqualFold(cfg.Env, "production") {
		return nil
	}
	var problems []string
	if cfg.StoreKind == "memory" {
		problems = append(problems, "--store=memory is not durable (read models are lost on restart); use sqlite or postgres")
	}
	if cfg.LogKind == "memory" {
		problems = append(problems, "--log=memory is not durable (the event log is the system of record); use file, sqlite, postgres, or nats")
	}
	if !encryptionEnabled && os.Getenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST") == "" {
		problems = append(problems, "INTRAKTIBLE_ENCRYPTION_KEY is unset, so PII/event payloads would be written in plaintext at rest; set it, or set INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST=1 to accept that risk")
	}
	if len(problems) > 0 {
		return fmt.Errorf("server: refusing to start with INTRAKTIBLE_ENV=production and insecure config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	// Non-fatal production advisories.
	if cfg.LogKind == "file" {
		slog.Warn("--log=file is a single-process WAL; use --log=postgres or --log=nats for multi-replica HA")
	}
	if os.Getenv("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE") != "" {
		slog.Warn("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE is set: flow connectors may reach private/internal hosts (the cloud metadata service stays blocked)")
	}
	return nil
}

// configureCookieSecurity decides when session cookies are marked Secure and HSTS
// is emitted. Production forces Secure on (a prod deployment is reached over HTTPS)
// unless explicitly opted out; either environment can force it via env. A trusted
// TLS-terminating proxy is honored only when INTRAKTIBLE_TRUST_PROXY is set (the
// X-Forwarded-Proto header is otherwise client-forgeable).
func configureCookieSecurity(env string) {
	force := truthy(os.Getenv("INTRAKTIBLE_SECURE_COOKIES"))
	if strings.EqualFold(env, "production") && !isFalsey(os.Getenv("INTRAKTIBLE_SECURE_COOKIES")) {
		force = true
	}
	httpx.ConfigureCookieSecurity(force, truthy(os.Getenv("INTRAKTIBLE_TRUST_PROXY")))
}

func isFalsey(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

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

// Projectors returns the read-model projectors for the enabled modules — the
// single source of truth shared by `serve` (live projections) and `replay`
// (rebuild from the log).
func Projectors(modules string) []projection.Projector {
	// Privacy masking config and the audit index are platform capabilities, projected
	// regardless of which modules are enabled (so masking and the audit trail work in
	// every profile). The audit projector re-indexes every event for tenant-scoped reads.
	ps := []projection.Projector{privacy.Projector{}, comments.Projector{}, notifications.Projector{}, audit.Projector{}}
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
		ps = append(ps, agents.Projector{}, eval.Projector{})
	}
	return ps
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

// piiSealer adapts the erasure vault + a PII field set to the decision engine's
// PIISealer port, sealing a recorded decision's PII fields under the entity
// subject. It keeps the engine free of direct erasure/privacy imports.
type piiSealer struct {
	vault  *erasure.Vault
	fields map[string]bool
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
