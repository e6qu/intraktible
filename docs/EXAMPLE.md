# Worked example — an end-to-end decision

This walks the whole platform: the Context Layer (entities, events, features,
connectors), the Agent Manager, the Decision Engine, and the Case Manager — plus
the operator tooling. A runnable version is [`examples/demo.sh`](../examples/demo.sh).

```sh
go run ./cmd/intraktible serve            # terminal 1 (serves :8080, dev key dev-sandbox-key)
./examples/demo.sh                        # terminal 2
```

All requests are tenant-scoped (org `demo` / workspace `main`, from the dev key)
and authenticated with `X-Api-Key: dev-sandbox-key`.

## 1. Context Layer — entities, events, features

Record an entity and some events, define a windowed feature, then read it back
(computed at the read boundary, so the stored log stays clock-free):

```sh
curl -H "X-Api-Key: dev-sandbox-key" -H 'Content-Type: application/json' \
  -d '{"entity_type":"customer","entity_id":"acme","attributes":{"tier":"gold"}}' \
  localhost:8080/v1/context/entities
# ... record a few transaction events for customer/acme ...
curl ... -d '{"name":"txn_count_24h","entity_type":"customer","event_name":"transaction","aggregation":"count","window_hours":24}' \
  localhost:8080/v1/context/features
curl -H "X-Api-Key: dev-sandbox-key" localhost:8080/v1/context/entities/customer/acme/features
# {"features":[{"name":"txn_count_24h","value":3}]}
```

## 2. Context Layer — a connector

Define a connector and invoke it; the response is recorded as an event (replay
reads the stored response, never a re-fetch):

```sh
curl ... -d '{"name":"bureau","type":"mock_bureau"}' localhost:8080/v1/context/connectors
curl ... -d '{"params":{"subject":"Acme Corp"}}' localhost:8080/v1/context/connectors/bureau/fetch
# {"fetch_id":"…","response":{"risk_score":64,"sanctioned":false,"subject":"Acme Corp"}}
```

## 3. Agent Manager — an agent

Define an agent (a config over the pluggable AI provider) and run it; the run is
recorded for audit/monitoring:

```sh
curl ... -d '{"name":"assess","system":"assess onboarding risk"}' localhost:8080/v1/agents
curl ... -d '{"prompt":"Is Acme Corp risky?"}' localhost:8080/v1/agents/assess/run
# {"run_id":"…","status":"completed","text":"stub: Is Acme Corp risky?"}
```

## 4. Decision Engine — a flow that ties it together

Publish a flow whose **Connect** node calls the bureau, whose **AI** node runs the
agent, and which escalates risky subjects through a **manual_review** node:

```
input → connect(bureau) → ai(assess) → split(connect.bureau.risk_score >= 50)
                                          ├─ yes → manual_review → output
                                          └─ no  → assignment(decision=APPROVE) → output
```

The shell pre-resolves the connector and agent before execution (so the pure core
does no I/O) and records the results in the decision; `decide` returns the trace:

```sh
curl ... -d '{"data":{"subject":"Acme Corp"}}' localhost:8080/v1/flows/onboard/production/decide
# status "completed"; data carries connect.bureau.* and ai.assessment.*
```

## 5. Case Manager — the escalation lands

Because the bureau scored ≥ 50, the `manual_review` node emitted
`decision.manual_review_requested`, which the Case Manager consumed (cross-component
via the event log only) to open a case:

```sh
curl -H "X-Api-Key: dev-sandbox-key" localhost:8080/v1/cases
# 1 case: Acme Corp / aml / needs_review  (linked to the source decision)
```

## 6. Operate — audit, replay, rollback

Everything above is durable events; the read models are rebuildable projections.
Point the operator commands at the same `--data-dir`:

```sh
intraktible log                  # every event in the log, plus a per-stream summary
intraktible replay               # rebuild all projections from the log into a fresh store
intraktible replay --as-of 5     # log-based rollback: rebuild state as of seq 5 (read-only)
```
