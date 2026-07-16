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

## Scale-to-zero on AWS (ECS + API Gateway + S3)

### Always-on API service

Set `api_always_on = true` when the application must remain available without a
warm-up request. The module then creates the API service with one desired task,
sets the Amazon Elastic Container Service autoscaling floor to one, and omits
the EventBridge idle reaper. Combine it with `scheduler_mode = "warm"` when
timed monitoring must also remain continuously active.

Set `serve_embedded_ui_from_api = true` with an always-on API service to serve
the UI embedded in the production binary through the existing Amazon API
Gateway and CloudFront path. This is a fully deployed UI without a separate
S3 asset-upload step. The static-site bucket remains private, encrypted, and
unversioned so it does not accumulate noncurrent object storage.

The Amazon Elastic Container Service services explicitly depend on the
`AWSCURRENT` versions of every injected AWS Secrets Manager secret. This keeps
Fargate from attempting startup while a newly created database DSN is still
waiting for its Amazon RDS endpoint.

For a deployment that costs almost nothing when idle, the Terraform root module in
[`deploy/terraform/aws-ecs-scale-to-zero`](../deploy/terraform/aws-ecs-scale-to-zero)
runs the same image in a **private VPC** with the compute and database scaled to zero when
there is no traffic. It keeps the same stateless-replicas-behind-one-endpoint posture as
the topology above; it just removes the always-on pieces.

Design, and why each choice:

- **No always-on load balancer.** An ALB or NLB bills hourly and cannot scale to zero.
  **API Gateway (HTTP API)** is request-priced and reaches the ECS tasks privately through
  a **VPC Link → Cloud Map** service discovery integration — no load balancer at all.
- **CloudFront fronts S3 and the API under one domain.** The web client only ever calls
  relative `/v1` paths (there is no configurable API base URL), so serving the static site
  from **S3** and the API from API Gateway under **one CloudFront origin** keeps it
  same-origin — no code or CORS change. With the backend at zero, the S3 site still loads
  and runs the **in-browser wasm engine** (the "dehydrated" site).
- **ECS Fargate `api` service scales 0↔N.** A **waker Lambda** brings it `0→1` on a
  `POST /wake` request; CPU target-tracking scales `1→N` under load; a **reaper** scales it
  back to `0` when the edge has been idle. The wake request is the "event" that spins the
  infra back up.
- **Aurora Serverless v2 with `min_capacity = 0`** pauses to zero and resumes on the next
  connection; it backs both `--log=postgres` and `--store=postgres`. A cold replica resumes
  projections from its durable checkpoint (not a full log replay), so the wake stays
  incremental.
- **The singleton scheduler is event-driven.** An always-on scheduler would hold Aurora
  awake, so by default EventBridge wakes the scheduler service briefly on a cron to run one
  sweep window, then scales it back to zero (a `warm` always-on mode is also available). See
  the module README for the trade-off and the clean long-term fix (an idempotent
  `POST /internal/sweep` endpoint, not yet built).
- **fck-nat**, not a NAT Gateway, provides egress from the private subnets — a single small
  instance is the main residual idle cost.

The intended idle floor is roughly the fck-nat instance plus storage: no hourly load
balancer, no running compute, no database compute. See the module README for prerequisites
(build/push the image, sync the site to S3), the cost table, and the deploy-time
verification points.

The module can also reuse a shared environment by supplying `existing_vpc_id`,
`existing_private_subnet_ids`, and `existing_ecs_cluster_arn`. In that mode it
does not create another VPC, fck-nat instance, or Amazon Elastic Container
Service cluster. Configure Shauth as the generic OpenID Connect provider with
`oidc_provider_name`, `oidc_issuer`, `oidc_client_id`,
`oidc_client_secret`, and `oidc_redirect_url`; the client secret is stored in
AWS Secrets Manager and injected only into the task.

> **Future direction — instant wasm shell, then hydrate onto the live backend.** Because
> the full backend already runs in the browser as wasm, the S3 site is an instantly
> interactive app with the backend at zero — which is exactly what would mask cold-wake
> latency. Delivering a seamless "load from wasm, then hydrate onto the live durable
> backend" hand-off is **not built yet**: today the in-browser engine is a local sandbox
> (in-memory log/store, seeded from a static file, delta saved to `localStorage`) with no
> client↔server sync. It needs a client-side feature — a configurable API base, a
> wake/poll/hydrate handoff, and local-delta sync into the durable log. The Terraform
> provisions the wake path so the app is ready for it; until then the wake is a
> manual/first-request trigger.

> **Alternative (not implemented) — dqlite for self-hosted HA without a managed database.**
> [dqlite](https://dqlite.io) (Raft-replicated SQLite) could back the log/store for a
> fixed-size, self-contained HA cluster with no external database. It does **not** fit
> scale-to-zero: Raft needs a quorum of always-on nodes (typically three), so it is an
> always-on HA option, the opposite of pausing to zero. It is also a **new backend to
> build**: the current SQLite backend is pure-Go (modernc), whereas dqlite is a CGO/C
> library, and the js/wasm build deliberately excludes native backends — so adding it
> changes the build and would not run in the browser engine. Worth considering for an
> always-on, no-managed-DB posture; not for this one.

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
