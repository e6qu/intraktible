// SPDX-License-Identifier: AGPL-3.0-or-later
// Agent registry seed: seven production-ish agents with immutable version
// histories, output schemas and tool grants where the job calls for them, eval
// suites with a deliberate mix of passing and failing cases (real teams have red
// rows), and a month of run logs — completed, failed with believable provider
// errors, and one still streaming — whose prompt/output lengths feed the token
// usage and cost rollups.

import type { Agent, AgentRun, AgentVersion, EvalCase } from '$lib/api';
import { ago, AVA, DIEGO, PRIYA } from './base';

export function seedAgents(): Agent[] {
  return [
    {
      name: 'aml-narrative',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'You write concise SAR narratives from transaction context, citing the triggering typology.',
      schema: { type: 'object', properties: { narrative: { type: 'string' } } },
      tools: ['lookup_entity', 'sanctions_check'],
      latest: 3,
      runs: 0, // derived from seeded runs in createState
      updated_at: ago(20)
    },
    {
      name: 'kyc-extract',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a submitted identity document.',
      schema: {
        type: 'object',
        properties: {
          name: { type: 'string' },
          dob: { type: 'string' },
          doc_number: { type: 'string' }
        }
      },
      tools: [],
      latest: 2,
      runs: 0,
      updated_at: ago(60)
    },
    {
      name: 'dispute-summarizer',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Summarize a cardholder dispute and recommend representment or refund.',
      schema: {
        type: 'object',
        properties: { summary: { type: 'string' }, recommendation: { type: 'string' } }
      },
      tools: ['lookup_transaction'],
      latest: 2,
      runs: 0,
      updated_at: ago(44)
    },
    {
      name: 'fraud-explainer',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'Explain a fraud model score in plain language for an analyst.',
      tools: [],
      latest: 2,
      runs: 0,
      updated_at: ago(15)
    },
    {
      name: 'collections-planner',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'Propose a hardship payment plan within program guardrails from the verified income change and balance.',
      schema: {
        type: 'object',
        properties: {
          plan_months: { type: 'number' },
          rate_relief: { type: 'number' },
          summary: { type: 'string' }
        }
      },
      tools: ['income_verification', 'account_lookup'],
      latest: 2,
      runs: 0,
      updated_at: ago(46)
    },
    {
      name: 'claims-adjuster-brief',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'Draft an adjuster brief for a purchase-protection claim: liability, coverage position, recommendation.',
      schema: {
        type: 'object',
        properties: { recommendation: { type: 'string' }, rationale: { type: 'string' } }
      },
      tools: ['policy_lookup', 'claim_history'],
      latest: 2,
      runs: 0,
      updated_at: ago(118)
    },
    {
      name: 'merchant-memo',
      provider: 'anthropic',
      model: 'claude-opus',
      system: 'Write an underwriting memo for a merchant application from its risk profile.',
      tools: ['registry_lookup', 'web_research'],
      latest: 1,
      runs: 0,
      updated_at: ago(28)
    }
  ];
}

export function seedAgentVersions(): Map<string, AgentVersion[]> {
  const m = new Map<string, AgentVersion[]>();
  m.set('aml-narrative', [
    {
      version: 3,
      etag: 'av3',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'You write concise SAR narratives from transaction context, citing the triggering typology.',
      published_at: ago(20),
      published_by: PRIYA
    },
    {
      version: 2,
      etag: 'av2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'You write concise SAR narratives from transaction context.',
      published_at: ago(120),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'av1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Draft a SAR narrative.',
      published_at: ago(300),
      published_by: AVA
    }
  ]);
  m.set('kyc-extract', [
    {
      version: 2,
      etag: 'kv2',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a submitted identity document.',
      published_at: ago(60),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'kv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract KYC fields.',
      published_at: ago(220),
      published_by: PRIYA
    }
  ]);
  m.set('dispute-summarizer', [
    {
      version: 2,
      etag: 'dv2',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Summarize a cardholder dispute and recommend representment or refund.',
      published_at: ago(44),
      published_by: DIEGO
    },
    {
      version: 1,
      etag: 'dv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Summarize a dispute.',
      published_at: ago(190),
      published_by: DIEGO
    }
  ]);
  m.set('fraud-explainer', [
    {
      version: 2,
      etag: 'fv2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'Explain a fraud model score in plain language for an analyst.',
      published_at: ago(15),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'fv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Explain a fraud score.',
      published_at: ago(130),
      published_by: PRIYA
    }
  ]);
  m.set('collections-planner', [
    {
      version: 2,
      etag: 'cv2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'Propose a hardship payment plan within program guardrails from the verified income change and balance.',
      published_at: ago(46),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'cv1',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'Propose a hardship payment plan.',
      published_at: ago(140),
      published_by: DIEGO
    }
  ]);
  m.set('claims-adjuster-brief', [
    {
      version: 2,
      etag: 'clv2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'Draft an adjuster brief for a purchase-protection claim: liability, coverage position, recommendation.',
      published_at: ago(118),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'clv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Draft an adjuster brief.',
      published_at: ago(260),
      published_by: PRIYA
    }
  ]);
  m.set('merchant-memo', [
    {
      version: 1,
      etag: 'mv1',
      provider: 'anthropic',
      model: 'claude-opus',
      system: 'Write an underwriting memo for a merchant application from its risk profile.',
      published_at: ago(28),
      published_by: AVA
    }
  ]);
  return m;
}

// Eval suites with a real red/green mix: the failing cases are genuine capability
// gaps (the stub reply never names a typology or cites SHAP attributions), so the
// eval page reads like a team mid-hardening, not a vanity dashboard.
export function seedAgentEvals(): Map<string, EvalCase[]> {
  const m = new Map<string, EvalCase[]>();
  m.set('aml-narrative', [
    {
      name: 'produces narrative',
      prompt: 'Wire of $50,000 to a sanctioned region',
      mode: 'contains',
      expect: 'narrative'
    },
    {
      name: 'handles structuring',
      prompt: 'Structuring across 6 deposits under threshold',
      mode: 'contains',
      expect: 'narrative'
    },
    {
      name: 'names the typology',
      prompt: 'Layered transfers through 3 shell companies',
      mode: 'contains',
      expect: 'typology'
    }
  ]);
  m.set('kyc-extract', [
    {
      name: 'extracts passport',
      prompt: 'Passport, DOB 1990-01-01',
      mode: 'contains',
      expect: 'doc_number'
    },
    {
      name: 'extracts name field',
      prompt: 'Driver license, Jane A. Doe',
      mode: 'contains',
      expect: 'name'
    }
  ]);
  m.set('dispute-summarizer', [
    {
      name: 'recommends action',
      prompt: 'Chargeback for non-receipt, $210',
      mode: 'contains',
      expect: 'recommendation'
    },
    {
      name: 'cites network reason code',
      prompt: 'Visa 10.4 card-absent fraud dispute, $740',
      mode: 'contains',
      expect: '10.4'
    }
  ]);
  m.set('fraud-explainer', [
    {
      name: 'explains drivers',
      prompt: 'Score 88: high velocity, new device',
      mode: 'contains',
      expect: 'velocity'
    },
    {
      name: 'cites feature attributions',
      prompt: 'Score 91: explain with SHAP attributions',
      mode: 'contains',
      expect: 'SHAP'
    }
  ]);
  m.set('collections-planner', [
    {
      name: 'proposes a plan',
      prompt: 'Income dropped 40% after layoff; balance $12,400',
      mode: 'contains',
      expect: 'plan_months'
    },
    {
      name: 'includes rate relief',
      prompt: 'Medical hardship, 3 missed payments',
      mode: 'contains',
      expect: 'rate_relief'
    }
  ]);
  m.set('claims-adjuster-brief', [
    {
      name: 'recommends pay/deny',
      prompt: 'Stolen laptop claim, $1,900 against $3,000 coverage',
      mode: 'contains',
      expect: 'recommendation'
    },
    {
      name: 'flags subrogation',
      prompt: 'Damaged in courier transit, carrier at fault',
      mode: 'contains',
      expect: 'subrogation'
    }
  ]);
  m.set('merchant-memo', [
    {
      name: 'writes a memo',
      prompt: 'Crypto exchange, projected $250k monthly volume',
      mode: 'contains',
      expect: 'Recommend'
    }
  ]);
  return m;
}

// One seed row per run, each with a handcrafted output SPECIFIC to its prompt (a
// structuring prompt gets a structuring narrative, an over-invoicing prompt reads
// as trade-based laundering — a real run log never repeats itself verbatim).
// Schema-bearing agents carry `structured` and render it as pretty JSON, exactly
// the shape agentReply produces at runtime; error text mirrors what real providers
// return so the run log's failure rows teach the retry/escalate story.
interface RunSeed {
  agent: string;
  model: string;
  prompt: string;
  hrs: number;
  status?: AgentRun['status'];
  error?: string;
  output?: string;
  structured?: Record<string, unknown>;
}

const RUNS: RunSeed[] = [
  // aml-narrative — the busiest agent (every SAR referral drafts through it).
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Wire of $50,000 to a high-risk jurisdiction',
    hrs: 6,
    structured: {
      narrative:
        'Customer initiated a single outbound wire of $50,000 to a beneficiary bank in a high-risk jurisdiction, against a 12-month average of under $3,000/month in account activity. No documented business relationship with the counterparty exists on file. Typology: high-risk corridor transfer. Recommend filing a SAR and applying 90-day enhanced monitoring.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Structuring pattern across 6 deposits',
    hrs: 20,
    structured: {
      narrative:
        'Six cash deposits between $8,600 and $9,700 were made across three branches within an 11-day window, each below the $10,000 CTR threshold, followed by a consolidated outbound transfer of $54,100. The branch rotation and amount clustering indicate deliberate threshold avoidance. Typology: structuring. Recommend filing a SAR covering the full deposit series.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Rapid pass-through funding from a shell entity',
    hrs: 34,
    structured: {
      narrative:
        'Funds totaling $130,000 arrived from an entity incorporated 6 weeks ago with no operating footprint, and 96% was forwarded to three unrelated accounts within 48 hours, leaving a near-zero balance. The account functions as a conduit rather than a business. Typology: pass-through / funnel account layering. Recommend a SAR and exit review of the relationship.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Cash deposits just under the reporting threshold',
    hrs: 78,
    structured: {
      narrative:
        'Recurring cash deposits of $9,000–$9,900 posted on 9 of the last 12 business days into a single account, with no corresponding business revenue declared at onboarding. The consistency of just-sub-threshold amounts is inconsistent with legitimate cash flow. Typology: threshold avoidance (structuring). Recommend a SAR and a source-of-funds request.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Cross-border transfer to a PEP-linked account',
    hrs: 96,
    status: 'failed',
    error: 'provider timeout after 30s (upstream 504)'
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Round-tripping between affiliated accounts',
    hrs: 150,
    structured: {
      narrative:
        'A circular flow of $78,000 moved through three accounts under common beneficial ownership and returned to the originating account within five business days, net of $2,100 in fees, with no goods, services, or investment activity attached. Typology: round-tripping between affiliates. Recommend a SAR citing the absence of economic purpose.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Unusual surge in inbound remittances',
    hrs: 260,
    structured: {
      narrative:
        'Inbound remittance volume rose 9x month-over-month, arriving from 14 distinct senders across four countries with no prior relationship to the customer, followed by same-day ATM withdrawals of 80% of received value. The pattern matches a collection account. Typology: money mule / remittance aggregation. Recommend a SAR and account restriction pending contact.'
    }
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Trade-based laundering via over-invoicing',
    hrs: 410,
    structured: {
      narrative:
        'Invoice values on seven trade payments exceed referenced market prices for the declared goods by 40–65%, with settlement routed through an intermediary in a third country unrelated to the shipping route. The mispricing transfers value beyond the goods exchanged. Typology: trade-based money laundering (over-invoicing). Recommend a SAR and trade-document review.'
    }
  },
  // kyc-extract
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Passport, DOB 1990-01-01',
    hrs: 18,
    structured: { name: 'Amelia R. Novak', dob: '1990-01-01', doc_number: 'P4811940US' }
  },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Utility bill, address verification',
    hrs: 44,
    structured: { name: 'Marcus T. Oyelaran', dob: '', doc_number: 'ACCT-118842-ELEC' }
  },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Company registration extract',
    hrs: 70,
    structured: { name: 'Ollivanders Trading Ltd', dob: '2011-06-14', doc_number: 'HRB 88291' }
  },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Driver license, expired',
    hrs: 96,
    status: 'failed',
    error: 'schema validation failed: missing required field "doc_number"'
  },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Proof of funds statement',
    hrs: 122,
    structured: { name: 'Ingrid Svensson', dob: '1984-11-02', doc_number: 'STMT-8817-2026' }
  },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Residence permit, machine-readable zone',
    hrs: 300,
    structured: { name: 'Yusuf Demir', dob: '1979-03-22', doc_number: 'RP7724031TR' }
  },
  // dispute-summarizer
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Chargeback for non-receipt, $210',
    hrs: 12,
    structured: {
      summary:
        'Cardholder claims a $210 order never arrived; merchant tracking shows the parcel stalled at the origin facility and no proof of delivery exists.',
      recommendation: 'refund'
    }
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Duplicate charge dispute, $89',
    hrs: 32,
    structured: {
      summary:
        'Two identical $89 authorizations posted 40 seconds apart with the same order id; the merchant confirms a gateway retry after a timeout.',
      recommendation: 'refund'
    }
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Fraudulent transaction claim, $740',
    hrs: 52,
    status: 'failed',
    error: 'rate limited by provider (429); retry after 12s'
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Subscription not canceled, $29',
    hrs: 72,
    structured: {
      summary:
        'Cardholder canceled on the 3rd but was billed $29 on the 12th; the merchant cancellation log confirms the request preceded the billing cycle.',
      recommendation: 'refund'
    }
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Quality dispute, $1,200',
    hrs: 92,
    structured: {
      summary:
        'Cardholder disputes a $1,200 furniture charge citing damage on arrival; the merchant holds a signed delivery acceptance and offered repair, which was refused.',
      recommendation: 'representment'
    }
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Card-absent fraud, $460, device mismatch',
    hrs: 210,
    structured: {
      summary:
        'A $460 card-absent charge came from a device and IP never seen on the account while the cardholder transacted in-store in another city within the hour.',
      recommendation: 'refund'
    }
  },
  // fraud-explainer
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 88: high velocity, new device',
    hrs: 4,
    output:
      'The 88 is driven primarily by velocity — 9 authorizations in the past hour against a baseline of 1–2 per day — compounded by a first-seen device fingerprint. Amount contributes little. Velocity alone accounts for roughly two-thirds of the elevation; recommend step-up authentication before the next approval.'
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 41: mid risk, mismatched geo',
    hrs: 13,
    output:
      'A mid-band 41: the dominant driver is the geo mismatch between the IP country and the card home region. Velocity and device history are clean, which holds the score below the 80 block line; passive monitoring is sufficient.'
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 12: low risk, trusted device',
    hrs: 22,
    output:
      'Low risk at 12: a recognized device with 14 months of history, an in-pattern amount, and no velocity signal. No single feature contributes materially — approve without friction.'
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 92: account takeover signals',
    hrs: 31,
    status: 'failed',
    error: 'context window exceeded: 214k tokens > 200k limit'
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 33: card-present, recurring merchant',
    hrs: 40,
    output:
      'A 33 on a card-present transaction at a recurring merchant: the physical read and merchant familiarity suppress the modest amount deviation. Below the review band — no analyst action required.'
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 76: ticket 9x average, VPN exit node',
    hrs: 170,
    output:
      'The 76 combines a ticket 9x the account average with an IP flagged as a commercial VPN exit node. Either signal alone lands mid-band; together they cross the review threshold. Recommend a hold pending cardholder confirmation.'
  },
  // collections-planner
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Income dropped 40% after layoff; balance $12,400',
    hrs: 10,
    structured: {
      plan_months: 12,
      rate_relief: 0.5,
      summary:
        'Verified 40% income reduction after involuntary layoff. A 12-month plan at 50% rate relief brings the payment to 31% of current disposable income, within program guardrails.'
    }
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Medical hardship, 3 missed payments, balance $6,300',
    hrs: 36,
    structured: {
      plan_months: 9,
      rate_relief: 0.5,
      summary:
        'Documented medical event with three missed payments. Nine months at 50% relief cures the arrears by month four while staying under the 36% payment-to-income cap.'
    }
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Divorce settlement pending, income halved',
    hrs: 60,
    status: 'failed',
    error: 'provider timeout after 30s (upstream 504)'
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Seasonal worker, income resumes in March',
    hrs: 130,
    structured: {
      plan_months: 6,
      rate_relief: 0.25,
      summary:
        'Income verified to resume in March; a 6-month bridge at 25% relief with a deferred first payment aligns the step-up with the documented re-employment date.'
    }
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Small-business owner, revenue down 60%',
    hrs: 320,
    structured: {
      plan_months: 12,
      rate_relief: 0.5,
      summary:
        'Business bank feeds confirm a 60% revenue decline. The 12-month/50% concession exceeds the standard authority band — route to supervisor countersign per program guardrails.'
    }
  },
  // claims-adjuster-brief
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Stolen laptop claim, $1,900 against $3,000 coverage',
    hrs: 16,
    structured: {
      recommendation: 'pay',
      rationale:
        'Police report and purchase receipt are consistent; the $1,900 claim sits at 63% of coverage with no prior claims in 24 months. Severity is high but the documentation is clean.'
    }
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Cracked phone screen, $240, third claim this year',
    hrs: 58,
    structured: {
      recommendation: 'refer to SIU',
      rationale:
        'Third claim in twelve months on the same line: individually low-value, but the frequency exceeds the abuse threshold. Hold payment pending a claim-history review.'
    }
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Damaged in courier transit, carrier at fault',
    hrs: 110,
    structured: {
      recommendation: 'pay and subrogate',
      rationale:
        'Transit damage is a covered peril and the carrier scan log places the damage in their custody. Pay the insured now and pursue subrogation against the courier.'
    }
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Claim on a policy 12 days old, near coverage limit',
    hrs: 140,
    status: 'failed',
    error: 'tool call failed: claim_history returned 503'
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Water-damaged headphones, receipt attached',
    hrs: 380,
    structured: {
      recommendation: 'pay',
      rationale:
        'Receipt validates ownership and price; accidental liquid damage is covered, the amount is well under limit, and this is the customer first claim.'
    }
  },
  // merchant-memo — opus, unpriced in the demo price table (usage without cost).
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'Crypto exchange, projected $250k monthly volume',
    hrs: 26,
    output:
      'Underwriting memo — crypto exchange, projected $250k/month. MCC 6051 lands in the enhanced-review tier: MSB registration verified in two of three operating states, chargeback exposure modeled low, funding-source risk elevated. Recommend approval with a 10% rolling reserve, a $100k monthly cap for the first 90 days, and quarterly licensing re-checks.'
  },
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'High-risk MCC 7995, offshore directors',
    hrs: 88,
    output:
      'Underwriting memo — MCC 7995 (gambling) with two of four directors resident offshore. The registry extract confirms beneficial ownership, but the payout jurisdiction lacks a mutual enforcement treaty. Recommend decline at standard terms; reconsider only with a domestic guarantor entity and a 15% rolling reserve.'
  },
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'Established grocery chain, 12 locations',
    hrs: 200,
    output:
      'Underwriting memo — 12-location grocery chain with nine years of processing history and a 0.02% chargeback ratio. MCC 5411 is low-risk and volume is seasonally stable. Recommend approval at standard MDR with no reserve, on an annual review cycle.'
  },
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'Nutraceutical subscription merchant, 2.9% chargeback rate',
    hrs: 0,
    status: 'running'
  }
];

export function seedAgentRuns(): AgentRun[] {
  return RUNS.map((r, i) => {
    const status = r.status ?? 'completed';
    // A completed seed row must carry its handcrafted output — a missing one is a
    // seed bug, never something to paper over with the generic stub reply.
    const text = r.structured ? JSON.stringify(r.structured, null, 2) : (r.output ?? '');
    if (status === 'completed' && !text) {
      throw new Error(`seed: run for "${r.prompt}" (${r.agent}) has no output`);
    }
    return {
      run_id: `run_${i + 1}`,
      agent: r.agent,
      model: r.model,
      prompt: r.prompt,
      status,
      text: status === 'completed' ? text : '',
      structured: status === 'completed' ? r.structured : undefined,
      error: r.error,
      at: ago(r.hrs)
    };
  });
}
