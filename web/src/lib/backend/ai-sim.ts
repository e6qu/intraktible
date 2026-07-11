// SPDX-License-Identifier: AGPL-3.0-or-later
// The simulated-LLM provider the demo page registers for the embedded backend's
// "js" AI provider (platform/ai/js.go). It is a PROVIDER, not a code fork: the
// backend calls it exactly as it would an OpenAI-compatible endpoint, records
// the same round-trip, and bills the same usage fields. The simulation
// synthesizes a plausible reply — a structured verdict when the agent declares
// an output schema, else an analyst-style narrative shaped by the prompt.

interface SchemaProp {
  type?: string;
}

interface AIRequest {
  model?: string;
  system?: string;
  prompt: string;
  schema?: { properties?: Record<string, SchemaProp>; required?: string[] };
}

interface GraphNode {
  id: string;
  type: string;
  name: string;
  config?: Record<string, unknown>;
}

interface GraphEdge {
  from: string;
  to: string;
  branch?: string;
}

interface AIResponse {
  text?: string;
  structured?: Record<string, unknown>;
  model: string;
  usage: { prompt_tokens: number; completion_tokens: number };
}

/** Registers the hook the wasm backend's "js" provider calls. */
export function registerSimulatedAI(): void {
  (globalThis as Record<string, unknown>).__intraktible_ai = async (
    reqJSON: string
  ): Promise<string> => {
    const req = JSON.parse(reqJSON) as AIRequest;
    const context = contextOf(req.prompt);
    const fields = req.schema?.properties
      ? Object.keys(req.schema.properties)
      : (req.schema?.required ?? []);
    let text: string;
    let structured: Record<string, unknown> | undefined;
    if (isGraphSchema(req.schema)) {
      structured = draftGraph(req.prompt);
      text = JSON.stringify(structured, null, 2);
    } else if (fields.length > 0) {
      structured = Object.fromEntries(
        fields.map((k) => [k, plausibleField(k, req.prompt, context)])
      );
      text = JSON.stringify(structured, null, 2);
    } else {
      text = narrative(req.prompt, context);
    }
    const resp: AIResponse = {
      text,
      structured,
      model: simulatedModel(req.model),
      usage: {
        prompt_tokens: Math.ceil((req.system ?? '').length / 4 + req.prompt.length / 4),
        completion_tokens: Math.ceil(text.length / 4)
      }
    };
    return JSON.stringify(resp);
  };
}

// The AI node sends the running decision record inside the prompt; recover it
// so the rationale agrees with the verdict shown next to it.
function contextOf(prompt: string): Record<string, unknown> | undefined {
  const start = prompt.indexOf('{');
  if (start < 0) return undefined;
  try {
    return JSON.parse(prompt.slice(start)) as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

function dispositionOf(ctx?: Record<string, unknown>): 'approve' | 'decline' | 'refer' | undefined {
  if (!ctx) return undefined;
  const d = String(ctx.disposition ?? ctx.decision ?? ctx.outcome ?? '').toLowerCase();
  if (/approve|accept|clear|pass/.test(d)) return 'approve';
  if (/decline|reject|deny|fail/.test(d)) return 'decline';
  if (/refer|review|escalate/.test(d)) return 'refer';
  if (ctx.approved === true) return 'approve';
  if (ctx.approved === false) return 'decline';
  const pd = Number(ctx.pd ?? ctx.probability ?? ctx.probability_of_default);
  if (Number.isFinite(pd)) return pd >= 0.5 ? 'decline' : pd <= 0.1 ? 'approve' : 'refer';
  const risk = Number(ctx.risk ?? ctx.risk_score ?? ctx.score);
  if (Number.isFinite(risk)) return risk >= 70 ? 'decline' : risk <= 40 ? 'approve' : 'refer';
  return undefined;
}

function narrative(prompt: string, context?: Record<string, unknown>): string {
  const p = prompt.toLowerCase();
  const disp = dispositionOf(context);
  if (disp === 'decline')
    return 'The risk drivers exceed policy appetite — affordability is stretched and the modelled default probability is high. Recommend declining; the top contributing factors form the adverse-action reasons.';
  if (disp === 'approve')
    return 'The risk drivers sit comfortably within policy — affordability is sound and the modelled default probability is low. Recommend approval at the assessed limit.';
  if (disp === 'refer')
    return 'The risk drivers are borderline against policy, with key factors close to threshold. Recommend referring for manual underwriting review.';
  if (
    /sanction|watchlist|pep|aml|wire|structuring|shell|jurisdiction|pass-through|deposit|launder/.test(
      p
    )
  )
    return 'Screened against sanctions and watchlists; the funding pattern (cross-border value, layering signals) warrants enhanced due diligence. Recommend referring for review and drafting a SAR before clearing.';
  if (/fraud|velocity|device|chargeback|dispute/.test(p))
    return 'Transaction signals show elevated risk (velocity and device anomalies). Recommend a temporary hold pending review.';
  if (/credit|income|dti|underwrit|loan|limit|adverse|rationale|risk driver|decline|approv/.test(p))
    return 'Affordability is borderline relative to the requested exposure. Recommend manual underwriting review.';
  if (/kyc|identity|document|passport/.test(p))
    return 'Identity evidence is largely consistent; one attribute needs corroboration. Recommend a brief verification step.';
  return 'Reviewed the input; no disqualifying signals were identified. Recommend referring for a final decision.';
}

function plausibleField(name: string, prompt: string, context?: Record<string, unknown>): unknown {
  const n = name.toLowerCase();
  const p = prompt.toLowerCase();
  const disp = dispositionOf(context);
  if (/prob/.test(n)) return 0.62;
  if (/risk|score/.test(n)) return 58;
  if (/decision|disposition|recommendation|outcome/.test(n))
    return disp ?? (/clear|approve|pass/.test(p) ? 'approve' : 'refer');
  if (/flag|hit|match|suspicious|blocked/.test(n)) return /sanction|watchlist|fraud|pep/.test(p);
  if (/narrative|summary|reason|rationale|explanation|notes?/.test(n))
    return narrative(prompt, context);
  if (/confidence/.test(n)) return 0.8;
  return narrative(prompt, context).split('.')[0];
}

// The demo is a simulation, not a real model, and this must never read as one:
// the real model name (empty for the copilot, or an agent's pinned model on an AI
// node) is prefixed so every decision/agent/observability view labels the output
// as simulated on its own, not only via the global DemoBanner. Idempotent so a
// value that is already marked is not double-prefixed.
export function simulatedModel(model?: string): string {
  if (!model) return 'simulated-llm';
  return model.startsWith('simulated-') ? model : `simulated-${model}`;
}

// The copilot's flow-generation call is the one request whose schema declares a
// graph (object/array `nodes` and `edges`); it needs a real graph, not the
// field-by-field guesses plausibleField makes for a verdict schema.
function isGraphSchema(schema?: AIRequest['schema']): boolean {
  const props = schema?.properties;
  if (!props) return false;
  return props.nodes?.type === 'array' && props.edges?.type === 'array';
}

// A scorecard factor: a compilable expr-lang condition and its weight. The
// condition references fields the flow's inputs would carry; validation is a
// syntax-only compile (no evaluation), so unknown identifiers are fine.
interface Factor {
  when: string;
  weight: number;
}

// Keyword-driven risk factors, ordered most-specific first. The synthesized
// scorecard sums the factors that fit the requirement, so a "fraud" prompt drafts
// velocity/device factors and a "kyc" prompt drafts sanctions/identity ones.
const FACTOR_RULES: { match: RegExp; label: string; factors: Factor[] }[] = [
  {
    match: /fraud|velocity|device|chargeback|dispute|scam/,
    label: 'Fraud',
    factors: [
      { when: 'transaction_velocity > 5', weight: 40 },
      { when: 'new_device', weight: 25 }
    ]
  },
  {
    match: /sanction|watchlist|pep|aml|kyc|identity|onboard|launder/,
    label: 'KYC & sanctions',
    factors: [
      { when: 'sanctions_hit', weight: 60 },
      { when: '!identity_verified', weight: 30 }
    ]
  },
  {
    match: /credit|loan|underwrit|dti|income|fico|delinquen|afford/,
    label: 'Credit risk',
    factors: [
      { when: 'dti > 0.4', weight: 35 },
      { when: 'fico < 660', weight: 30 }
    ]
  },
  {
    match: /limit|amount|exposure|balance|spend/,
    label: 'Exposure',
    factors: [{ when: 'requested_amount > 50000', weight: 20 }]
  }
];

// draftGraph synthesizes a plausible, ALWAYS-VALID decision flow from the prompt:
// input → scorecard → split, with the split's yes/no branches ending at two
// outputs. That shape satisfies every ValidateFlow rule — one input, an output,
// no dead ends, fully connected, acyclic, and both "yes" and "no" edges on the
// split — so the drafted graph always publishes. Deterministic: the same prompt
// always yields the same graph (no Math.random), matching the seeded demo.
export function draftGraph(prompt: string): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const p = prompt.toLowerCase();
  const matched = FACTOR_RULES.filter((r) => r.match.test(p));
  const label = matched[0]?.label ?? 'Risk';
  const factors = matched.flatMap((r) => r.factors);
  // A prompt that matches no keyword still gets a valid, non-empty scorecard.
  if (factors.length === 0) factors.push({ when: 'true', weight: 25 });

  const nodes: GraphNode[] = [
    { id: 'input', type: 'input', name: 'Application intake' },
    {
      id: 'score',
      type: 'scorecard',
      name: `${label} scorecard`,
      config: { output: 'risk_score', factors }
    },
    { id: 'gate', type: 'split', name: 'High-risk?', config: { condition: 'risk_score >= 50' } },
    {
      id: 'refer',
      type: 'output',
      name: 'Refer for review',
      config: { fields: ['risk_score'] }
    },
    {
      id: 'approve',
      type: 'output',
      name: 'Auto-approve',
      config: { fields: ['risk_score'] }
    }
  ];
  const edges: GraphEdge[] = [
    { from: 'input', to: 'score' },
    { from: 'score', to: 'gate' },
    { from: 'gate', to: 'refer', branch: 'yes' },
    { from: 'gate', to: 'approve', branch: 'no' }
  ];
  return { nodes, edges };
}
