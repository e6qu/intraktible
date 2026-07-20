<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# intraktible on AWS — scale-to-zero compute (ECS + API Gateway + S3 + fck-rds)

A Terraform root module that deploys intraktible into a **private VPC** where compute
scales to zero when idle while durable state resides in the shared always-on **fck-rds**
PostgreSQL service. The app still loads from **S3** in the
meantime. There is **no always-on load balancer**: API Gateway (request-priced) reaches
the ECS tasks privately via a VPC Link + Cloud Map, so at idle the fixed cost is a single
small fck-nat instance plus storage.

## What it provisions

```
                         ┌──────────────┐
   user ────────────────▶│  CloudFront  │  one public origin (same-origin for the SPA)
                         └──────┬───────┘
              ┌─────────────────┴──────────────────┐
   default (/, static, wasm)              /v1/*, /healthz, /readyz, /wake
              │                                     │
        ┌─────▼─────┐                        ┌──────▼───────────┐
        │    S3     │  "dehydrated" SPA      │  API Gateway     │  HTTP API, per-request
        │  + wasm   │  (always available)    │  (HTTP API)      │
        └───────────┘                        └───┬──────────┬───┘
                                    POST /wake ──▶│          │ /v1 ──VPC Link──▶ Cloud Map
                                     waker λ ─────┘          └──────────────────────┐
                                        │                                           │
                            scales API service 0→1                          ┌───────▼────────┐
                                                                            │ ECS Fargate    │ 0↔N
   EventBridge:                                                             │ api + scheduler│
     reaper (5m)  ── idle? → API 0                                          └───────┬────────┘
     sweep (cron) ── scheduled scheduler wake/sleep                                 │
                                                                            ┌───────▼────────┐
   egress: private subnets ──▶ fck-nat (public subnet)                     │ fck-rds Postgres│ EFS-backed
                                                                            └────────────────┘
```

- **VPC** with public + private subnets across `az_count` AZs. ECS tasks are in
  private subnets with no public IPs. **fck-nat** (a single small instance, not a NAT
  Gateway) provides outbound egress.
- **S3 + CloudFront** serve the adapter-static SvelteKit build and the wasm engine. With
  the backend at zero the app still loads and runs the **in-browser engine**. CloudFront
  fronts both S3 and the API under one domain, so the web client's relative `/v1` calls
  stay same-origin (no CORS / no configurable API base URL needed).
- **API Gateway HTTP API** replaces an ALB/NLB (both bill hourly, neither scales to zero).
  A **VPC Link → Cloud Map** private integration reaches the ECS tasks with no load
  balancer.
- **ECS Fargate**, two services on one cluster:
  - `api` — stateless, `desired=0` at idle, woken `0→1` by the **waker Lambda** on
    `POST /wake`, scaled `1→N` by CPU target-tracking, scaled back to `0` by the **reaper**
    when the edge is idle for `idle_scale_in_minutes`.
  - `scheduler` — the singleton timed-sweep runner (monitor/drift alerts, timed deploy
    activation, SLA breach), running the **same image** with `INTRAKTIBLE_MONITOR_INTERVAL`
    set. See *Scheduler modes* below.
- **Database** (`database_url_secret_arn`): a tenant-specific URL supplied by the shared
  **fck-rds** PostgreSQL service. It is used as both the event log (`--log=postgres`) and
  projection store (`--store=postgres`), so N stateless API tasks share one ordered log and
  durable read models. fck-rds owns the isolated database and role; Intraktible can read only
  its own URL secret and connect from its task security group.
- **Secrets Manager** for the at-rest encryption key, the bootstrap admin key, and the
  composed Postgres DSN, injected into the task at runtime.

## Prerequisites

1. **A published backend image.** The repo's `release` workflow builds and pushes a
   **multi-arch** image to `ghcr.io/e6qu/intraktible` on every merge to main (`:main`,
   `:sha-<short>`) and on version tags (`:1.4.2`, `:1.4`, `:1`) — there is no `:latest`.
   Point `container_image` at one; pin a version in production. Tasks run **arm64**
   (Graviton, cheaper), which the multi-arch manifest covers. If the GHCR package is
   **private**, either make it public, set `image_pull_secret_arn` to a Secrets Manager
   `{username,password}` secret holding a GHCR pull token, or mirror the image to ECR.
   (To build locally instead: `docker build -t <ref> .` and push to any registry.)
2. **A remote state backend you control** (the module generates secrets that land in
   state — use S3 + SSE-KMS with locking; see *Secrets and state* below).

## Apply

```sh
terraform init
terraform apply \
  -var 'container_image=ghcr.io/e6qu/intraktible:sha-<commit>' \
  -var 'region=eu-west-1'
```

Then publish the static site (build it first — `make web` / `npm --prefix web run build`):

```sh
# terraform output -raw site_sync_command  # prints the exact command
aws s3 sync web/build s3://$(terraform output -raw site_bucket)/ --delete
aws cloudfront create-invalidation --distribution-id $(terraform output -raw cloudfront_distribution_id) --paths '/*'
```

Get the first admin credential from `bootstrap_api_key_secret_arn` (rotate it after minting
a managed key / configuring SSO, per `docs/DEPLOY.md`).

For Shauth or another OpenID Connect provider, register all four application
coordinates on the same app origin:

- callback: `https://<domain>/v1/auth/oidc/<provider>/callback`
- post-logout landing: `https://<domain>/v1/auth/signed-out`
- Front-Channel Logout URI: `https://<domain>/v1/auth/oidc/<provider>/frontchannel-logout`
- Back-Channel Logout URI: `https://<domain>/v1/auth/oidc/<provider>/backchannel-logout`

The provider must include a session identifier in front-channel notifications.
Intraktible authenticates confidential-client token exchanges using the method
advertised by discovery, including `client_secret_post` for Shauth.

## Custom domain

Two ways to put the app on your own domain:

- **Managed DNS (recommended)** — set `domain_name` **and** `route53_zone_id` (an existing,
  already-delegated Route53 hosted zone for that domain). The module then creates the ACM
  certificate (us-east-1, DNS-validated in that zone) and the CloudFront alias `A`/`AAAA`
  records itself — no cert or record wiring on your side. **The zone must already be
  delegated** (its `NS` records live at the parent registrar) before `apply`, or the ACM
  validation step blocks until it times out. `app_url` output is then `https://<domain>`.
- **Bring-your-own cert** — set `domain_name` + `acm_certificate_arn` (a validated cert in
  us-east-1) and leave `route53_zone_id` empty. The module sets the CloudFront alias but
  does **not** create Route53 records — you point the domain at the distribution yourself.

## Cost model (idle vs active)

| Component        | Idle (no traffic)                 | Active                          |
|------------------|-----------------------------------|---------------------------------|
| CloudFront       | ~$0 (per-request)                 | per-request + egress            |
| S3               | pennies (storage)                 | + per-request                   |
| API Gateway      | $0 (per-request)                  | per-request                     |
| ECS Fargate      | **$0 (0 tasks)**                  | per running task-second         |
| fck-rds          | shared always-on PostgreSQL + EFS  | shared service cost              |
| fck-nat          | ~$3/mo (one small instance)       | + egress data                   |
| Secrets/logs     | pennies                           | pennies                         |

The intended idle floor is roughly **fck-nat + storage** — no hourly load balancer, no
running Intraktible compute; fck-rds remains the shared database service.

## Wake and "hydration" — what works, and the seam

At idle the S3 site loads and runs the in-browser wasm engine (a local sandbox). To use
the live, durable, multi-user backend the front end must:

1. `POST /wake` (through CloudFront) → the waker scales the `api` service `0→1`.
2. Poll `/readyz` until it returns 200 (the task is up and projections have caught up).
3. Switch its API calls to the live backend and load real state.

> **The infra is ready for this; the client-side handoff is not built yet.** Today the
> in-browser engine has no path to sync to or hand off to a live backend (in-memory log/
> store, seeded from a static file, delta persisted to `localStorage`). So the automatic
> "load instantly from wasm, then hydrate onto the live backend" experience is a **separate
> app feature** (a configurable API base + a wake/poll/hydrate handoff + local-delta sync).
> Until it ships, the wake path works with a manual/first-request trigger, and the S3 site
> serves the standalone wasm experience. Tracked as a future direction in `docs/DEPLOY.md`.

## Scheduler modes (`scheduler_mode`)

The timed sweeps must run on exactly one replica (they are not leader-elected — multiple
would double-deliver alerts). The scheduler remains event-driven to avoid needless compute.

- **`scheduled`** (default) — the scheduler service sits at `0`. EventBridge wakes it on
  `scheduler_window_cron`; the controller Lambda scales it to `1`, waits
  `scheduler_run_minutes` (during which it runs sweeps every `monitor_interval`), then scales
  it back to `0`. Trade-off: a sweep fires at most once per
  window, not continuously.
- **`warm`** — one always-on scheduler task. Correct and simple, with the corresponding
  always-on compute cost.

The clean long-term answer is a small backend addition — an idempotent `POST /internal/sweep`
endpoint — which would let the scheduler be a pure Lambda with no running task. Not built yet.

## Caveats / deploy-time verification

- **`terraform validate` passes offline; a real `plan`/`apply` needs AWS credentials.** The
  data sources (AZs, the fck-nat AMI, managed CloudFront policies) resolve only against a
  real account. Verify these against your account/region on first plan.
- **Readiness window.** The runtime image is distroless (no shell), so there is no container
  health check; ECS registers a task in Cloud Map when it reaches RUNNING, slightly before
  `/readyz` is green. For a few seconds after a cold wake, a request may hit a task still
  catching up projections (reads briefly stale; the ordered log is still the source of truth).
- **Embedded UI origin.** When `serve_embedded_ui_from_api = true`, CloudFront leaves `/`
  unchanged and forwards it to the embedded UI handler. It must not configure `index.html` as
  a CloudFront default root object, because that path is a directory-style redirect in Go's
  file server and would loop at the edge.
- **Cold-wake latency** = Fargate task start + image pull + PostgreSQL connection + the
  projection tail catch-up. Because a durable Postgres store resumes from its stored
  checkpoint (not a full replay), the catch-up is incremental — but the wake is still seconds,
  not instant. This is exactly what the S3/wasm dehydrated front end is meant to mask.
- **fck-nat AMI.** `data.aws_ami.fck_nat` resolves the latest published fck-nat AL2023 arm64
  image; pin `fck_nat_ami_id` if you need a specific version or mirror it into your account.
- **Cloud Map SRV + HTTP API.** The VPC Link integration targets a Cloud Map service that ECS
  registers with SRV records (carrying the task port). Confirm end-to-end reachability on
  first deploy — this is the piece with the least offline verifiability.
- **Secrets and state.** The encryption key and bootstrap key are generated by Terraform and
  therefore stored in state. The database URL belongs to fck-rds. Use an encrypted,
  access-controlled remote backend and rotate the bootstrap key after first use.

## Teardown

Empty the S3 bucket before `terraform destroy`. The shared fck-rds tenant is managed by the
environment-level fck-rds module rather than by this application module.
