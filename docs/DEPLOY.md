<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Production deployment runbook

intraktible ships as one self-contained binary (the SvelteKit UI is embedded). This
is the runbook for a real, hardened, highly-available deployment. For the flag/env
reference see [LAUNCH.md](./LAUNCH.md); for backups and disaster recovery see
[DR.md](./DR.md).

## The production posture, in one place

`INTRAKTIBLE_ENV=production` (or `--env=production`) turns on a **preflight** that
**refuses to start** on insecure config, so a production install cannot silently boot
in an unsafe state:

- a non-durable projection store (`--store=memory`) or event log (`--log=memory`) is
  **refused** — the event log is the system of record;
- a missing `INTRAKTIBLE_ENCRYPTION_KEY` is **refused** (PII/event payloads would be
  written in plaintext at rest) unless you explicitly set
  `INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST=1`;
- session cookies are forced `Secure` and HSTS is emitted (see TLS, below);
- the well-known dev API key is never seeded (it only seeds with `--store=memory`,
  which production refuses).

It also **warns** on a single-process `--log=file` (not HA) and on
`INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE` (lets connectors reach private hosts; the cloud
metadata service stays blocked regardless).

## Topology

Two tiers, and they matter:

- **API tier** — the stateless read/write surface. Scale it horizontally; each
  replica rebuilds its own projections from the log and serves once caught up.
- **Scheduler tier** — a **singleton** (1 replica) that runs the timed sweeps
  (monitor alerts, model-drift alerts, time-boxed deploy activation/revert, SLA
  breach sweep). These sweeps are not leader-elected, so running them on more than
  one replica would double-deliver alerts. The API tier therefore runs with
  `INTRAKTIBLE_MONITOR_INTERVAL` **unset**; only the scheduler tier sets it. (The
  deploy-activation sweep is additionally claim-guarded, so it is safe either way,
  but keep the alert sweeps on the singleton.)

The event log and projection store are external dependencies for HA: use a networked
log (`--log=postgres` or `--log=nats`) and `--store=postgres` so N API replicas share
one ordered log and durable read models.

## Deploy on Kubernetes (Helm)

The chart in [`deploy/helm/intraktible`](../deploy/helm/intraktible) encodes the whole
posture: the API/scheduler split, `livenessProbe: /healthz` + `readinessProbe:
/readyz`, resource requests/limits, a hardened `securityContext` (non-root,
read-only rootfs, all caps dropped), an HPA on the API tier, a PodDisruptionBudget, a
ServiceAccount without token automount, and an optional NetworkPolicy.

```sh
# 1. Build & push the image (or use your registry's copy).
docker build -t <registry>/intraktible:<tag> .
docker push <registry>/intraktible:<tag>

# 2. Create the secret out-of-band (recommended) — or let the chart manage it.
kubectl create secret generic intraktible-secrets \
  --from-literal=INTRAKTIBLE_POSTGRES_DSN='postgres://…' \
  --from-literal=INTRAKTIBLE_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=INTRAKTIBLE_BOOTSTRAP_API_KEY="$(openssl rand -hex 24)"

# 3. Install.
helm install intraktible deploy/helm/intraktible \
  --set image.repository=<registry>/intraktible --set image.tag=<tag> \
  --set secret.existingSecret=intraktible-secrets \
  --set ingress.enabled=true --set ingress.host=intraktible.example.com

kubectl rollout status deploy/intraktible-intraktible-api
```

Rolling deploys are safe: a new pod is **not** routed traffic until `/readyz` reports
its projections have caught up to the log head, so a rollout never serves empty read
models.

## Deploy on a single host (Docker Compose)

For a smaller install, [`deploy/docker-compose.prod.yml`](../deploy/docker-compose.prod.yml)
runs the app on Postgres with encryption at rest:

```sh
cp deploy/.env.example deploy/.env   # fill in — never commit the filled-in file
docker compose -f deploy/docker-compose.prod.yml --env-file deploy/.env up -d
```

## TLS

The binary serves plain HTTP; **terminate TLS at your ingress/load balancer** and
forward to `:8080` on a private network. So cookies are still marked `Secure` and
HSTS is emitted behind the proxy (where the app sees plaintext), set
`INTRAKTIBLE_SECURE_COOKIES=1` (the production preflight defaults this on) and, if you
want `X-Forwarded-Proto: https` honored, `INTRAKTIBLE_TRUST_PROXY=1` — only with a
real terminating proxy in front, since that header is otherwise client-forgeable. The
Helm chart sets both.

## The first credential

A durable-store install seeds no key. Get the first admin credential one of two ways:

- **Bootstrap key** — set `INTRAKTIBLE_BOOTSTRAP_API_KEY` (a real secret, ≥16 chars).
  It seeds a single admin key on any store; use it to mint managed keys / users, then
  **rotate to a managed key and unset the variable**.
- **SSO** — configure OIDC/SAML (`INTRAKTIBLE_OIDC_PROVIDERS` / `INTRAKTIBLE_SAML_PROVIDERS`)
  and SCIM (`INTRAKTIBLE_SCIM_*`); users sign in with their IdP identity and roles.

> **Four-eyes in production requires verified identities.** The maker-checker gate
> compares actor strings, and an API key's actor is operator-set free text — so a
> single admin who can mint keys could satisfy four-eyes alone. In a regulated
> deployment, provision maker and checker as distinct **SSO/SCIM** users and restrict
> API-key minting; do not rely on free-text key actors for separation of duties.

## Observability

- **`/healthz`** (liveness): 503 if the projection consumer stalled.
- **`/readyz`** (readiness): 503 until this replica's projections reach the log head.
- **`/metrics`**: Prometheus (unauthenticated — keep it internal via NetworkPolicy).
- **`/version`**: build revision + Go toolchain.
- Distributed tracing: set `INTRAKTIBLE_OTEL_EXPORTER` (OTLP).

## Graceful shutdown

On `SIGTERM` the server stops accepting connections and drains in-flight requests for
`INTRAKTIBLE_SHUTDOWN_TIMEOUT` (default 30s); set your orchestrator's
`terminationGracePeriodSeconds` a little higher (the chart uses 60s). Long-lived SSE
streams (`/decide/stream`, agent run streams) are cut at the deadline; clients
reconnect to a healthy replica.
