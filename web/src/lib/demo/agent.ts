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
  schema?: AgentSchema,
  context?: Record<string, unknown>
): { text: string; structured?: Record<string, unknown> } {
  const fields = schema?.properties
    ? Object.keys(schema.properties)
    : Array.isArray(schema?.required)
      ? schema.required
      : [];
  if (fields.length > 0) {
    const entries = fields.map((k) => [k, plausibleField(k, prompt, context)] as const);
    const structured = Object.fromEntries(entries);
    return { text: JSON.stringify(structured, null, 2), structured };
  }
  return { text: narrative(prompt, context) };
}

// Map the running decision record to a verdict so a generated rationale agrees with the
// outcome shown next to it (an approved and a declined applicant must not get identical
// text). Reads an explicit disposition/approved flag first, else infers from the model
// signals (probability of default, risk score) the credit flow computes upstream.
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

export function narrative(prompt: string, context?: Record<string, unknown>): string {
  const p = prompt.toLowerCase();
  // Decision-aware: when the record carries the verdict, shape the rationale to match it.
  const disp = dispositionOf(context);
  if (disp === 'decline')
    return 'The risk drivers exceed policy appetite — affordability is stretched and the modelled default probability is high. Recommend declining; the top contributing factors form the adverse-action reasons.';
  if (disp === 'approve')
    return 'The risk drivers sit comfortably within policy — affordability is sound and the modelled default probability is low. Recommend approval at the assessed limit.';
  if (disp === 'refer')
    return 'The risk drivers are borderline against policy, with key factors close to threshold. Recommend referring for manual underwriting review.';
  if (/sanction|watchlist|pep|aml/.test(p))
    return 'Screened against sanctions and watchlists. A potential match warrants enhanced due diligence; recommend referring for review before clearing.';
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
