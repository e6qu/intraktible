// SPDX-License-Identifier: AGPL-3.0-or-later
// A deterministic stand-in for an LLM agent's response in the static demo. The real
// product calls a configured provider (platform/ai); here we synthesize a plausible
// reply — a structured JSON verdict when the agent declares an output schema, else a
// short analyst-style narrative shaped by the prompt — so the demo never shows the
// literal "stub: <prompt>" echo. Pure (no store/state) so the seed and the runtime
// both use it without a circular import.

export type AgentSchema = { properties?: Record<string, unknown>; required?: string[] };

export function agentReply(
  prompt: string,
  schema?: AgentSchema
): { text: string; structured?: Record<string, unknown> } {
  const fields = schema?.properties
    ? Object.keys(schema.properties)
    : Array.isArray(schema?.required)
      ? schema.required
      : [];
  if (fields.length > 0) {
    const entries = fields.map((k) => [k, plausibleField(k, prompt)] as const);
    const structured = Object.fromEntries(entries);
    return { text: JSON.stringify(structured, null, 2), structured };
  }
  return { text: narrative(prompt) };
}

export function narrative(prompt: string): string {
  const p = prompt.toLowerCase();
  if (/sanction|watchlist|pep|aml/.test(p))
    return 'Screened against sanctions and watchlists. A potential match warrants enhanced due diligence; recommend referring for review before clearing.';
  if (/fraud|velocity|device|chargeback|dispute/.test(p))
    return 'Transaction signals show elevated risk (velocity and device anomalies). Recommend a temporary hold pending review.';
  if (/credit|income|dti|underwrit|loan|limit/.test(p))
    return 'Affordability is borderline relative to the requested exposure. Recommend manual underwriting review.';
  if (/kyc|identity|document|passport/.test(p))
    return 'Identity evidence is largely consistent; one attribute needs corroboration. Recommend a brief verification step.';
  return 'Reviewed the input; no disqualifying signals were identified. Recommend referring for a final decision.';
}

function plausibleField(name: string, prompt: string): unknown {
  const n = name.toLowerCase();
  const p = prompt.toLowerCase();
  if (/prob/.test(n)) return 0.62;
  if (/risk|score/.test(n)) return 58;
  if (/decision|disposition|recommendation|outcome/.test(n))
    return /clear|approve|pass/.test(p) ? 'approve' : 'refer';
  if (/flag|hit|match|suspicious|blocked/.test(n)) return /sanction|watchlist|fraud|pep/.test(p);
  if (/narrative|summary|reason|rationale|explanation|notes?/.test(n)) return narrative(prompt);
  if (/confidence/.test(n)) return 0.8;
  return narrative(prompt).split('.')[0];
}
