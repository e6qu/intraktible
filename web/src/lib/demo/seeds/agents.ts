// SPDX-License-Identifier: AGPL-3.0-or-later
// Agent registry seed: seven production-ish agents with immutable version
// histories, output schemas and tool grants where the job calls for them, eval
// suites with a deliberate mix of passing and failing cases (real teams have red
// rows), and a month of run logs — completed, failed with believable provider
// errors, and one still streaming — whose prompt/output lengths feed the token
// usage and cost rollups.

import type { Agent, AgentRun, AgentVersion, EvalCase } from '$lib/api';
import { agentReply } from '../agent';
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

// One seed row per run; error text mirrors what real providers return so the run
// log's failure rows teach the retry/escalate story.
interface RunSeed {
  agent: string;
  model: string;
  prompt: string;
  hrs: number;
  status?: AgentRun['status'];
  error?: string;
}

const RUNS: RunSeed[] = [
  // aml-narrative — the busiest agent (every SAR referral drafts through it).
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Wire of $50,000 to a high-risk jurisdiction',
    hrs: 6
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Structuring pattern across 6 deposits',
    hrs: 20
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Rapid pass-through funding from a shell entity',
    hrs: 34
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Cash deposits just under the reporting threshold',
    hrs: 78
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
    hrs: 150
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Unusual surge in inbound remittances',
    hrs: 260
  },
  {
    agent: 'aml-narrative',
    model: 'claude-sonnet',
    prompt: 'Trade-based laundering via over-invoicing',
    hrs: 410
  },
  // kyc-extract
  { agent: 'kyc-extract', model: 'claude-haiku', prompt: 'Passport, DOB 1990-01-01', hrs: 18 },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Utility bill, address verification',
    hrs: 44
  },
  { agent: 'kyc-extract', model: 'claude-haiku', prompt: 'Company registration extract', hrs: 70 },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Driver license, expired',
    hrs: 96,
    status: 'failed',
    error: 'schema validation failed: missing required field "doc_number"'
  },
  { agent: 'kyc-extract', model: 'claude-haiku', prompt: 'Proof of funds statement', hrs: 122 },
  {
    agent: 'kyc-extract',
    model: 'claude-haiku',
    prompt: 'Residence permit, machine-readable zone',
    hrs: 300
  },
  // dispute-summarizer
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Chargeback for non-receipt, $210',
    hrs: 12
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Duplicate charge dispute, $89',
    hrs: 32
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
    hrs: 72
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Quality dispute, $1,200',
    hrs: 92
  },
  {
    agent: 'dispute-summarizer',
    model: 'claude-haiku',
    prompt: 'Card-absent fraud, $460, device mismatch',
    hrs: 210
  },
  // fraud-explainer
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 88: high velocity, new device',
    hrs: 4
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 41: mid risk, mismatched geo',
    hrs: 13
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 12: low risk, trusted device',
    hrs: 22
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
    hrs: 40
  },
  {
    agent: 'fraud-explainer',
    model: 'claude-sonnet',
    prompt: 'Score 76: ticket 9x average, VPN exit node',
    hrs: 170
  },
  // collections-planner
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Income dropped 40% after layoff; balance $12,400',
    hrs: 10
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Medical hardship, 3 missed payments, balance $6,300',
    hrs: 36
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
    hrs: 130
  },
  {
    agent: 'collections-planner',
    model: 'claude-sonnet',
    prompt: 'Small-business owner, revenue down 60%',
    hrs: 320
  },
  // claims-adjuster-brief
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Stolen laptop claim, $1,900 against $3,000 coverage',
    hrs: 16
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Cracked phone screen, $240, third claim this year',
    hrs: 58
  },
  {
    agent: 'claims-adjuster-brief',
    model: 'claude-sonnet',
    prompt: 'Damaged in courier transit, carrier at fault',
    hrs: 110
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
    hrs: 380
  },
  // merchant-memo — opus, unpriced in the demo price table (usage without cost).
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'Crypto exchange, projected $250k monthly volume',
    hrs: 26
  },
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'High-risk MCC 7995, offshore directors',
    hrs: 88
  },
  {
    agent: 'merchant-memo',
    model: 'claude-opus',
    prompt: 'Established grocery chain, 12 locations',
    hrs: 200
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
    return {
      run_id: `run_${i + 1}`,
      agent: r.agent,
      model: r.model,
      prompt: r.prompt,
      status,
      text: status === 'completed' ? agentReply(r.prompt).text : '',
      structured: undefined,
      error: r.error,
      at: ago(r.hrs)
    };
  });
}
