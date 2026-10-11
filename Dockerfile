# SPDX-License-Identifier: AGPL-3.0-or-later
# Stage 1: build the SvelteKit UI
# The build images come from the Amazon ECR Public copy of Docker's official
# images, pinned to the same index digest Docker Hub serves: Docker Hub limits
# anonymous pulls per address, and the shared CI runners exhaust it.
FROM public.ecr.aws/docker/library/node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392 AS web
# Build provenance shown in the UI footer. .git is not in this build context, so the
# values come from build args (the release workflow passes the short SHA + UTC build
# time); they default to 'dev'/now for a bare `docker build`.
ARG GIT_SHA=dev
ARG BUILD_TIME=
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
ENV PUBLIC_GIT_SHA=$GIT_SHA
ENV PUBLIC_BUILD_TIME=$BUILD_TIME
RUN npm run build

# Stage 2: build the static Go binary with the UI embedded
FROM public.ecr.aws/docker/library/golang:1.27-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ARG GIT_SHA=dev
ARG BUILD_TIME=
WORKDIR /src
COPY . .
COPY --from=web /web/build ./platform/web/assets
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-X github.com/e6qu/intraktible/platform/httpx.releaseRevision=${GIT_SHA} -X github.com/e6qu/intraktible/platform/httpx.releaseBuiltAt=${BUILD_TIME}" \
    -o /out/intraktible ./cmd/intraktible

# Stage 3: minimal runtime (single self-contained artifact)
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/intraktible /intraktible
EXPOSE 8080
VOLUME ["/data"]
# Ship with a DURABLE store: the well-known dev key is seeded ONLY with the
# in-memory store, so a durable store means the public image never grants admin via
# a known secret. To obtain the first credential set INTRAKTIBLE_BOOTSTRAP_API_KEY
# (a real secret) or configure SSO. For a real deployment also set INTRAKTIBLE_ENV=
# production, INTRAKTIBLE_ENCRYPTION_KEY, and a networked store/log — see docs/DEPLOY.md.
ENTRYPOINT ["/intraktible", "serve", "--addr=:8080", "--data-dir=/data", "--store=sqlite"]
