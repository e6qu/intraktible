#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# End-to-end demo: exercises all four intraktible components through the public
# API, then the operator tooling. Start the server first, e.g.:
#
#     INTRAKTIBLE_AI_STUB=1 go run ./cmd/intraktible serve   # in another terminal (stub AI for the demo agent)
#     ./examples/demo.sh
#
# Override the target with BASE / KEY env vars.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
KEY="${KEY:-dev-sandbox-key}"
H=(-fsS -H "X-Api-Key: ${KEY}" -H "Content-Type: application/json")
jq() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)"; }

say() { printf '\n=== %s ===\n' "$1"; }

say "Context Layer — define an entity, record events, define + compute a feature"
curl "${H[@]}" -d '{"entity_type":"customer","entity_id":"acme","attributes":{"tier":"gold"}}' \
  "$BASE/v1/context/entities" >/dev/null
for i in 1 2 3; do
  curl "${H[@]}" -d '{"entity_type":"customer","entity_id":"acme","event_name":"transaction","data":{"amount":100}}' \
    "$BASE/v1/context/events" >/dev/null
done
curl "${H[@]}" -d '{"name":"txn_count_24h","entity_type":"customer","event_name":"transaction","aggregation":"count","window_hours":24}' \
  "$BASE/v1/context/features" >/dev/null
sleep 0.3
echo "features for customer/acme:"
curl "${H[@]}" "$BASE/v1/context/entities/customer/acme/features"; echo

say "Context Layer — define a connector and fetch from it (recorded)"
curl "${H[@]}" -d '{"name":"bureau","type":"mock_bureau"}' "$BASE/v1/context/connectors" >/dev/null
echo "bureau says:"
curl "${H[@]}" -d '{"params":{"subject":"Acme Corp"}}' "$BASE/v1/context/connectors/bureau/fetch"; echo

say "Agent Manager — define an agent and run it"
curl "${H[@]}" -d '{"name":"assess","system":"assess onboarding risk"}' "$BASE/v1/agents" >/dev/null
echo "agent run:"
curl "${H[@]}" -d '{"prompt":"Is Acme Corp risky?"}' "$BASE/v1/agents/assess/run"; echo

say "Decision Engine — publish a flow that calls the bureau and escalates risky cases"
FID=$(curl "${H[@]}" -d '{"slug":"onboard","name":"Onboarding"}' "$BASE/v1/flows" | jq "['flow_id']")
GRAPH='{"graph":{"nodes":[
  {"id":"in","type":"input"},
  {"id":"c","type":"connect","config":{"connector":"bureau","output":"bureau"}},
  {"id":"a","type":"ai","config":{"agent":"assess","output":"assessment"}},
  {"id":"s","type":"split","config":{"condition":"connect.bureau.risk_score >= 50"}},
  {"id":"review","type":"manual_review","config":{"company_name":"subject","case_type":"'"'"'aml'"'"'","sla_days":3}},
  {"id":"approve","type":"assignment","config":{"assignments":[{"target":"decision","expr":"'"'"'APPROVE'"'"'"}]}},
  {"id":"out","type":"output"}],
  "edges":[
  {"from":"in","to":"c"},{"from":"c","to":"a"},{"from":"a","to":"s"},
  {"from":"s","to":"review","branch":"yes"},{"from":"s","to":"approve","branch":"no"},
  {"from":"review","to":"out"},{"from":"approve","to":"out"}]}}'
curl "${H[@]}" -d "$GRAPH" "$BASE/v1/flows/$FID/versions" >/dev/null
sleep 0.3
echo "decide for subject 'Acme Corp' (bureau risk_score >= 50 -> manual review):"
curl "${H[@]}" -d '{"data":{"subject":"Acme Corp"}}' "$BASE/v1/flows/onboard/sandbox/decide"; echo

say "Case Manager — the decision's manual_review node opened a case"
sleep 0.3
curl "${H[@]}" "$BASE/v1/cases" | python3 -c "import sys,json;cs=json.load(sys.stdin)['cases'];print(f'{len(cs)} case(s):', [(c['company_name'],c['case_type'],c['status']) for c in cs])"

say "Operator tooling (run against the same --data-dir)"
echo "  intraktible log                 # audit: every event in the log"
echo "  intraktible replay              # rebuild all projections from the log"
echo "  intraktible replay --as-of 5    # log-based rollback: state as of seq 5"
