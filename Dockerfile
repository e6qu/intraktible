# SPDX-License-Identifier: AGPL-3.0-or-later
# Stage 1: build the SvelteKit UI
FROM node:22-bookworm-slim AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: build the static Go binary with the UI embedded
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY . .
COPY --from=web /web/build ./platform/web/assets
RUN CGO_ENABLED=0 go build -trimpath -o /out/intraktible ./cmd/intraktible

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
