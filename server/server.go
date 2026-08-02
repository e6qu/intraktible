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
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	agentcmd "github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/eval"
	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	agentservice "github.com/e6qu/intraktible/agent-manager/service"
	"github.com/e6qu/intraktible/agent-manager/tools"
	"github.com/e6qu/intraktible/case-manager/cases"
	casecmd "github.com/e6qu/intraktible/case-manager/command"
	casedomain "github.com/e6qu/intraktible/case-manager/domain"
	caseschedule "github.com/e6qu/intraktible/case-manager/schedule"
	caseservice "github.com/e6qu/intraktible/case-manager/service"
	commscmd "github.com/e6qu/intraktible/comms/command"
	commsprojection "github.com/e6qu/intraktible/comms/projection"
	commsservice "github.com/e6qu/intraktible/comms/service"
	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	contextservice "github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/authoring"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/grants"
	"github.com/e6qu/intraktible/decision-engine/history"
	enginemodels "github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/decision-engine/outcomes"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/population"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/decision-engine/schedule"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	"github.com/e6qu/intraktible/fairlending"
	hellocmd "github.com/e6qu/intraktible/hello/command"
	helloservice "github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	modelingprojection "github.com/e6qu/intraktible/modeling/projection"
	modelingservice "github.com/e6qu/intraktible/modeling/service"
	"github.com/e6qu/intraktible/mrm"
	packscmd "github.com/e6qu/intraktible/packs/command"
	packsprojection "github.com/e6qu/intraktible/packs/projection"
	packsservice "github.com/e6qu/intraktible/packs/service"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/jurisdiction"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/openapi"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/projection"
	platformscheduler "github.com/e6qu/intraktible/platform/scheduler"
	"github.com/e6qu/intraktible/platform/scim"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/sharing"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/web"
	providerscmd "github.com/e6qu/intraktible/providers/command"
	providersprojection "github.com/e6qu/intraktible/providers/projection"
	providersservice "github.com/e6qu/intraktible/providers/service"
	"github.com/e6qu/intraktible/reconsideration"
	"github.com/e6qu/intraktible/registers"
	"github.com/e6qu/intraktible/retention"
	tenancycmd "github.com/e6qu/intraktible/tenancy/command"
	tenancydomain "github.com/e6qu/intraktible/tenancy/domain"
	tenancyprojection "github.com/e6qu/intraktible/tenancy/projection"
	tenancyservice "github.com/e6qu/intraktible/tenancy/service"
)

// asyncRunWorkers is the size of the Agent Manager's async-run worker pool.
const asyncRunWorkers = 4
const populationWorkers = 4

const (
	processRoleAll       = "all"
	processRoleAPI       = "api"
	processRoleWorker    = "worker"
	processRoleScheduler = "scheduler"

	defaultDecisionRecoveryInterval = time.Second
)

// Config carries the caller-selected knobs New needs beyond the injected log
// and store. Everything else (AI providers, guardrails, egress, encryption,
// SSO, SCIM) comes from the environment inside New, so both deployment
// targets read it identically.
type Config struct {
	// Modules is the comma-separated module selection (or "all").
	Modules string
	// ProcessRole controls background ownership while leaving the HTTP and
	// projection surfaces available on every process: all (local monolith), api
	// (no background work), worker (durable decisions/agent runs), or scheduler
	// (singleton timed governance sweeps).
	ProcessRole string
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

	agents          *agentcmd.Handler
	agentGovernance *agentgovernance.Service
	population      *population.Handler
	modeling        *modelingservice.Service
	caseScheduler   *caseschedule.Scheduler
	// draining latches on BeginDrain and makes /readyz report 503 while the
	// process still serves traffic, so a load balancer depools this replica
	// before the listener closes.
	draining atomic.Bool
}

// BeginDrain marks this replica as shutting down: /readyz starts answering 503
// while the listener stays open. The caller is expected to keep serving for a
// drain window afterwards, long enough for the load balancer to observe the
// failed probe and stop routing here, and only then shut the listener down.
// Idempotent, and safe to call from a signal handler.
func (s *Server) BeginDrain() { s.draining.Store(true) }

// Draining reports whether BeginDrain has been called.
func (s *Server) Draining() bool { return s.draining.Load() }

// Close waits for the Agent Manager's async-run workers to finish. The workers
// stop on ctx cancellation, so cancel the ctx passed to New first (an in-flight
// run finishes; call Close before closing the injected log so it can record).
func (s *Server) Close() {
	if s.agents != nil {
		s.agents.DrainWorkers()
	}
	if s.agentGovernance != nil {
		s.agentGovernance.DrainWorkers()
	}
	if s.population != nil {
		s.population.DrainWorkers()
	}
	if s.modeling != nil {
		s.modeling.DrainWorkers()
	}
}

// ReconcileCaseAssists advances only the policy-requested case-assist loop. It
// is useful to deterministic hosts such as the demo seeder; production normally
// drives the same reconciler through the configured case scheduler cadence.
func (s *Server) ReconcileCaseAssists(
	ctx context.Context,
) (caseschedule.TickSummary, error) {
	if s.caseScheduler == nil {
		return caseschedule.TickSummary{}, errors.New(
			"server: case assist scheduler is unavailable",
		)
	}
	return s.caseScheduler.ReconcileAssists(ctx)
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
	processRole, err := normalizeProcessRole(cfg.ProcessRole)
	if err != nil {
		return nil, err
	}
	runWorkers := processRole == processRoleAll || processRole == processRoleWorker
	runSchedulers := processRole == processRoleAll || processRole == processRoleScheduler
	if err := validateBooleanEnv(); err != nil {
		return nil, err
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
	artifactSigningKey, err := artifactSigningKeyFromEnv()
	if err != nil {
		return nil, err
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
			Platform: true,
		})
		slog.Warn("seeded bootstrap admin API key from INTRAKTIBLE_BOOTSTRAP_API_KEY; rotate to a managed key and unset the variable")
	}

	root := http.NewServeMux()
	api := http.NewServeMux()

	// The AI provider registry is shared by the Agent Manager and the decision
	// engine's AI node. When INTRAKTIBLE_AI_BASE_URL is set, a real OpenAI-compatible
	// HTTP provider is registered (and becomes the default). The deterministic Stub
	// is opt-in through INTRAKTIBLE_AI_STUB for development and tests; with neither
	// configured, AI operations fail loudly.
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
	// Retention: on the shared sweep cadence, apply each tenant's retention policy
	// (crypto-shred subjects past their window, skipping legal holds). Opt-in and off
	// by default — a tenant with no policy is never swept.
	legalRetention := retentionGate{store: st, now: now}
	retentionScheduler := erasure.NewScheduler(erasureVault).WithNow(now).WithRetentionGate(legalRetention)
	// The leader handle shared by every timed sweep: epoch-based leader/work
	// claims let redundant scheduler replicas run concurrently with exactly one
	// tick per epoch across the fleet (no deployment-enforced singleton needed).
	schedulerLeader := &platformscheduler.Leader{
		Log: log, ID: identity.Identity{Org: "_platform", Workspace: "scheduler", Actor: "scheduler"},
		Holder: newSchedulerHolder(), Now: now,
	}
	retentionScheduler = retentionScheduler.WithLeader(schedulerLeader)
	erasurePIIFields := splitCSV(os.Getenv("INTRAKTIBLE_ERASURE_PII_FIELDS"))
	// Consent: one handler backs both the /v1/consent surface and the decide-path
	// gate (capture asserted consent + enforce permissible purpose on Connect nodes).
	consentHandler := consent.NewHandler(log).WithNow(now)
	sharingHandler := sharing.NewHandler(log).WithNow(now)

	if enabled(cfg.Modules, "hello") {
		helloservice.New(hellocmd.NewHandler(log).WithNow(now), st).Routes(api)
	}
	var monitorScheduler *monitor.Scheduler
	var driftScheduler *enginemodels.Scheduler
	var deployScheduler *schedule.Scheduler
	var caseScheduler *caseschedule.Scheduler
	var decideHandler *enginecmd.DecideHandler
	var engineHandler *enginecmd.Handler
	var populationHandler *population.Handler
	var populationScheduler *population.Scheduler
	var experimentScheduler *experiments.Scheduler
	var authoringScheduler *authoring.Scheduler
	var agentGovernanceScheduler *agentgovernance.Scheduler
	var agentGovernanceService *agentgovernance.Service
	var governanceHandler *agentgovernance.Handler
	var modelingService *modelingservice.Service
	var modelingScheduler *modelingservice.Scheduler
	if enabled(cfg.Modules, "context-layer") || enabled(cfg.Modules, "decision-engine") {
		modelingHandler := modelingcmd.NewHandler(log).WithNow(now)
		modelingOptions := []modelingservice.Option{
			modelingservice.WithNow(now),
			modelingservice.WithContentSealer(erasureVault),
		}
		if artifactSigningKey != nil {
			modelingOptions = append(
				modelingOptions,
				modelingservice.WithArtifactSigningKey(artifactSigningKey),
			)
		}
		modelingService = modelingservice.New(
			modelingHandler, st, modelingOptions...,
		)
		modelingScheduler = modelingservice.NewScheduler(modelingHandler, st).WithNow(now).WithLeader(schedulerLeader)
		modelingService.Routes(api)
	}
	if enabled(cfg.Modules, "case-manager") || enabled(cfg.Modules, "agent-manager") {
		governanceHandler = agentgovernance.NewHandler(log).
			WithNow(now).
			WithContentSealer(erasureVault)
	}
	// Outbound webhook delivery (egress-guarded, retried, recorded) — shared by the
	// monitor/drift pushes and the case SLA escalation, so both reach the same
	// operator-configured webhooks.
	notifier := notify.NewNotifier(log, st, egress.Client(15*time.Second)).WithNow(now)
	if enabled(cfg.Modules, "decision-engine") {
		experimentHandler := experiments.NewHandler(log, st).WithNow(now)
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
			enginecmd.WithConsent(consentGate{store: st, cmd: consentHandler, now: now}),
			enginecmd.WithSharing(sharingGate{store: st}),
			enginecmd.WithExperiments(experimentHandler),
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
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("INTRAKTIBLE_DECIDE_EVAL_TIMEOUT %q: want a positive duration", v)
			}
			decideOpts = append(decideOpts, enginecmd.WithEvalTimeout(d))
		}
		decide := enginecmd.NewDecideHandler(log, st, decideOpts...)
		decideHandler = decide
		// The pre-approval write side is shared: the engine service uses it to
		// promote an approved batch into grants; the pre-approval service exposes
		// the standalone grant/list/revoke surface.
		paCmd := preapproval.NewHandler(log).WithNow(now)
		engineCmd := enginecmd.NewHandler(log).WithNow(now)
		engineHandler = engineCmd
		engineSvc := engineservice.New(engineCmd, decide, paCmd, st)
		if len(erasurePIIFields) > 0 {
			engineSvc.UseEraser(erasureVault)
		}
		engineSvc.UseCopilot(aiCompleter{reg: aiRegistry})
		engineSvc.Routes(api)
		authoringHandler := authoring.NewHandler(log, st, engineCmd).WithNow(now)
		authoring.New(authoringHandler, st).Routes(api)
		authoringScheduler = authoring.NewScheduler(authoringHandler, st).WithNow(now).WithLeader(schedulerLeader)
		experiments.New(experimentHandler, st, engineCmd).Routes(api)
		experimentScheduler = experiments.NewScheduler(experimentHandler, st).WithNow(now).WithLeader(schedulerLeader)
		outcomes.New(outcomes.NewHandler(log, st).WithNow(now), st).Routes(api)
		populationHandler = population.NewHandler(log, st, decide, experimentHandler).WithNow(now)
		population.New(populationHandler, st).WithNow(now).Routes(api)
		populationScheduler = population.NewScheduler(log, st).WithNow(now).WithLeader(schedulerLeader)
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
		monitorScheduler = monitor.NewScheduler(st, monCmd, notifier).WithNow(now).WithLeader(schedulerLeader)
		// Model drift: the same scheduler cadence sweeps every model's PSI vs its
		// configured threshold and pushes the firing edge to webhooks. The window is
		// cumulative by default; INTRAKTIBLE_MODEL_DRIFT_WINDOW (days) narrows it to a
		// recent slice so a fresh shift isn't diluted by all-time history.
		windowDays, err := driftWindowDays()
		if err != nil {
			return nil, err
		}
		driftScheduler = enginemodels.NewScheduler(st, engineCmd, notifier, windowDays).WithNow(now).WithLeader(schedulerLeader)
		// Deploy scheduler: activates due scheduled deploys and reverts expired
		// time-boxed ones on the same cadence as the monitor sweep.
		deployScheduler = schedule.NewScheduler(st, engineCmd).WithNow(now).WithLeader(schedulerLeader)
		// Flow assertions: input→expected test cases, run through the pure core and
		// used as a pre-promote gate.
		assertions.New(assertions.NewHandler(log).WithNow(now), st).Routes(api)
		// Per-flow access grants: fine-grained, opt-in restriction of change-control
		// on a specific flow/environment, layered over the global RBAC roles.
		grants.New(grants.NewHandler(log).WithNow(now), st).Routes(api)
	}
	if modelingService != nil && engineHandler != nil {
		modelingService.UseModelRegistrar(engineHandler)
	}
	if enabled(cfg.Modules, "case-manager") {
		caseCmd := casecmd.NewHandler(log).WithNow(now)
		caseservice.New(caseCmd, st).
			WithNow(now).
			WithGovernance(caseGovernance{store: st, vault: erasureVault}).
			Routes(api)
		// SLA sweeper: records breaches for open cases past deadline on the same
		// cadence as the monitor/drift/deploy sweeps (the /cases/sla-sweep endpoint
		// is the on-demand alternative).
		// Push an overdue human task to the operator-configured webhooks (the in-app
		// inbox is driven separately off the same events) so reviewers are pulled to it.
		caseScheduler = caseschedule.NewScheduler(st, caseCmd).WithNow(now).WithLeader(schedulerLeader).WithNotify(
			func(ctx context.Context, id identity.Identity, caseID string) (caseschedule.DeliveryOutcome, error) {
				summary, err := notifier.Deliver(ctx, id, "case.sla_breached",
					map[string]any{"event": "case.overdue", "case_id": caseID})
				if err != nil {
					return "", err
				}
				switch {
				case summary.RetryWorthy():
					return caseschedule.DeliveryRetry, nil
				case summary.Delivered():
					return caseschedule.DeliverySucceeded, nil
				case len(summary.Results) == 0:
					return caseschedule.DeliveryNoChannel, nil
				default:
					return caseschedule.DeliveryPermanentFailure, nil
				}
			}).WithAssistRequester(
			func(
				ctx context.Context,
				id identity.Identity,
				view cases.CaseView,
				policy casedomain.AssistAutomation,
				source caseschedule.AssistPolicySource,
			) (caseschedule.AssistReconcileOutcome, error) {
				_, err := governanceHandler.RequestPolicyAssist(
					ctx, id, view, policy, agentgovernance.AssistPolicySource{
						Kind: source.Kind, Key: source.Key,
						ConfigurationSeq: source.ConfigurationSeq,
						PolicyKey:        policy.Key,
					},
				)
				switch {
				case errors.Is(err, agentgovernance.ErrAssistPolicyWaiting):
					return caseschedule.AssistWaiting, nil
				case errors.Is(err, agentgovernance.ErrAssistPolicyIneligible):
					return caseschedule.AssistIneligible, nil
				case err != nil:
					return "", err
				default:
					return caseschedule.AssistEligible, nil
				}
			},
		)
	}
	if enabled(cfg.Modules, "context-layer") {
		contextOptions := []contextservice.Option{
			contextservice.WithNow(now),
			contextservice.WithEgress(egress),
			contextservice.WithSecrets(connectorSecrets),
			contextservice.WithErasure(erasureVault, erasurePIIFields),
		}
		if modelingService != nil {
			contextOptions = append(contextOptions, contextservice.WithContracts(modelingService))
		}
		contextservice.New(contextcmd.NewHandler(log).WithNow(now), st, contextOptions...).Routes(api)
	}
	srv := &Server{}
	srv.caseScheduler = caseScheduler
	var agentHandler *agentcmd.Handler
	if enabled(cfg.Modules, "agent-manager") {
		agentGovernanceService = agentgovernance.NewService(
			governanceHandler, st,
			agentgovernance.WithAssistRegistry(aiRegistry),
			agentgovernance.WithToolbox(toolbox),
			agentgovernance.WithContentSealer(erasureVault),
			agentgovernance.WithNow(now),
			agentgovernance.WithRemoteClient(
				agentgovernance.NewRemoteClient(
					egress.Client(10*time.Minute), os.LookupEnv,
				).WithNow(now),
			),
		)
		agentGovernanceService.Routes(api)
		agentGovernanceScheduler = agentgovernance.NewScheduler(st, governanceHandler).WithNow(now).WithLeader(schedulerLeader)
		agentHandler = agentcmd.NewHandler(
			log, st, aiRegistry,
			agentcmd.WithToolbox(toolbox),
			agentcmd.WithNow(now),
			agentcmd.WithEnqueueOnStart(runWorkers),
		)
		if runWorkers {
			// Async jobs are durable. Only worker/all roles poll and claim them;
			// API and singleton scheduler replicas accept requests without also
			// invoking providers.
			agentHandler.StartWorkers(ctx, asyncRunWorkers)
			srv.agents = agentHandler
		}
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

	// Fair lending: the disparate-impact report (adverse-impact ratio, four-fifths
	// rule) parameterized by a first-class per-flow config, plus ECOA / Reg B
	// adverse-action notice generation from a declined decision's recorded reason
	// codes and the workspace creditor settings.
	fairlending.New(fairlending.NewHandler(log).WithNow(now), st).Routes(api)

	// Reconsideration: the human review of a solely-automated adverse decision (Art. 22
	// human intervention / ECOA reconsideration), recorded per decision.
	reconsideration.New(reconsideration.NewHandler(log).WithNow(now), st).Routes(api)

	// Compliance registers: the adverse-action / human-review / lawful-basis records a
	// lender retains and produces on examination, exported as CSV or Markdown.
	registers.New(st).WithNow(now).Routes(api)

	// Privacy: per-workspace sensitive-field masking, applied at read boundaries
	// (decision history/exports). A platform capability, independent of modules.
	privacy.New(privacy.NewHandler(log).WithNow(now), st).Routes(api)

	// Comments: general discussion threads attached to any subject (deployment
	// requests, decisions, cases) so workflow surfaces carry an explanation trail.
	comments.New(comments.NewHandler(log).WithNow(now), st).Routes(api)

	// Consent: a purpose-limitation ledger — a data subject's consent to process
	// their data for a named purpose, recorded as events so the grant/withdraw
	// history is auditable (GDPR Art. 6/7, GLBA purpose limitation).
	consent.New(consentHandler, st).WithNow(now).Routes(api)

	// Applicable data-protection / fair-lending regimes for the workspace, so the
	// automated-decision explanation cites the law that actually applies.
	jurisdiction.New(jurisdiction.NewHandler(log).WithNow(now), st).Routes(api)

	// GLBA sharing opt-out: a consumer's election to stop NPI sharing with
	// nonaffiliated third parties, enforced at outbound-sharing Connect nodes.
	sharing.New(sharingHandler, st).Routes(api)

	// Notifications: a per-user inbox derived from @-mentions in comments.
	notifications.New(notifications.NewHandler(log).WithNow(now), st).Routes(api)

	// Tenancy administration: platform-level organization lifecycle (platform
	// principals only) and org-level workspace/membership administration (org admins).
	tenancyHandler := tenancycmd.NewHandler(log).WithNow(now)
	tenancyservice.New(tenancyHandler, st, apiKeys).Routes(api)

	// Providers: versioned provider lifecycle (install → test → approve → deploy →
	// pause/resume → upgrade → retire) per environment, plus health reads.
	providersSvc := providersservice.New(providerscmd.NewHandler(log).WithNow(now), st)
	providersSvc.WithConformanceTester(connectorProvider)
	providersSvc.Routes(api)

	// Solution packs: signed, versioned, dependency-pinned pack manifests with an
	// install/upgrade/rollback/retire lifecycle into the workspace.
	packsservice.New(packscmd.NewHandler(log).WithNow(now), st).Routes(api)

	// Communication channels: governed delivery endpoints (webhook/email/SMS/
	// in-app) with lifecycle + delivery evidence.
	commsservice.New(commscmd.NewHandler(log).WithNow(now), st).Routes(api)
	// Bootstrap the default organization as a governed tenancy entity on first boot
	// (idempotent), so the platform org exists in the tenancy read model before any
	// organization is created through the API.
	if err := bootstrapDefaultOrg(ctx, tenancyHandler); err != nil {
		return nil, fmt.Errorf("tenancy bootstrap: %w", err)
	}

	// Authenticated caller introspection (inside the /v1 auth chain).
	httpx.NewAPIKeysHandler(apiKeys, log).Routes(api)
	// Right-to-erasure (crypto-shredding) + retention, admin-gated. erasureVault is
	// built earlier and shared with the Context Layer's PII field sealing. The retention
	// gate refuses erasure of a subject still within a statutory record-retention window.
	erasure.NewService(erasureVault).WithRetentionGate(legalRetention).Routes(api)
	// A subject's record-retention status (read-only), for the compliance/entity view.
	retention.New(st).WithNow(now).Routes(api)
	api.HandleFunc("GET /v1/me", httpx.MeHandler())

	rt := projection.New(log, st, Projectors(cfg.Modules)...)
	if err := rt.Start(ctx); err != nil {
		return nil, fmt.Errorf("projection start: %w", err)
	}
	// Optional backpressure: shed read load when the projection lags more than
	// INTRAKTIBLE_MAX_PROJECTION_LAG events behind the log head (0 = off).
	maxLag, err := envUint64("INTRAKTIBLE_MAX_PROJECTION_LAG", 0)
	if err != nil {
		return nil, err
	}
	srv.Projections = rt
	if runWorkers && populationHandler != nil {
		populationHandler.StartWorkers(ctx, populationWorkers)
		srv.population = populationHandler
	}
	if runWorkers && modelingService != nil {
		modelingService.StartWorkers(ctx, populationWorkers)
		srv.modeling = modelingService
	}
	if runWorkers && agentGovernanceService != nil {
		if err := agentGovernanceService.StartWorkers(ctx, asyncRunWorkers); err != nil {
			return nil, fmt.Errorf("agent governance: start assist workers: %w", err)
		}
		srv.agentGovernance = agentGovernanceService
	}
	// Validate the durable queues after projections are rebuilt. Pollers continue
	// from here, but startup fails rather than silently serving a worker that cannot
	// read its source of truth.
	if runWorkers && agentHandler != nil {
		if n, err := agentHandler.RecoverRunning(ctx); err != nil {
			return nil, fmt.Errorf("agent-manager: recover running runs: %w", err)
		} else if n > 0 {
			slog.Info("agent-manager: re-enqueued interrupted runs", "count", n)
		}
	}
	var recoveryWorker *enginecmd.RecoveryWorker
	if runWorkers && decideHandler != nil {
		recoveryWorker = decideHandler.NewRecoveryWorker()
		summary, err := recoveryWorker.Tick(ctx)
		if err != nil {
			return nil, fmt.Errorf("decision-engine: initial recovery scan: %w", err)
		}
		if summary.Claimed > 0 {
			slog.Info(
				"decision-engine: initial recovery completed",
				"scanned", summary.Scanned,
				"claimed", summary.Claimed,
				"recovered", summary.Recovered,
				"abandoned", summary.Abandoned,
			)
		}
	}
	// Timed sweeps: if INTRAKTIBLE_MONITOR_INTERVAL is set (e.g. "1m"), every
	// enabled scheduler sweeps on that shared cadence — monitor alerts, model
	// drift, scheduled deploys, case SLAs. Off by default — the /monitors/check
	// endpoint is the on-demand alternative. Each scheduler is gated only on its
	// own module, so a split-services profile (e.g. --modules=case-manager) still
	// runs its SLA sweeps without the decision-engine module.
	var sweeps []namedTimedSweeper
	if runSchedulers && monitorScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "flow_monitor", runner: monitorScheduler})
	}
	if runSchedulers && driftScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "model_drift", runner: driftScheduler})
	}
	if runSchedulers && deployScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "deploy_schedule", runner: deployScheduler})
	}
	if runSchedulers && populationScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "population_retention", runner: populationScheduler})
	}
	if runSchedulers && experimentScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "experiment_windows", runner: experimentScheduler})
	}
	if runSchedulers && authoringScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "authoring", runner: authoringScheduler})
	}
	if runSchedulers && caseScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{name: "case_sla", runner: caseScheduler})
	}
	if runSchedulers && agentGovernanceScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{
			name: "agent_governance", runner: agentGovernanceScheduler,
		})
	}
	if runSchedulers && modelingScheduler != nil {
		sweeps = append(sweeps, namedTimedSweeper{
			name: "modeling_lifecycle", runner: modelingScheduler,
		})
	}
	// Retention runs regardless of module (erasure is a platform capability); it is a
	// no-op for every tenant without a retention policy.
	if runSchedulers {
		sweeps = append(sweeps, namedTimedSweeper{name: "data_retention", runner: retentionScheduler})
	}
	healthLoops := append([]namedTimedSweeper(nil), sweeps...)
	if recoveryWorker != nil {
		healthLoops = append(
			healthLoops,
			namedTimedSweeper{name: "decision_recovery", runner: recoveryWorker},
		)
	}
	schedulerState := newSchedulerHealth(healthLoops)
	if runSchedulers {
		if err := startTimedSweeps(ctx, os.Getenv("INTRAKTIBLE_MONITOR_INTERVAL"), sweeps, schedulerState); err != nil {
			return nil, err
		}
	}
	if recoveryWorker != nil {
		interval, err := positiveEnvDuration(
			"INTRAKTIBLE_DECISION_RECOVERY_INTERVAL",
			defaultDecisionRecoveryInterval,
		)
		if err != nil {
			return nil, err
		}
		go recoveryWorker.Run(ctx, interval, func(err error) {
			schedulerState.Report("decision_recovery", err)
		})
	}

	// The API contract (OpenAPI 3.1) + a reference page, served publicly so
	// integrators and code generators can fetch it without a key.
	openapi.Routes(root)

	// /healthz reflects projection health: degraded (503) if a live apply error
	// stopped the consumer, so an orchestrator does not keep routing to a node
	// serving stale read models. It also reflects the event log's own delivery
	// health where the backend reports it — a log whose live feed has died leaves
	// this replica just as stale as a stopped projector, and the NATS backend has no
	// poller behind its subscription to notice on its own.
	var agentWorkerHealth func() error
	if agentGovernanceService != nil {
		agentWorkerHealth = agentGovernanceService.Err
	}
	health := combinedHealth(
		readModelHealth(rt.Err, log), schedulerState.Err, agentWorkerHealth,
	)
	root.HandleFunc("GET /healthz", httpx.Health(health))
	// /readyz gates traffic during a rolling deploy: 503 until this replica's
	// projections have caught up to the log head, so a freshly-started pod does not
	// serve empty read models while it rebuilds. Liveness (/healthz) vs readiness.
	root.HandleFunc("GET /readyz", httpx.Ready(rt.Applied, log.Head, health, srv.Draining))
	// /capacity is the SLO/SLA evidence surface (unauthenticated like /healthz — it
	// carries operational counters, not tenant data): projection lag vs the log
	// head, the configured backpressure bound, process role, and scheduler health,
	// so an operator can verify published service levels instead of inferring them.
	root.HandleFunc("GET /capacity", func(w http.ResponseWriter, _ *http.Request) {
		a, h := rt.Applied(), log.Head()
		status := "ok"
		schedErr := ""
		if err := schedulerState.Err(); err != nil {
			status = "degraded"
			schedErr = err.Error()
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"status": status,
			"projection": map[string]any{
				"applied": a, "head": h, "lag": h - a,
				"backpressure_max_lag": maxLag,
			},
			"process_role":     processRole,
			"scheduler_health": schedErr,
		})
	})
	// /version reports the build (VCS revision + Go) so ops can confirm what's live.
	root.HandleFunc("GET /version", httpx.Version())
	// /auth/validation is the app-owned, deployment-neutral SSO validation
	// surface. It accepts only a real application session cookie and exposes the
	// verified identity, normalized role, release revision, and ordinary logout.
	root.HandleFunc("GET /auth/validation", httpx.ValidationHandler(sessions))
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
	rps, err := envFloat("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", 10)
	if err != nil {
		return nil, err
	}
	burst, err := envInt("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", 30)
	if err != nil {
		return nil, err
	}
	if rps > 0 && burst < 1 {
		return nil, fmt.Errorf("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST must be positive when login rate limiting is enabled")
	}
	loginHandler := httpx.LoginHandler(keyring, sessions)
	if rps > 0 {
		loginHandler = httpx.NewRateLimit(rps, burst)(loginHandler)
	}
	root.HandleFunc("POST /v1/login", loginHandler)
	root.HandleFunc("POST /v1/logout", httpx.LogoutHandler(sessions))
	// SSO: OIDC login for the configured providers (Google, AWS Cognito, …). Each
	// provider's discovery runs now; a provider that fails to initialize stops
	// startup so an explicitly configured login path cannot disappear.
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
	authers, err := oidcAuthenticators(ctx)
	if err != nil {
		return nil, err
	}
	oh := httpx.NewOIDCHandler(sessions, authers...)
	oh.SetGate(ssoGate)
	oh.SetRoleAugmenter(ssoAugment)
	oh.Routes(root)
	if len(authers) > 0 {
		slog.Info("sso: OIDC enabled", "providers", oidcNames(authers))
	}
	samlers, err := samlAuthenticators()
	if err != nil {
		return nil, err
	}
	sh := httpx.NewSAMLHandler(sessions, samlers...)
	sh.SetGate(ssoGate)
	sh.SetRoleAugmenter(ssoAugment)
	sh.Routes(root)
	if len(samlers) > 0 {
		slog.Info("sso: SAML enabled", "providers", samlNames(samlers))
	}
	// The embedded UI is mounted last, behind the browser gate, because the gate's
	// sign-in entry point is decided by which browser-authentication mechanism this
	// deployment actually configured — which is only known now that the SSO
	// providers above have been resolved. An anonymous browser navigating to a
	// protected route is redirected here rather than handed the SPA shell, so the
	// application fails closed on the wire instead of after client JS has run.
	//
	// With SSO configured the entry point is the signed-out page, whose control
	// links into the provider's authorization endpoint. Without it, the deployment's
	// browser credential is an API key, so the entry point is the UI's own login
	// route. /login stays reachable either way: an operator bootstrapping a fresh
	// install has an API key and no identity provider yet.
	signIn := httpx.SignInEntry{Path: "/login", Exempt: []string{"/login"}}
	if len(authers) > 0 || len(samlers) > 0 {
		// When SSO is the configured browser auth, an anonymous visit (e.g. the
		// Shauth catalog launch) should auto-initiate the OIDC authorization
		// request, not land on a static signed-out page. The signed-out page
		// remains the explicit post-logout target (reached from /v1/logout).
		if oidcProviders := oidcProviderNames(authers); len(oidcProviders) == 1 {
			signIn.Path = "/v1/auth/oidc/" + oidcProviders[0] + "/login?return_to=%2F"
			slog.Info("browser sign-in entry point (auto-initiate SSO)", "path", signIn.Path, "provider", oidcProviders[0])
		} else {
			signIn.Path = "/v1/auth/signed-out"
			slog.Info("browser sign-in entry point (signed-out page)", "path", signIn.Path)
		}
	} else {
		slog.Info("browser sign-in entry point (API-key login)", "path", signIn.Path)
	}
	root.Handle("/", httpx.BrowserGate(web.Handler(), sessions, signIn))

	// Optional deployment-level network control: restrict /v1 to allowlisted CIDRs
	// (a VPC, trusted proxy, or API gateway). Empty = open (the default).
	allowlist, err := httpx.ParseIPAllowlist(os.Getenv("INTRAKTIBLE_IP_ALLOWLIST"))
	if err != nil {
		return nil, fmt.Errorf("INTRAKTIBLE_IP_ALLOWLIST: %w", err)
	}

	root.Handle("/v1/", httpx.Chain(
		api, allowlist.Middleware, httpx.Backpressure(rt.Applied, log.Head, maxLag),
		httpx.Authenticate(keyring, sessions), httpx.AuthorizeRoutes(api),
	))
	srv.Handler = httpx.Chain(root, httpx.SecurityHeaders, httpx.Recover, httpx.RequestID, httpx.Tracing, httpx.Logger, httpx.Metrics)
	return srv, nil
}

// readModelHealth combines the projection runtime's health with the event log's
// live-delivery health, when the backend reports any. Both mean the same thing to a
// caller — this replica's read models are no longer tracking the log — so they
// belong behind one probe rather than one of them going unnoticed.
func readModelHealth(projection func() error, log eventlog.Log) func() error {
	reporter, ok := log.(interface{ Err() error })
	if !ok {
		return projection
	}
	return func() error {
		if err := projection(); err != nil {
			return err
		}
		return reporter.Err()
	}
}

// combinedHealth returns the first operational failure in dependency order. A
// single check is shared by liveness and readiness so neither probe can claim
// this replica is usable while another has already observed a stalled subsystem.
func combinedHealth(checks ...func() error) func() error {
	return func() error {
		for _, check := range checks {
			if check == nil {
				continue
			}
			if err := check(); err != nil {
				return err
			}
		}
		return nil
	}
}

// timedSweeper is the shared shape of the module schedulers (monitor, drift,
// deploy, case SLA, retention): a loop that sweeps on a fixed cadence until ctx
// is done and reports every completed tick (nil means the prior failure cleared).
type timedSweeper interface {
	Run(ctx context.Context, interval time.Duration, report func(error))
}

type namedTimedSweeper struct {
	name   string
	runner timedSweeper
}

// schedulerHealth latches the most recent failed tick independently per
// scheduler. A later successful tick clears only that scheduler: one recovered
// loop cannot hide another loop that is still unable to do its work.
type schedulerHealth struct {
	mu     sync.RWMutex
	order  []string
	errors map[string]error
}

func newSchedulerHealth(sweeps []namedTimedSweeper) *schedulerHealth {
	h := &schedulerHealth{errors: make(map[string]error, len(sweeps))}
	for _, sweep := range sweeps {
		h.order = append(h.order, sweep.name)
	}
	return h
}

func (h *schedulerHealth) Report(name string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		delete(h.errors, name)
		return
	}
	h.errors[name] = err
}

func (h *schedulerHealth) Err() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, name := range h.order {
		if err := h.errors[name]; err != nil {
			return fmt.Errorf("scheduler %s: %w", name, err)
		}
	}
	return nil
}

// startTimedSweeps starts each scheduler on the interval given by
// INTRAKTIBLE_MONITOR_INTERVAL (a no-op when unset). The schedulers start
// independently of one another, so enabling only one module still gets its
// timed sweeps.
func startTimedSweeps(ctx context.Context, interval string, sweeps []namedTimedSweeper, health *schedulerHealth) error {
	if interval == "" {
		return nil
	}
	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		return fmt.Errorf("INTRAKTIBLE_MONITOR_INTERVAL %q: must be a positive duration", interval)
	}
	for _, sweep := range sweeps {
		go sweep.runner.Run(ctx, d, func(err error) {
			health.Report(sweep.name, err)
		})
	}
	return nil
}

func positiveEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s %q: must be a positive duration", key, value)
	}
	return duration, nil
}

func normalizeProcessRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return processRoleAll, nil
	}
	switch role {
	case processRoleAll, processRoleAPI, processRoleWorker, processRoleScheduler:
		return role, nil
	default:
		return "", fmt.Errorf(
			"server: process role %q is invalid: want all, api, worker, or scheduler",
			role,
		)
	}
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
	if !encryptionEnabled && !truthy(os.Getenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST")) {
		problems = append(problems, "INTRAKTIBLE_ENCRYPTION_KEY is unset, so PII/event payloads would be written in plaintext at rest; set it, or set INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST=1 to accept that risk")
	}
	if strings.TrimSpace(os.Getenv("INTRAKTIBLE_ARTIFACT_SIGNING_KEY")) == "" {
		problems = append(
			problems,
			"INTRAKTIBLE_ARTIFACT_SIGNING_KEY is unset, so training-worker replicas would not share a stable artifact-signing identity",
		)
	}
	// A single-process WAL behind a load balancer is silent data divergence: each
	// replica appends to its own file and none of them sees the others' events, so
	// the "system of record" quietly forks. This used to be a warning, which is the
	// wrong shape for a failure mode nobody notices until the logs disagree. It is
	// now a refusal that the operator must consciously opt out of by declaring the
	// deployment single-replica — so the unsafe combination can never be reached by
	// omission, only by an explicit statement that turns out to be false.
	if cfg.LogKind == "file" && !truthy(os.Getenv("INTRAKTIBLE_SINGLE_REPLICA")) {
		problems = append(problems, "--log=file is a single-process WAL: every replica would keep its own divergent copy of the event log. Use --log=postgres or --log=nats for a multi-replica deployment, or set INTRAKTIBLE_SINGLE_REPLICA=1 to declare that exactly one replica ever runs")
	}
	if len(problems) > 0 {
		return fmt.Errorf("server: refusing to start with INTRAKTIBLE_ENV=production and insecure config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	// Non-fatal production advisories.
	if cfg.LogKind == "file" {
		slog.Warn("--log=file with INTRAKTIBLE_SINGLE_REPLICA=1: this deployment must never be scaled beyond one replica")
	}
	if truthy(os.Getenv("INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE")) {
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
		Platform: true,
	})
	return true
}

// bootstrapDefaultOrg creates the platform's default organization as a governed
// tenancy entity when it does not already exist. It is idempotent across restarts:
// the creation claim is unique per org key, so a concurrent or repeated boot
// produces exactly one organization.
func bootstrapDefaultOrg(ctx context.Context, handler *tenancycmd.Handler) error {
	bootstrap := identity.Identity{Org: "default", Workspace: "main", Actor: "bootstrap"}
	_, err := handler.CreateOrganization(
		ctx, bootstrap, "default", "Default organization",
		tenancydomain.OrganizationConfig{Plan: "platform", MaxWorkspaces: 100}, "bootstrap",
	)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// Projectors returns the read-model projectors for the enabled modules — the
// single source of truth shared by `serve` (live projections) and `replay`
// (rebuild from the log).
func Projectors(modules string) []projection.Projector {
	// Privacy masking config and the audit index are platform capabilities, projected
	// regardless of which modules are enabled (so masking and the audit trail work in
	// every profile). The audit projector re-indexes every event for tenant-scoped reads.
	ps := []projection.Projector{privacy.Projector{}, comments.Projector{}, consent.Projector{}, sharing.Projector{}, jurisdiction.Projector{}, notifications.Projector{}, audit.Projector{}, tenancyprojection.Projector{}, providersprojection.Projector{}, packsprojection.Projector{}, commsprojection.Projector{},
		fairlending.ConfigProjector{}, fairlending.SettingsProjector{}, fairlending.IssuanceProjector{}, reconsideration.Projector{}, reconsideration.ContestProjector{}, modelingprojection.Projector{}}
	if enabled(modules, "hello") {
		ps = append(ps, stats.Projector{})
	}
	if enabled(modules, "decision-engine") {
		ps = append(ps, flows.Projector{}, authoring.Projector{}, history.Projector{}, analytics.Projector{}, policy.Projector{}, preapproval.Projector{}, monitor.Projector{}, notify.Projector{}, assertions.Projector{}, shadow.Projector{}, schedule.Projector{}, grants.Projector{}, enginemodels.Projector{}, enginemodels.DriftProjector{}, experiments.Projector{}, outcomes.Projector{}, population.Projector{})
	}
	if enabled(modules, "case-manager") {
		ps = append(ps, cases.Projector{})
	}
	if enabled(modules, "context-layer") {
		ps = append(ps, entities.Projector{}, features.Projector{}, connectors.Projector{})
	}
	if enabled(modules, "agent-manager") {
		ps = append(ps, agents.Projector{}, eval.Projector{}, agentgovernance.Projector{})
	}
	return ps
}

// enabled reports whether module m should run given the --modules selection.
// newSchedulerHolder returns a stable, replica-unique holder name for the
// epoch-based leader claims. Two scheduler processes never share one.
func newSchedulerHolder() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "scheduler"
	}
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic("server: crypto/rand unavailable: " + err.Error())
	}
	return host + "-" + hex.EncodeToString(b[:])
}

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

func (p piiSealer) OpenPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error) {
	return p.vault.OpenFields(ctx, id, subject, doc)
}

// consentGate adapts the consent ledger to the engine's ConsentGate port, keeping the
// decision engine from importing platform/consent directly (composition-root wiring).
type consentGate struct {
	store store.Store
	cmd   *consent.Handler
	now   func() time.Time
}

func (g consentGate) HasConsent(ctx context.Context, id identity.Identity, subject, purpose string) (bool, error) {
	return consent.Has(ctx, g.store, id, subject, purpose, g.now())
}

func (g consentGate) RecordConsent(
	ctx context.Context,
	id identity.Identity,
	subject, purpose, basis, unique string,
) error {
	// A consent asserted inside a decision request is the controller vouching, via the
	// API, for authorization it holds out-of-band; the signed artifact is attached
	// separately on the subject's page. Record the method so the audit trail shows how.
	_, err := g.cmd.GrantUnique(ctx, id, consent.GrantCmd{
		Subject: subject, Purpose: purpose, Basis: consent.LawfulBasis(basis),
		Evidence: &consent.Evidence{Method: consent.MethodAPIAssertion},
	}, unique)
	return err
}

// sharingGate adapts the GLBA sharing opt-out ledger to the engine's SharingGate port
// (composition-root wiring, so the engine doesn't import platform/sharing).
type sharingGate struct{ store store.Store }

func (g sharingGate) HasOptedOut(ctx context.Context, id identity.Identity, subject string) (bool, error) {
	return sharing.HasOptedOut(ctx, g.store, id, subject)
}

// retentionGate adapts the retention read model to the erasure service's RetentionGate
// port, so platform/erasure needn't import the compliance-record packages.
type retentionGate struct {
	store store.Store
	now   func() time.Time
}

func (g retentionGate) Retained(ctx context.Context, id identity.Identity, subject string) (bool, string, error) {
	return retention.Retained(ctx, g.store, id, subject, g.now())
}

// caseGovernance exposes the same authoritative statutory-retention and
// crypto-shred state on case reads and attachment access.
type caseGovernance struct {
	store store.Store
	vault *erasure.Vault
}

func (g caseGovernance) Status(
	ctx context.Context,
	id identity.Identity,
	subject string,
	now time.Time,
) (cases.SubjectGovernance, error) {
	retained, err := retention.StatusFor(ctx, g.store, id, subject, now)
	if err != nil {
		return cases.SubjectGovernance{}, err
	}
	held, err := g.vault.OnHold(ctx, id, subject)
	if err != nil {
		return cases.SubjectGovernance{}, err
	}
	erased, err := g.vault.Erased(ctx, id, subject)
	if err != nil {
		return cases.SubjectGovernance{}, err
	}
	return cases.SubjectGovernance{
		Subject: subject, Retained: retained.Retained, RetainUntil: retained.RetainUntil,
		LegalHold: held, Erased: erased,
	}, nil
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
