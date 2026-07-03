// SPDX-License-Identifier: AGPL-3.0-or-later
// The flow fleet: ten decisioning domains a mid-size fintech runs, each with a real
// version history (older graphs preserved as published versions), environment
// deployments (champion/challenger arms, staged rollouts), maker-checker deployment
// requests and a strict promotion policy. Every latest graph is deep (8–14 nodes,
// swimlanes, real branch expressions) and the fleet collectively exercises all 14
// node types the engine executes. Graph expressions are REAL — the seed generator
// (decisions.ts) walks these exact graphs, so every recorded trace is consistent
// with the logic shown in the builder.

import type { Flow } from '$lib/api';
import { ago, AVA, MARCUS, PRIYA, DIEGO } from './base';

// strict promotion policy: gates tighten as you climb toward production. Reused by
// every flow so the maker-checker / promotion surfaces read consistently.
const STRICT_PROMOTION = {
  sandbox: {
    require_assertions: false,
    require_no_firing_monitors: false,
    allow_force: true,
    require_review: false
  },
  staging: {
    require_assertions: true,
    require_no_firing_monitors: true,
    allow_force: true,
    require_review: false
  },
  production: {
    require_assertions: true,
    require_no_firing_monitors: true,
    allow_force: false,
    require_review: true
  }
};

// --- Consumer credit -------------------------------------------------------------

// v1 — the original thin flow: DTI → PD model → 2-way band.
const creditGraphV1 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Loan application', lane: 'Intake' },
    {
      id: 'enrich',
      type: 'assignment' as const,
      name: 'Compute DTI',
      lane: 'Score',
      config: { assignments: [{ target: 'dti', expr: '(debt / income)' }] }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'PD model',
      lane: 'Score',
      config: { model: 'credit_pd', output: 'pd' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Risk',
      lane: 'Decide',
      config: { assignments: [{ target: 'risk', expr: 'predict.pd.probability * 100' }] }
    },
    { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
    {
      id: 'approve',
      type: 'assignment' as const,
      name: 'Approve',
      lane: 'Decide',
      config: { assignments: [{ target: 'approved', expr: 'true' }] }
    },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Refer',
      lane: 'Decide',
      config: { case_type: 'credit_review', sla_days: 3 }
    },
    { id: 'out', type: 'output' as const, name: 'Decision', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'enrich' },
    { from: 'enrich', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'band' },
    { from: 'band', to: 'approve', branch: 'risk < 50' },
    { from: 'band', to: 'review', branch: 'risk >= 50' },
    { from: 'approve', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

// Shared by v2/v3: bureau enrichment, dual models (propensity + PD), affordability-
// aware limit, adverse-action narrative, 3-way band with underwriter referral.
function creditCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Loan application', lane: 'Intake' },
    {
      id: 'enrich',
      type: 'assignment' as const,
      name: 'Enrich bureau features',
      lane: 'Intake',
      config: {
        assignments: [
          { target: 'dti', expr: '(debt / income)' },
          { target: 'utilization', expr: '(revolving_balance / credit_limit)' },
          { target: 'delinquencies', expr: 'delinquencies_24m' },
          { target: 'fico_score', expr: 'fico_score' }
        ]
      }
    },
    {
      id: 'propensity',
      type: 'predict' as const,
      name: 'Repayment propensity',
      lane: 'Score',
      config: { model: 'repayment_propensity', output: 'propensity' }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Probability of default',
      lane: 'Score',
      config: { model: 'credit_pd', output: 'pd' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Derive risk + limit',
      lane: 'Score',
      config: {
        assignments: [
          { target: 'risk', expr: 'predict.pd.probability * 100' },
          {
            target: 'offered_limit',
            expr: 'risk >= 70 ? 0 : ((income - debt) / 12 * 4 < income * 0.1 ? (income - debt) / 12 * 4 : income * 0.1)'
          }
        ]
      }
    },
    {
      id: 'narrative',
      type: 'ai' as const,
      name: 'Adverse-action draft',
      lane: 'Score',
      config: {
        prompt: 'Draft an adverse-action rationale from the risk drivers',
        output: 'rationale'
      }
    },
    { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Underwriter review',
      lane: 'Decide',
      config: { case_type: 'credit_review', sla_days: 3 }
    },
    {
      id: 'approve',
      type: 'assignment' as const,
      name: 'Approve',
      lane: 'Decide',
      config: { assignments: [{ target: 'approved', expr: 'true' }] }
    },
    {
      id: 'decline',
      type: 'assignment' as const,
      name: 'Decline',
      lane: 'Decide',
      config: { assignments: [{ target: 'approved', expr: 'false' }] }
    },
    {
      id: 'out',
      type: 'output' as const,
      name: 'Credit decision',
      lane: 'Decide',
      config: { assignments: [{ target: 'flow', expr: '"credit-decision"' }] }
    }
  ];
}

function creditCoreEdges() {
  return [
    { from: 'enrich', to: 'propensity' },
    { from: 'propensity', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'narrative', to: 'band' },
    { from: 'band', to: 'approve', branch: 'risk < 35' },
    { from: 'band', to: 'decline', branch: 'risk >= 70' },
    { from: 'band', to: 'review', branch: 'risk >= 35' },
    { from: 'approve', to: 'out' },
    { from: 'decline', to: 'out' },
    { from: 'review', to: 'out' }
  ];
}

const creditGraphV2 = {
  nodes: creditCoreNodes(),
  edges: [{ from: 'in', to: 'enrich' }, { from: 'derive', to: 'narrative' }, ...creditCoreEdges()]
};

// v3 adds the live Experian pull and a Reg B / FCRA adverse-action reason node, so
// declines carry specific permissible codes rather than generic band labels.
const creditGraphV3 = {
  nodes: [
    ...creditCoreNodes(),
    {
      id: 'bureau',
      type: 'connect' as const,
      name: 'Experian bureau pull',
      lane: 'Intake',
      config: { connector: 'experian', output: 'bureau' }
    },
    {
      id: 'adverse',
      type: 'reason' as const,
      name: 'Adverse-action codes',
      lane: 'Score',
      config: {
        reasons: [
          {
            when: 'dti >= 0.43',
            code: 'DTI_TOO_HIGH',
            description: 'Debt-to-income ratio too high'
          },
          {
            when: 'fico_score < 620',
            code: 'LOW_SCORE',
            description: 'Credit score below threshold'
          },
          {
            when: 'delinquencies >= 2',
            code: 'DELINQUENCY_HISTORY',
            description: 'Serious delinquency on file'
          },
          {
            when: 'utilization >= 0.75',
            code: 'UTILIZATION_HIGH',
            description: 'Revolving utilization too high'
          }
        ]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'bureau' },
    { from: 'bureau', to: 'enrich' },
    { from: 'derive', to: 'adverse' },
    { from: 'adverse', to: 'narrative' },
    ...creditCoreEdges()
  ]
};

// Examples land a sampled "Sample input" run in a real (mid) band so a test run
// routes a branch and returns a disposition instead of failing "no branch matched".
const creditSchema = {
  type: 'object',
  properties: {
    income: { type: 'number', example: 52000 },
    debt: { type: 'number', example: 14000 },
    revolving_balance: { type: 'number', example: 4200 },
    credit_limit: { type: 'number', example: 12000 },
    delinquencies_24m: { type: 'number', example: 0 },
    fico_score: { type: 'number', example: 668 },
    tenure_years: { type: 'number', example: 4 },
    employment_stability: { type: 'number', example: 0.8 }
  }
};

// --- AML screening ----------------------------------------------------------------

const amlGraphV1 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Transaction', lane: 'Intake' },
    {
      id: 'rule',
      type: 'assignment' as const,
      name: 'Flag large',
      lane: 'Score',
      config: { assignments: [{ target: 'aml_score', expr: 'amount / 10000' }] }
    },
    { id: 'band', type: 'split' as const, name: 'Band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Review',
      lane: 'Decide',
      config: { case_type: 'aml_alert', sla_days: 5 }
    },
    {
      id: 'clear',
      type: 'assignment' as const,
      name: 'Clear',
      lane: 'Decide',
      config: { assignments: [{ target: 'cleared', expr: 'true' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Outcome', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'rule' },
    { from: 'rule', to: 'band' },
    { from: 'band', to: 'review', branch: 'aml_score >= 2' },
    { from: 'band', to: 'clear', branch: 'aml_score < 2' },
    { from: 'clear', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

function amlCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Wire / transfer', lane: 'Intake' },
    {
      id: 'feat',
      type: 'assignment' as const,
      name: 'Screening features',
      lane: 'Enrich',
      config: {
        assignments: [
          { target: 'cross_border', expr: 'origin_country != dest_country ? 1 : 0' },
          { target: 'high_value', expr: 'amount > 10000 ? 1 : 0' }
        ]
      }
    },
    {
      id: 'sanctions',
      type: 'assignment' as const,
      name: 'Sanctions hit',
      lane: 'Enrich',
      config: {
        assignments: [{ target: 'sanctions_hit', expr: 'watchlist_score >= 80 ? 1 : 0' }]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'AML risk score',
      lane: 'Score',
      config: { model: 'aml_risk', output: 'aml' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Compose risk',
      lane: 'Score',
      config: {
        assignments: [{ target: 'aml_score', expr: 'predict.aml.score + sanctions_hit * 5' }]
      }
    },
    {
      id: 'sar',
      type: 'ai' as const,
      name: 'SAR narrative draft',
      lane: 'Score',
      config: {
        prompt: 'Draft a SAR narrative from the transaction context',
        output: 'narrative'
      }
    },
    { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'AML analyst review',
      lane: 'Decide',
      config: { case_type: 'aml_alert', sla_days: 5 }
    },
    {
      id: 'clear',
      type: 'assignment' as const,
      name: 'Clear',
      lane: 'Decide',
      config: { assignments: [{ target: 'cleared', expr: 'true' }] }
    }
  ];
}

const amlGraphV2 = {
  nodes: [
    ...amlCoreNodes(),
    {
      id: 'out',
      type: 'output' as const,
      name: 'Screening outcome',
      lane: 'Decide',
      config: {
        assignments: [{ target: 'cleared', expr: 'sanctions_hit == 1 ? false : aml_score < 6' }]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'sanctions' },
    { from: 'sanctions', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'sar' },
    { from: 'sar', to: 'band' },
    { from: 'band', to: 'review', branch: 'sanctions_hit == 1' },
    { from: 'band', to: 'review', branch: 'aml_score >= 6' },
    { from: 'band', to: 'clear', branch: 'aml_score < 6' },
    { from: 'clear', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

// v3 adds the structuring heuristics (a code node) the champion misses — the
// challenger arm on staging measures how many extra referrals it produces.
const amlGraphV3 = {
  nodes: [
    ...amlCoreNodes(),
    {
      id: 'struct',
      type: 'code' as const,
      name: 'Structuring heuristics',
      lane: 'Enrich',
      config: {
        language: 'javascript',
        source:
          '// classic sub-threshold structuring + rapid pass-through\nstructuring = deposits_30d >= 4 && amount < 10000 ? 1 : 0\nrapid_movement = outflow_ratio > 0.9 ? 1 : 0'
      }
    },
    {
      id: 'out',
      type: 'output' as const,
      name: 'Screening outcome',
      lane: 'Decide',
      config: {
        assignments: [
          {
            target: 'cleared',
            expr: 'sanctions_hit != 1 && structuring != 1 && aml_score < 6'
          }
        ]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'sanctions' },
    { from: 'sanctions', to: 'struct' },
    { from: 'struct', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'sar' },
    { from: 'sar', to: 'band' },
    { from: 'band', to: 'review', branch: 'sanctions_hit == 1' },
    { from: 'band', to: 'review', branch: 'structuring == 1' },
    { from: 'band', to: 'review', branch: 'aml_score >= 6' },
    { from: 'band', to: 'clear', branch: 'aml_score < 6' },
    { from: 'clear', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

const amlSchema = {
  type: 'object',
  properties: {
    amount: { type: 'number', example: 52000 },
    origin_country: { type: 'string', example: 'US' },
    dest_country: { type: 'string', example: 'KY' },
    watchlist_score: { type: 'number', example: 10 },
    deposits_30d: { type: 'number', example: 2 },
    outflow_ratio: { type: 'number', example: 0.4 }
  }
};

// --- KYC onboarding ----------------------------------------------------------------

function kycCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Onboarding packet', lane: 'Intake' },
    {
      id: 'extract',
      type: 'ai' as const,
      name: 'Document extract',
      lane: 'Enrich',
      config: { prompt: 'Extract KYC fields from the submitted documents', output: 'extracted' }
    },
    {
      id: 'pep',
      type: 'assignment' as const,
      name: 'PEP / adverse media',
      lane: 'Enrich',
      config: {
        assignments: [
          { target: 'pep_flag', expr: 'pep_match >= 1 ? 1 : 0' },
          { target: 'doc_quality', expr: 'doc_score' }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'KYC vendor score',
      lane: 'Score',
      config: { model: 'kyc_score', output: 'kyc' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Identity confidence',
      lane: 'Score',
      config: { assignments: [{ target: 'identity_conf', expr: 'doc_quality - pep_flag * 40' }] }
    },
    { id: 'gate', type: 'split' as const, name: 'Verify gate', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'EDD review',
      lane: 'Decide',
      config: { case_type: 'kyc_review', sla_days: 2 }
    },
    {
      id: 'pass',
      type: 'assignment' as const,
      name: 'Verified',
      lane: 'Decide',
      config: { assignments: [{ target: 'verified', expr: 'true' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Onboarding result', lane: 'Decide', config: {} }
  ];
}

function kycCoreEdges() {
  return [
    { from: 'extract', to: 'pep' },
    { from: 'pep', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'gate' },
    { from: 'gate', to: 'review', branch: 'identity_conf < 60' },
    { from: 'gate', to: 'pass', branch: 'identity_conf >= 60' },
    { from: 'pass', to: 'out' },
    { from: 'review', to: 'out' }
  ];
}

const kycGraphV1 = {
  nodes: kycCoreNodes(),
  edges: [{ from: 'in', to: 'extract' }, ...kycCoreEdges()]
};

const kycGraphV2 = {
  nodes: [
    ...kycCoreNodes(),
    {
      id: 'docv',
      type: 'connect' as const,
      name: 'Jumio doc verification',
      lane: 'Enrich',
      config: { connector: 'jumio-kyc', output: 'docv' }
    }
  ],
  edges: [{ from: 'in', to: 'docv' }, { from: 'docv', to: 'extract' }, ...kycCoreEdges()]
};

const kycSchema = {
  type: 'object',
  properties: {
    doc_score: { type: 'number', example: 55 },
    pep_match: { type: 'number', example: 0 }
  }
};

// --- Card fraud ----------------------------------------------------------------------

function fraudCoreNodes(withExplain: boolean) {
  return [
    { id: 'in', type: 'input' as const, name: 'Authorization', lane: 'Intake' },
    {
      id: 'feat',
      type: 'assignment' as const,
      name: 'Velocity + device',
      lane: 'Enrich',
      config: {
        assignments: [
          { target: 'velocity', expr: 'tx_count_1h' },
          { target: 'device_risk', expr: 'device_score' },
          { target: 'amount_ratio', expr: '(amount / avg_ticket)' }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Fraud model',
      lane: 'Score',
      config: { model: 'fraud_score', output: 'fraud' }
    },
    ...(withExplain
      ? [
          {
            id: 'explain',
            type: 'ai' as const,
            name: 'Explanation',
            lane: 'Score',
            config: {
              prompt: 'Explain the fraud score drivers for the analyst',
              output: 'explanation'
            }
          }
        ]
      : []),
    { id: 'band', type: 'split' as const, name: 'Fraud band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Fraud analyst review',
      lane: 'Decide',
      config: { case_type: 'fraud_review', sla_days: 1 }
    },
    {
      id: 'block',
      type: 'assignment' as const,
      name: 'Block',
      lane: 'Decide',
      config: { assignments: [{ target: 'blocked', expr: 'true' }] }
    },
    {
      id: 'allow',
      type: 'assignment' as const,
      name: 'Allow',
      lane: 'Decide',
      config: { assignments: [{ target: 'blocked', expr: 'false' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Auth decision', lane: 'Decide', config: {} }
  ];
}

function fraudDerive(withTrust: boolean) {
  return {
    id: 'derive',
    type: 'assignment' as const,
    name: 'Fraud probability',
    lane: 'Score',
    config: {
      assignments: [
        {
          target: 'fraud_p',
          expr: withTrust
            ? 'predict.fraud.probability * 100 + (trust_adj ?? 0)'
            : 'predict.fraud.probability * 100'
        }
      ]
    }
  };
}

function fraudBandEdges(reviewAt: number) {
  return [
    { from: 'band', to: 'block', branch: 'fraud_p >= 80' },
    { from: 'band', to: 'review', branch: `fraud_p >= ${reviewAt}` },
    { from: 'band', to: 'allow', branch: `fraud_p < ${reviewAt}` },
    { from: 'block', to: 'out' },
    { from: 'allow', to: 'out' },
    { from: 'review', to: 'out' }
  ];
}

const fraudGraphV1 = {
  nodes: [...fraudCoreNodes(false), fraudDerive(false)],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'band' },
    ...fraudBandEdges(40)
  ]
};

function fraudExplainEdges() {
  return [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'explain' },
    { from: 'explain', to: 'band' }
  ];
}

const fraudGraphV2 = {
  nodes: [...fraudCoreNodes(true), fraudDerive(false)],
  edges: [...fraudExplainEdges(), ...fraudBandEdges(40)]
};

// v3 tightens the review band 40 → 35 (more referrals, fewer silent approvals).
const fraudGraphV3 = {
  nodes: [...fraudCoreNodes(true), fraudDerive(false)],
  edges: [...fraudExplainEdges(), ...fraudBandEdges(35)]
};

// v4 (production challenger) adds trusted-customer rules that shade the probability
// before banding — the experiment is whether it cuts false-positive referrals.
const fraudGraphV4 = {
  nodes: [
    ...fraudCoreNodes(true),
    fraudDerive(true),
    {
      id: 'trust',
      type: 'rule' as const,
      name: 'Trusted-customer rules',
      lane: 'Enrich',
      config: {
        rules: [
          {
            when: 'card_present == 1 && tx_count_1h <= 1',
            then: [{ target: 'trust_adj', expr: '-8' }]
          },
          { when: 'new_device == 1', then: [{ target: 'trust_adj', expr: '6' }] }
        ]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'trust' },
    { from: 'trust', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'explain' },
    { from: 'explain', to: 'band' },
    ...fraudBandEdges(35)
  ]
};

const fraudSchema = {
  type: 'object',
  properties: {
    amount: { type: 'number', example: 240 },
    tx_count_1h: { type: 'number', example: 6 },
    device_score: { type: 'number', example: 45 },
    avg_ticket: { type: 'number', example: 120 },
    card_present: { type: 'number', example: 1 },
    new_device: { type: 'number', example: 0 }
  }
};

// --- Dispute / chargeback triage ------------------------------------------------------

const disputeGraphV1 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Dispute intake', lane: 'Intake' },
    {
      id: 'classify',
      type: 'assignment' as const,
      name: 'Classify + liability',
      lane: 'Triage',
      config: {
        assignments: [
          { target: 'high_value', expr: 'amount > 500 ? 1 : 0' },
          { target: 'liability', expr: 'reason_code == "fraud" ? 1 : 0' }
        ]
      }
    },
    {
      id: 'summary',
      type: 'ai' as const,
      name: 'Dispute summary',
      lane: 'Triage',
      config: {
        prompt: 'Summarize the dispute and recommend representment vs refund',
        output: 'summary'
      }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Triage score',
      lane: 'Triage',
      config: { assignments: [{ target: 'triage', expr: 'high_value * 50 + liability * 40' }] }
    },
    { id: 'band', type: 'split' as const, name: 'Triage band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Disputes ops review',
      lane: 'Decide',
      config: { case_type: 'dispute', sla_days: 7 }
    },
    {
      id: 'refund',
      type: 'assignment' as const,
      name: 'Auto-refund',
      lane: 'Decide',
      config: { assignments: [{ target: 'outcome', expr: '"refund"' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Disposition', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'classify' },
    { from: 'classify', to: 'summary' },
    { from: 'summary', to: 'derive' },
    { from: 'derive', to: 'band' },
    { from: 'band', to: 'review', branch: 'triage >= 50' },
    { from: 'band', to: 'refund', branch: 'triage < 50' },
    { from: 'refund', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

// v2 replaces the liability heuristic with a proper reason-code decision table
// (network rules: what evidence a representment needs, who carries liability).
const disputeGraphV2 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Dispute intake', lane: 'Intake' },
    {
      id: 'liability',
      type: 'decision_table' as const,
      name: 'Reason-code liability',
      lane: 'Triage',
      config: {
        hit: 'first',
        rows: [
          {
            when: 'reason_code == "fraud"',
            outputs: [
              { target: 'liability', expr: '1' },
              { target: 'evidence', expr: '"4837 affidavit + device history"' }
            ]
          },
          {
            when: 'reason_code == "product_not_received"',
            outputs: [
              { target: 'liability', expr: '0' },
              { target: 'evidence', expr: '"carrier tracking + delivery confirmation"' }
            ]
          },
          {
            when: 'reason_code == "duplicate"',
            outputs: [
              { target: 'liability', expr: '0' },
              { target: 'evidence', expr: '"settlement records"' }
            ]
          },
          {
            outputs: [
              { target: 'liability', expr: '0' },
              { target: 'evidence', expr: '"merchant response"' }
            ]
          }
        ]
      }
    },
    {
      id: 'value',
      type: 'assignment' as const,
      name: 'Value tier',
      lane: 'Triage',
      config: { assignments: [{ target: 'high_value', expr: 'amount > 500 ? 1 : 0' }] }
    },
    {
      id: 'summary',
      type: 'ai' as const,
      name: 'Dispute summary',
      lane: 'Triage',
      config: {
        prompt: 'Summarize the dispute and recommend representment vs refund',
        output: 'summary'
      }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Triage score',
      lane: 'Triage',
      config: { assignments: [{ target: 'triage', expr: 'high_value * 50 + liability * 40' }] }
    },
    { id: 'band', type: 'split' as const, name: 'Triage band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Disputes ops review',
      lane: 'Decide',
      config: { case_type: 'dispute', sla_days: 7 }
    },
    {
      id: 'refund',
      type: 'assignment' as const,
      name: 'Auto-refund',
      lane: 'Decide',
      config: { assignments: [{ target: 'outcome', expr: '"refund"' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Disposition', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'liability' },
    { from: 'liability', to: 'value' },
    { from: 'value', to: 'summary' },
    { from: 'summary', to: 'derive' },
    { from: 'derive', to: 'band' },
    { from: 'band', to: 'review', branch: 'triage >= 50' },
    { from: 'band', to: 'refund', branch: 'triage < 50' },
    { from: 'refund', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

const disputeSchema = {
  type: 'object',
  properties: {
    amount: { type: 'number', example: 820 },
    reason_code: { type: 'string', example: 'fraud' }
  }
};

// --- Merchant onboarding --------------------------------------------------------------

const merchantGraphV1 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Merchant application', lane: 'Intake' },
    {
      id: 'feat',
      type: 'assignment' as const,
      name: 'MCC + volume risk',
      lane: 'Enrich',
      config: {
        assignments: [
          { target: 'high_risk_mcc', expr: 'mcc_risk >= 70 ? 1 : 0' },
          { target: 'amount', expr: 'monthly_volume' },
          { target: 'cross_border', expr: 'international ? 1 : 0' }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Merchant risk score',
      lane: 'Score',
      config: { model: 'aml_risk', output: 'mrisk' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Underwriting score',
      lane: 'Score',
      config: {
        assignments: [{ target: 'uw_score', expr: 'predict.mrisk.score + high_risk_mcc * 30' }]
      }
    },
    { id: 'gate', type: 'split' as const, name: 'Underwriting gate', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Underwriting review',
      lane: 'Decide',
      config: { case_type: 'merchant_review', sla_days: 4 }
    },
    {
      id: 'approve',
      type: 'assignment' as const,
      name: 'Board merchant',
      lane: 'Decide',
      config: { assignments: [{ target: 'boarded', expr: 'true' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Boarding result', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'gate' },
    { from: 'gate', to: 'review', branch: 'uw_score >= 25' },
    { from: 'gate', to: 'approve', branch: 'uw_score < 25' },
    { from: 'approve', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

// v2 grades MCC risk through a tiered table (not a binary flag) and drafts an
// underwriting memo for the reviewer.
const merchantGraphV2 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'Merchant application', lane: 'Intake' },
    {
      id: 'feat',
      type: 'assignment' as const,
      name: 'Volume features',
      lane: 'Enrich',
      config: {
        assignments: [
          { target: 'amount', expr: 'monthly_volume' },
          { target: 'high_value', expr: 'monthly_volume > 100000 ? 1 : 0' },
          { target: 'cross_border', expr: 'international ? 1 : 0' }
        ]
      }
    },
    {
      id: 'mcc',
      type: 'decision_table' as const,
      name: 'MCC tier adder',
      lane: 'Enrich',
      config: {
        hit: 'first',
        rows: [
          { when: 'mcc_risk >= 70', outputs: [{ target: 'mcc_adder', expr: '30' }] },
          { when: 'mcc_risk >= 40', outputs: [{ target: 'mcc_adder', expr: '15' }] },
          { outputs: [{ target: 'mcc_adder', expr: '0' }] }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Merchant risk score',
      lane: 'Score',
      config: { model: 'aml_risk', output: 'mrisk' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Underwriting score',
      lane: 'Score',
      config: {
        assignments: [{ target: 'uw_score', expr: 'predict.mrisk.score + mcc_adder' }]
      }
    },
    {
      id: 'memo',
      type: 'ai' as const,
      name: 'Underwriting memo',
      lane: 'Score',
      config: {
        prompt: 'Write an underwriting memo for this merchant application',
        output: 'memo'
      }
    },
    { id: 'gate', type: 'split' as const, name: 'Underwriting gate', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Underwriting review',
      lane: 'Decide',
      config: { case_type: 'merchant_review', sla_days: 4 }
    },
    {
      id: 'approve',
      type: 'assignment' as const,
      name: 'Board merchant',
      lane: 'Decide',
      config: { assignments: [{ target: 'boarded', expr: 'true' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Boarding result', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'feat' },
    { from: 'feat', to: 'mcc' },
    { from: 'mcc', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'memo' },
    { from: 'memo', to: 'gate' },
    { from: 'gate', to: 'review', branch: 'uw_score >= 25' },
    { from: 'gate', to: 'approve', branch: 'uw_score < 25' },
    { from: 'approve', to: 'out' },
    { from: 'review', to: 'out' }
  ]
};

const merchantSchema = {
  type: 'object',
  properties: {
    monthly_volume: { type: 'number', example: 90000 },
    mcc_risk: { type: 'number', example: 55 },
    international: { type: 'number', example: 1 }
  }
};

// --- Collections hardship --------------------------------------------------------------

function collectionsCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Hardship application', lane: 'Intake' },
    {
      id: 'verify',
      type: 'assignment' as const,
      name: 'Verify income change',
      lane: 'Intake',
      config: {
        assignments: [
          { target: 'income_drop', expr: '1 - current_income / prior_income' },
          { target: 'missed', expr: 'missed_payments_6m' }
        ]
      }
    },
    {
      id: 'score',
      type: 'scorecard' as const,
      name: 'Hardship scorecard',
      lane: 'Assess',
      config: {
        output: 'hardship_score',
        factors: [
          { when: 'income_drop >= 0.3', weight: 30 },
          { when: 'missed >= 2', weight: 20 },
          { when: 'medical_event == 1', weight: 25 },
          { when: 'tenure_years >= 3', weight: 10 },
          { when: 'balance_usd > 10000', weight: 15 }
        ]
      }
    },
    { id: 'gate', type: 'split' as const, name: 'Program gate', lane: 'Resolve', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Hardship supervisor review',
      lane: 'Resolve',
      config: { case_type: 'hardship_review', sla_days: 5 }
    },
    {
      id: 'offer',
      type: 'assignment' as const,
      name: 'Offer plan',
      lane: 'Resolve',
      config: { assignments: [{ target: 'enrolled', expr: 'true' }] }
    },
    {
      id: 'standard',
      type: 'assignment' as const,
      name: 'Standard collections',
      lane: 'Resolve',
      config: { assignments: [{ target: 'enrolled', expr: 'false' }] }
    },
    {
      id: 'out',
      type: 'output' as const,
      name: 'Hardship outcome',
      lane: 'Resolve',
      config: {
        assignments: [
          { target: 'outcome', expr: 'enrolled ? "hardship_plan" : "standard_collections"' }
        ]
      }
    }
  ];
}

function collectionsGateEdges() {
  return [
    { from: 'gate', to: 'review', branch: 'hardship_score >= 70' },
    { from: 'gate', to: 'offer', branch: 'hardship_score >= 45' },
    { from: 'gate', to: 'standard', branch: 'hardship_score < 45' },
    { from: 'review', to: 'out' },
    { from: 'offer', to: 'out' },
    { from: 'standard', to: 'out' }
  ];
}

const collectionsGraphV1 = {
  nodes: collectionsCoreNodes(),
  edges: [
    { from: 'in', to: 'verify' },
    { from: 'verify', to: 'score' },
    { from: 'score', to: 'gate' },
    ...collectionsGateEdges()
  ]
};

// v2 adds the plan-terms table (months of relief by hardship band) and a summary
// for the supervisor queue.
const collectionsGraphV2 = {
  nodes: [
    ...collectionsCoreNodes(),
    {
      id: 'plan',
      type: 'decision_table' as const,
      name: 'Plan terms',
      lane: 'Assess',
      config: {
        hit: 'first',
        rows: [
          {
            when: 'hardship_score >= 70',
            outputs: [
              { target: 'plan_months', expr: '12' },
              { target: 'rate_relief', expr: '0.5' }
            ]
          },
          {
            when: 'hardship_score >= 45',
            outputs: [
              { target: 'plan_months', expr: '6' },
              { target: 'rate_relief', expr: '0.25' }
            ]
          },
          {
            outputs: [
              { target: 'plan_months', expr: '0' },
              { target: 'rate_relief', expr: '0' }
            ]
          }
        ]
      }
    },
    {
      id: 'summary',
      type: 'ai' as const,
      name: 'Hardship summary',
      lane: 'Assess',
      config: {
        prompt: 'Summarize the hardship application and the proposed plan for the reviewer',
        output: 'summary'
      }
    }
  ],
  edges: [
    { from: 'in', to: 'verify' },
    { from: 'verify', to: 'score' },
    { from: 'score', to: 'plan' },
    { from: 'plan', to: 'summary' },
    { from: 'summary', to: 'gate' },
    ...collectionsGateEdges()
  ]
};

const collectionsSchema = {
  type: 'object',
  properties: {
    prior_income: { type: 'number', example: 5200 },
    current_income: { type: 'number', example: 3100 },
    missed_payments_6m: { type: 'number', example: 2 },
    medical_event: { type: 'number', example: 0 },
    tenure_years: { type: 'number', example: 4 },
    balance_usd: { type: 'number', example: 8400 }
  }
};

// --- Purchase-protection claim triage ---------------------------------------------------

function claimCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Claim intake', lane: 'Intake' },
    {
      id: 'ratio',
      type: 'assignment' as const,
      name: 'Coverage ratio',
      lane: 'Assess',
      config: {
        assignments: [{ target: 'amount_ratio', expr: 'amount / coverage_limit' }]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Claim abuse model',
      lane: 'Assess',
      config: { model: 'claim_fraud', output: 'cfraud' }
    },
    {
      id: 'severity',
      type: 'assignment' as const,
      name: 'Abuse probability',
      lane: 'Assess',
      config: {
        assignments: [{ target: 'fraud_p', expr: 'predict.cfraud.probability * 100' }]
      }
    },
    {
      id: 'brief',
      type: 'ai' as const,
      name: 'Adjuster brief',
      lane: 'Assess',
      config: {
        prompt: 'Draft an adjuster brief with a pay/deny recommendation',
        output: 'brief'
      }
    },
    { id: 'band', type: 'split' as const, name: 'Triage band', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Adjuster review',
      lane: 'Decide',
      config: { case_type: 'claim_review', sla_days: 3 }
    },
    {
      id: 'pay',
      type: 'assignment' as const,
      name: 'Pay claim',
      lane: 'Decide',
      config: {
        assignments: [
          { target: 'paid', expr: 'true' },
          { target: 'payout', expr: 'amount' }
        ]
      }
    },
    {
      id: 'deny',
      type: 'assignment' as const,
      name: 'Deny claim',
      lane: 'Decide',
      config: { assignments: [{ target: 'paid', expr: 'false' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Claim outcome', lane: 'Decide', config: {} }
  ];
}

const claimGraphV1 = {
  nodes: claimCoreNodes(),
  edges: [
    { from: 'in', to: 'ratio' },
    { from: 'ratio', to: 'score' },
    { from: 'score', to: 'severity' },
    { from: 'severity', to: 'brief' },
    { from: 'brief', to: 'band' },
    { from: 'band', to: 'deny', branch: 'policy_active == 0' },
    { from: 'band', to: 'review', branch: 'fraud_p >= 60' },
    { from: 'band', to: 'review', branch: 'amount_ratio > 0.5' },
    { from: 'band', to: 'pay', branch: 'amount_ratio <= 0.5' },
    { from: 'review', to: 'out' },
    { from: 'pay', to: 'out' },
    { from: 'deny', to: 'out' }
  ]
};

// v2 adds explicit fast-track / lapse rules and a denial-reason node so every
// outcome carries a specific, defensible code.
const claimGraphV2 = {
  nodes: [
    ...claimCoreNodes(),
    {
      id: 'rules',
      type: 'rule' as const,
      name: 'Fast-track rules',
      lane: 'Intake',
      config: {
        rules: [
          {
            when: 'amount <= 200 && policy_active == 1 && prior_claims_24m == 0',
            then: [{ target: 'fast_track', expr: '1' }]
          },
          { when: 'policy_active == 0', then: [{ target: 'lapsed', expr: '1' }] }
        ]
      }
    },
    {
      id: 'reasons',
      type: 'reason' as const,
      name: 'Denial & referral reasons',
      lane: 'Assess',
      config: {
        reasons: [
          {
            when: 'lapsed == 1',
            code: 'POLICY_LAPSED',
            description: 'Protection plan lapsed before the loss date'
          },
          {
            when: 'amount_ratio > 1',
            code: 'OVER_COVERAGE',
            description: 'Claim exceeds the coverage limit'
          },
          {
            when: 'fraud_p >= 60',
            code: 'CLAIM_FRAUD_SIGNALS',
            description: 'Model flags abuse-pattern signals'
          }
        ]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'rules' },
    { from: 'rules', to: 'ratio' },
    { from: 'ratio', to: 'score' },
    { from: 'score', to: 'severity' },
    { from: 'severity', to: 'reasons' },
    { from: 'reasons', to: 'brief' },
    { from: 'brief', to: 'band' },
    { from: 'band', to: 'deny', branch: 'lapsed == 1' },
    { from: 'band', to: 'review', branch: 'fraud_p >= 60' },
    { from: 'band', to: 'pay', branch: 'fast_track == 1' },
    { from: 'band', to: 'review', branch: 'amount_ratio > 0.5' },
    { from: 'band', to: 'pay', branch: 'amount_ratio <= 0.5' },
    { from: 'review', to: 'out' },
    { from: 'pay', to: 'out' },
    { from: 'deny', to: 'out' }
  ]
};

const claimSchema = {
  type: 'object',
  properties: {
    amount: { type: 'number', example: 1900 },
    coverage_limit: { type: 'number', example: 3000 },
    policy_active: { type: 'number', example: 1 },
    prior_claims_24m: { type: 'number', example: 1 },
    days_since_policy_start: { type: 'number', example: 210 }
  }
};

// --- Marketplace payout risk -------------------------------------------------------------

function payoutCoreNodes() {
  return [
    { id: 'in', type: 'input' as const, name: 'Payout request', lane: 'Intake' },
    {
      id: 'ledger',
      type: 'connect' as const,
      name: 'Core-banking ledger',
      lane: 'Intake',
      config: { connector: 'core-banking', output: 'ledger' }
    },
    {
      id: 'feat',
      type: 'assignment' as const,
      name: 'Payout features',
      lane: 'Score',
      config: {
        assignments: [
          { target: 'payout_ratio', expr: 'amount / avg_payout_30d' },
          { target: 'new_account', expr: 'account_age_days < 30 ? 1 : 0' },
          { target: 'nsf_12m', expr: 'connect.ledger.nsf_12m' }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'Payout risk score',
      lane: 'Score',
      config: { model: 'payout_risk', output: 'prisk' }
    },
    {
      id: 'level',
      type: 'assignment' as const,
      name: 'Risk level',
      lane: 'Score',
      config: { assignments: [{ target: 'payout_score', expr: 'predict.prisk.score' }] }
    },
    { id: 'gate', type: 'split' as const, name: 'Release gate', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Payout ops review',
      lane: 'Decide',
      config: { case_type: 'payout_review', sla_days: 2 }
    },
    {
      id: 'hold',
      type: 'assignment' as const,
      name: 'Hold funds',
      lane: 'Decide',
      config: {
        assignments: [
          { target: 'released', expr: 'false' },
          { target: 'hold_reason', expr: '"risk_hold"' }
        ]
      }
    },
    {
      id: 'release',
      type: 'assignment' as const,
      name: 'Release payout',
      lane: 'Decide',
      config: { assignments: [{ target: 'released', expr: 'true' }] }
    },
    { id: 'out', type: 'output' as const, name: 'Payout decision', lane: 'Decide', config: {} }
  ];
}

const payoutGraphV1 = {
  nodes: payoutCoreNodes(),
  edges: [
    { from: 'in', to: 'ledger' },
    { from: 'ledger', to: 'feat' },
    { from: 'feat', to: 'score' },
    { from: 'score', to: 'level' },
    { from: 'level', to: 'gate' },
    { from: 'gate', to: 'hold', branch: 'payout_score >= 60' },
    { from: 'gate', to: 'review', branch: 'payout_score >= 30' },
    { from: 'gate', to: 'release', branch: 'payout_score < 30' },
    { from: 'hold', to: 'out' },
    { from: 'review', to: 'out' },
    { from: 'release', to: 'out' }
  ]
};

// v2 routes through a risk × amount matrix: medium-risk small payouts auto-release
// (v1 sent them all to review — the ops-load fix this version shipped).
const payoutGraphV2 = {
  nodes: [
    ...payoutCoreNodes(),
    {
      id: 'matrix',
      type: '2d_matrix' as const,
      name: 'Risk × amount action',
      lane: 'Decide',
      config: {
        output: 'action',
        rows: [
          { when: 'payout_score >= 60' },
          { when: 'payout_score >= 30' },
          { when: 'payout_score < 30' }
        ],
        cols: [{ when: 'amount >= 10000' }, { when: 'amount < 10000' }],
        cells: [
          ['hold', 'hold'],
          ['review', 'release'],
          ['release', 'release']
        ]
      }
    }
  ],
  edges: [
    { from: 'in', to: 'ledger' },
    { from: 'ledger', to: 'feat' },
    { from: 'feat', to: 'score' },
    { from: 'score', to: 'level' },
    { from: 'level', to: 'matrix' },
    { from: 'matrix', to: 'gate' },
    { from: 'gate', to: 'hold', branch: 'action == "hold"' },
    { from: 'gate', to: 'review', branch: 'action == "review"' },
    { from: 'gate', to: 'release', branch: 'action == "release"' },
    { from: 'hold', to: 'out' },
    { from: 'review', to: 'out' },
    { from: 'release', to: 'out' }
  ]
};

const payoutSchema = {
  type: 'object',
  properties: {
    amount: { type: 'number', example: 12500 },
    avg_payout_30d: { type: 'number', example: 5200 },
    payouts_24h: { type: 'number', example: 2 },
    account_age_days: { type: 'number', example: 210 },
    chargeback_rate: { type: 'number', example: 0.011 }
  }
};

// --- Card limit increase --------------------------------------------------------------

const limitGraphV1 = {
  nodes: [
    { id: 'in', type: 'input' as const, name: 'CLI request', lane: 'Intake' },
    {
      id: 'usage',
      type: 'assignment' as const,
      name: 'Usage features',
      lane: 'Score',
      config: {
        assignments: [
          { target: 'utilization', expr: 'revolving_balance / credit_limit' },
          { target: 'dti', expr: 'debt / income' },
          { target: 'delinquencies', expr: 'delinquencies_24m' }
        ]
      }
    },
    {
      id: 'score',
      type: 'predict' as const,
      name: 'PD model',
      lane: 'Score',
      config: { model: 'credit_pd', output: 'pd' }
    },
    {
      id: 'derive',
      type: 'assignment' as const,
      name: 'Risk + proposed limit',
      lane: 'Score',
      config: {
        assignments: [
          { target: 'risk', expr: 'predict.pd.probability * 100' },
          {
            target: 'proposed_limit',
            expr: 'risk < 20 ? credit_limit * 1.5 : credit_limit * 1.25'
          }
        ]
      }
    },
    { id: 'gate', type: 'split' as const, name: 'CLI gate', lane: 'Decide', config: {} },
    {
      id: 'review',
      type: 'manual_review' as const,
      name: 'Credit ops review',
      lane: 'Decide',
      config: { case_type: 'limit_review', sla_days: 2 }
    },
    {
      id: 'grant',
      type: 'assignment' as const,
      name: 'Grant increase',
      lane: 'Decide',
      config: { assignments: [{ target: 'granted', expr: 'true' }] }
    },
    {
      id: 'refuse',
      type: 'assignment' as const,
      name: 'Keep current limit',
      lane: 'Decide',
      config: { assignments: [{ target: 'granted', expr: 'false' }] }
    },
    { id: 'out', type: 'output' as const, name: 'CLI decision', lane: 'Decide', config: {} }
  ],
  edges: [
    { from: 'in', to: 'usage' },
    { from: 'usage', to: 'score' },
    { from: 'score', to: 'derive' },
    { from: 'derive', to: 'gate' },
    { from: 'gate', to: 'grant', branch: 'risk < 20 && utilization < 0.6' },
    { from: 'gate', to: 'review', branch: 'risk < 45' },
    { from: 'gate', to: 'refuse', branch: 'risk >= 45' },
    { from: 'grant', to: 'out' },
    { from: 'review', to: 'out' },
    { from: 'refuse', to: 'out' }
  ]
};

const limitSchema = {
  type: 'object',
  properties: {
    income: { type: 'number', example: 74000 },
    debt: { type: 'number', example: 24000 },
    revolving_balance: { type: 'number', example: 7900 },
    credit_limit: { type: 'number', example: 12000 },
    delinquencies_24m: { type: 'number', example: 0 },
    fico_score: { type: 'number', example: 690 }
  }
};

// --- The fleet -------------------------------------------------------------------------

export function seedFlows(): Flow[] {
  return [
    {
      flow_id: 'flow_credit',
      slug: 'credit-decision',
      name: 'Consumer Credit Decision',
      latest: 3,
      versions: [
        {
          version: 1,
          etag: 'etag-c1',
          graph: creditGraphV1,
          published_at: ago(540),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-c2',
          graph: creditGraphV2,
          input_schema: creditSchema,
          published_at: ago(180),
          published_by: PRIYA
        },
        {
          version: 3,
          etag: 'etag-c3',
          graph: creditGraphV3,
          input_schema: creditSchema,
          published_at: ago(36),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2 },
        staging: { version: 3, previous_version: 2 },
        sandbox: { version: 3, challenger_version: 2, challenger_pct: 20 }
      },
      deployment_requests: [
        {
          request_id: 'req_c0',
          environment: 'production',
          version: 2,
          status: 'approved',
          reason: 'Backtest parity confirmed; dual-model rollout',
          requested_by: PRIYA,
          requested_at: ago(200),
          decided_by: MARCUS,
          decided_at: ago(196)
        },
        {
          request_id: 'req_c1',
          environment: 'production',
          version: 3,
          status: 'pending',
          reason: 'Roll out live bureau pull + Reg B adverse-action codes',
          requested_by: PRIYA,
          requested_at: ago(12)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_aml',
      slug: 'aml-screening',
      name: 'AML Transaction Screening',
      latest: 3,
      versions: [
        {
          version: 1,
          etag: 'etag-a1',
          graph: amlGraphV1,
          published_at: ago(480),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-a2',
          graph: amlGraphV2,
          input_schema: amlSchema,
          published_at: ago(96),
          published_by: PRIYA
        },
        {
          version: 3,
          etag: 'etag-a3',
          graph: amlGraphV3,
          input_schema: amlSchema,
          published_at: ago(20),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2 },
        staging: { version: 3, challenger_version: 2, challenger_pct: 30 },
        sandbox: { version: 3 }
      },
      deployment_requests: [
        {
          request_id: 'req_a1',
          environment: 'production',
          version: 3,
          status: 'pending',
          reason: 'Add structuring heuristics + SAR narrative to prod',
          requested_by: DIEGO,
          requested_at: ago(8)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_kyc',
      slug: 'kyc-onboarding',
      name: 'KYC Onboarding',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-k1',
          graph: kycGraphV1,
          published_at: ago(220),
          published_by: PRIYA
        },
        {
          version: 2,
          etag: 'etag-k2',
          graph: kycGraphV2,
          input_schema: kycSchema,
          published_at: ago(60),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2, previous_version: 1 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_fraud',
      slug: 'card-fraud',
      name: 'Card Fraud Scoring',
      latest: 4,
      versions: [
        {
          version: 1,
          etag: 'etag-f1',
          graph: fraudGraphV1,
          published_at: ago(400),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-f2',
          graph: fraudGraphV2,
          published_at: ago(200),
          published_by: PRIYA
        },
        {
          version: 3,
          etag: 'etag-f3',
          graph: fraudGraphV3,
          input_schema: fraudSchema,
          published_at: ago(72),
          published_by: PRIYA
        },
        {
          version: 4,
          etag: 'etag-f4',
          graph: fraudGraphV4,
          input_schema: fraudSchema,
          published_at: ago(10),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 3, challenger_version: 4, challenger_pct: 15, previous_version: 2 },
        staging: { version: 4 },
        sandbox: { version: 4 }
      },
      deployment_requests: [
        {
          request_id: 'req_f0',
          environment: 'production',
          version: 3,
          status: 'approved',
          reason: 'Tighten the review band to 35 after the Q2 loss review',
          requested_by: PRIYA,
          requested_at: ago(80),
          decided_by: MARCUS,
          decided_at: ago(74)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_dispute',
      slug: 'dispute-triage',
      name: 'Dispute / Chargeback Triage',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-d1',
          graph: disputeGraphV1,
          published_at: ago(150),
          published_by: DIEGO
        },
        {
          version: 2,
          etag: 'etag-d2',
          graph: disputeGraphV2,
          input_schema: disputeSchema,
          published_at: ago(40),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 1 },
        staging: { version: 2, previous_version: 1 },
        sandbox: { version: 2 }
      },
      deployment_requests: [
        {
          request_id: 'req_d1',
          environment: 'production',
          version: 2,
          status: 'pending',
          reason: 'Promote the reason-code liability table',
          requested_by: DIEGO,
          requested_at: ago(30)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_merchant',
      slug: 'merchant-onboarding',
      name: 'Merchant Onboarding',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-m1',
          graph: merchantGraphV1,
          published_at: ago(110),
          published_by: PRIYA
        },
        {
          version: 2,
          etag: 'etag-m2',
          graph: merchantGraphV2,
          input_schema: merchantSchema,
          published_at: ago(28),
          published_by: PRIYA
        }
      ],
      deployments: {
        staging: { version: 2, previous_version: 1 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_collections',
      slug: 'collections-hardship',
      name: 'Collections Hardship Program',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-h1',
          graph: collectionsGraphV1,
          published_at: ago(130),
          published_by: DIEGO
        },
        {
          version: 2,
          etag: 'etag-h2',
          graph: collectionsGraphV2,
          input_schema: collectionsSchema,
          published_at: ago(46),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2, previous_version: 1 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_claim',
      slug: 'claim-triage',
      name: 'Purchase Protection Claim Triage',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-cl1',
          graph: claimGraphV1,
          published_at: ago(240),
          published_by: PRIYA
        },
        {
          version: 2,
          etag: 'etag-cl2',
          graph: claimGraphV2,
          input_schema: claimSchema,
          published_at: ago(120),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2, previous_version: 1 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      deployment_requests: [
        {
          request_id: 'req_cl0',
          environment: 'production',
          version: 2,
          status: 'rejected',
          reason: 'Staging backtest shows +9% referral rate — tune the fraud band first',
          requested_by: PRIYA,
          requested_at: ago(160),
          decided_by: MARCUS,
          decided_at: ago(156)
        },
        {
          request_id: 'req_cl1',
          environment: 'production',
          version: 2,
          status: 'approved',
          reason: 'Fraud band re-tuned to 60; referral delta now +2%',
          requested_by: PRIYA,
          requested_at: ago(126),
          decided_by: MARCUS,
          decided_at: ago(121)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_payout',
      slug: 'payout-risk',
      name: 'Marketplace Payout Risk',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-p1',
          graph: payoutGraphV1,
          published_at: ago(170),
          published_by: DIEGO
        },
        {
          version: 2,
          etag: 'etag-p2',
          graph: payoutGraphV2,
          input_schema: payoutSchema,
          published_at: ago(55),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 2, previous_version: 1 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_limit',
      slug: 'limit-increase',
      name: 'Card Limit Increase',
      latest: 1,
      versions: [
        {
          version: 1,
          etag: 'etag-l1',
          graph: limitGraphV1,
          input_schema: limitSchema,
          published_at: ago(70),
          published_by: PRIYA
        }
      ],
      deployments: {
        staging: { version: 1 },
        sandbox: { version: 1 }
      },
      promotion_policy: STRICT_PROMOTION
    }
  ];
}
