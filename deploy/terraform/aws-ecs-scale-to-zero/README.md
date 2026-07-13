<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# intraktible on AWS — scale-to-zero (ECS + API Gateway + S3 + Aurora)

A Terraform root module that deploys intraktible into a **private VPC** where the compute
and database **scale to zero when idle** and the app still loads from **S3** in the
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
   egress: private subnets ──▶ fck-nat (public subnet)                     │ Aurora Svless v2│ min 0 ACU
                                                                            └────────────────┘
```

- **VPC** with public + private subnets across `az_count` AZs. ECS and Aurora are in
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
- **Aurora PostgreSQL Serverless v2** with `min_capacity = 0` — pauses to zero when idle,
  resumes on the next connection. Used as **both** the event log (`--log=postgres`) and the
  projection store (`--store=postgres`), so N stateless API tasks share one ordered log and
  durable read models.
- **Secrets Manager** for the at-rest encryption key, the bootstrap admin key, and the
  composed Postgres DSN, injected into the task at runtime.

## Prerequisites

1. **Build and push the backend image** (the repo `Dockerfile`) to a registry the ECS
   task can pull (ECR in the same account is simplest):
   ```sh
   aws ecr create-repository --repository-name intraktible
   docker build -t <account>.dkr.ecr.<region>.amazonaws.com/intraktible:<tag> .
   docker push  <account>.dkr.ecr.<region>.amazonaws.com/intraktible:<tag>
   ```
2. **A remote state backend you control** (the module generates secrets that land in
   state — use S3 + SSE-KMS with locking; see *Secrets and state* below).

## Apply

```sh
terraform init
terraform apply \
  -var 'container_image=<account>.dkr.ecr.<region>.amazonaws.com/intraktible:<tag>' \
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

## Cost model (idle vs active)

| Component        | Idle (no traffic)                 | Active                          |
|------------------|-----------------------------------|---------------------------------|
| CloudFront       | ~$0 (per-request)                 | per-request + egress            |
| S3               | pennies (storage)                 | + per-request                   |
| API Gateway      | $0 (per-request)                  | per-request                     |
| ECS Fargate      | **$0 (0 tasks)**                  | per running task-second         |
| Aurora Svless v2 | **$0 compute** (paused) + storage | per ACU-hour                    |
| fck-nat          | ~$3/mo (one small instance)       | + egress data                   |
| Secrets/logs     | pennies                           | pennies                         |

The intended idle floor is roughly **fck-nat + storage** — no hourly load balancer, no
running compute, no database compute.

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
would double-deliver alerts). An always-on scheduler would also hold Aurora awake, defeating
database scale-to-zero. So:

- **`scheduled`** (default) — the scheduler service sits at `0`. EventBridge wakes it on
  `scheduler_window_cron`; the controller Lambda scales it to `1`, waits
  `scheduler_run_minutes` (during which it runs sweeps every `monitor_interval`), then scales
  it back to `0`. Aurora can pause between windows. Trade-off: a sweep fires at most once per
  window, not continuously.
- **`warm`** — one always-on scheduler task. Correct and simple, but its periodic sweeps keep
  Aurora from pausing, so the database no longer scales to zero.

The clean long-term answer is a small backend addition — an idempotent `POST /internal/sweep`
endpoint — which would let the scheduler be a pure Lambda with no running task and let Aurora
stay paused except during the brief sweep. Not built yet.

## Caveats / deploy-time verification

- **`terraform validate` passes offline; a real `plan`/`apply` needs AWS credentials.** The
  data sources (AZs, the fck-nat AMI, managed CloudFront policies) resolve only against a
  real account. Verify these against your account/region on first plan.
- **Readiness window.** The runtime image is distroless (no shell), so there is no container
  health check; ECS registers a task in Cloud Map when it reaches RUNNING, slightly before
  `/readyz` is green. For a few seconds after a cold wake, a request may hit a task still
  catching up projections (reads briefly stale; the ordered log is still the source of truth).
- **Cold-wake latency** = Fargate task start + image pull + Aurora resume-from-zero + the
  projection tail catch-up. Because a durable Postgres store resumes from its stored
  checkpoint (not a full replay), the catch-up is incremental — but the wake is still seconds,
  not instant. This is exactly what the S3/wasm dehydrated front end is meant to mask.
- **fck-nat AMI.** `data.aws_ami.fck_nat` resolves the latest published fck-nat AL2023 arm64
  image; pin `fck_nat_ami_id` if you need a specific version or mirror it into your account.
- **Cloud Map SRV + HTTP API.** The VPC Link integration targets a Cloud Map service that ECS
  registers with SRV records (carrying the task port). Confirm end-to-end reachability on
  first deploy — this is the piece with the least offline verifiability.
- **Secrets and state.** The encryption key, bootstrap key, and DB password are generated by
  Terraform and therefore stored in state. Use an encrypted, access-controlled remote backend,
  and rotate the bootstrap key after first use. To keep a secret out of state entirely, create
  the Secrets Manager secret out-of-band and remove the `random_password` + version here.
- **RDS Proxy.** Omitted on purpose (it is always-on and would not scale to zero). At the
  default `api_max_tasks = 4` the connection fan-out is small. Add a proxy only if you raise
  task counts substantially.

## Teardown

`aws_rds_cluster` has `deletion_protection = true` and takes a final snapshot. Empty the S3
bucket, then disable deletion protection before `terraform destroy`.
