// SPDX-License-Identifier: AGPL-3.0-or-later
// Starter flows offered by the builder's "New from template" gallery. Each is a real
// flow-as-code document imported through POST /v1/flows/import (the same path as the
// in-product Import). They are authored to showcase the differentiating node types
// (scorecard, decision_table, 2d_matrix, rule, code, reason, connect, ai, predict) and
// to resolve against the demo's sample connector data, so a test run lands on a real
// band. nodeTypes drives the chips shown on each gallery card.

export interface FlowTemplate {
  id: string;
  name: string;
  purpose: string;
  nodeTypes: string[];
  doc: {
    slug: string;
    name: string;
    graph: { nodes: TemplateNode[]; edges: TemplateEdge[] };
    input_schema?: TemplateInputSchema;
  };
}
// The caller-input contract of each template: the fields its expressions read that no
// upstream node produces. It powers the builder's "Sample input" (examples land the run
// on a real branch) and the decide-time type/required validation on both backends.
interface TemplateInputSchema {
  type: 'object';
  required: string[];
  properties: Record<string, { type: string; example?: unknown }>;
}
interface TemplateNode {
  id: string;
  type: string;
  name?: string;
  config?: Record<string, unknown>;
}
interface TemplateEdge {
  from: string;
  to: string;
  branch?: string;
}

export const TEMPLATES: FlowTemplate[] = [
  {
    id: 'credit-stp',
    name: 'Consumer Credit STP',
    purpose:
      'Straight-through approve/refer/decline with a banded scorecard, a PD model, and adverse-action reasons.',
    nodeTypes: ['connect', 'scorecard', 'decision_table', 'reason'],
    doc: {
      slug: 'credit-stp',
      name: 'Consumer Credit STP',
      input_schema: {
        type: 'object',
        required: ['income', 'debt', 'revolving_balance', 'credit_limit'],
        properties: {
          income: { type: 'number', example: 52000 },
          debt: { type: 'number', example: 14000 },
          revolving_balance: { type: 'number', example: 4200 },
          credit_limit: { type: 'number', example: 12000 }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Application' },
          {
            id: 'bureau',
            type: 'connect',
            name: 'Bureau pull',
            config: { connector: 'experian', output: 'bureau' }
          },
          {
            id: 'derive',
            type: 'assignment',
            name: 'Derive ratios',
            config: {
              assignments: [
                { target: 'dti', expr: 'debt / income' },
                { target: 'util', expr: 'revolving_balance / credit_limit' }
              ]
            }
          },
          {
            id: 'pd',
            type: 'predict',
            name: 'PD model',
            config: { model: 'credit_pd', output: 'pd' }
          },
          {
            id: 'score',
            type: 'scorecard',
            name: 'Affordability score',
            config: {
              output: 'score',
              factors: [
                { when: 'dti < 0.35', weight: 30 },
                { when: 'util < 0.3', weight: 25 },
                { when: 'bureau.fico_score >= 720', weight: 25 },
                { when: 'bureau.delinquencies_24m == 0', weight: 20 }
              ]
            }
          },
          {
            id: 'adverse',
            type: 'reason',
            name: 'Adverse-action codes',
            config: {
              reasons: [
                { when: 'dti >= 0.43', code: 'AA-DTI', description: 'Debt-to-income too high' },
                {
                  when: 'bureau.fico_score < 640',
                  code: 'AA-FICO',
                  description: 'Insufficient credit score'
                }
              ]
            }
          },
          {
            id: 'limit',
            type: 'decision_table',
            name: 'Offered limit',
            config: {
              hit: 'first',
              rows: [
                {
                  when: 'score >= 80',
                  outputs: [{ target: 'offered_limit', expr: 'income * 0.2' }]
                },
                {
                  when: 'score >= 50',
                  outputs: [{ target: 'offered_limit', expr: 'income * 0.1' }]
                },
                { when: 'true', outputs: [{ target: 'offered_limit', expr: '0' }] }
              ]
            }
          },
          { id: 'band', type: 'split', name: 'Decision band' },
          {
            id: 'review',
            type: 'manual_review',
            name: 'Underwriter',
            config: { company_name: "'Applicant'", case_type: "'credit_review'", sla_days: 3 }
          },
          {
            id: 'out',
            type: 'output',
            name: 'Decision',
            config: { fields: ['score', 'offered_limit', 'reason_codes'] }
          }
        ],
        edges: [
          { from: 'in', to: 'bureau' },
          { from: 'bureau', to: 'derive' },
          { from: 'derive', to: 'pd' },
          { from: 'pd', to: 'score' },
          { from: 'score', to: 'adverse' },
          { from: 'adverse', to: 'limit' },
          { from: 'limit', to: 'band' },
          { from: 'band', to: 'out', branch: 'score >= 70' },
          { from: 'band', to: 'review', branch: 'score >= 40' },
          { from: 'band', to: 'out', branch: 'true' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  },
  {
    id: 'fraud-screen',
    name: 'Card-Not-Present Fraud Screen',
    purpose:
      'Real-time auth: allow / review / block from velocity + device intelligence via a 2D risk matrix.',
    nodeTypes: ['connect', '2d_matrix', 'predict'],
    doc: {
      slug: 'fraud-screen',
      name: 'Card-Not-Present Fraud Screen',
      input_schema: {
        type: 'object',
        required: ['amount'],
        properties: {
          amount: { type: 'number', example: 250 }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Authorization' },
          {
            id: 'device',
            type: 'connect',
            name: 'Device intel',
            config: { connector: 'device_intel', output: 'device' }
          },
          {
            id: 'fraud',
            type: 'predict',
            name: 'Fraud model',
            config: { model: 'fraud_score', output: 'fraud' }
          },
          {
            id: 'grid',
            type: '2d_matrix',
            name: 'Risk grid',
            config: {
              output: 'action',
              rows: [
                { when: 'fraud.probability >= 0.8' },
                { when: 'fraud.probability >= 0.4' },
                { when: 'true' }
              ],
              cols: [{ when: 'amount > 1000' }, { when: 'true' }],
              cells: [
                ['block', 'block'],
                ['review', 'allow'],
                ['allow', 'allow']
              ]
            }
          },
          { id: 'route', type: 'split', name: 'Route on action' },
          {
            id: 'review',
            type: 'manual_review',
            name: 'Fraud analyst',
            config: { company_name: "'Cardholder'", case_type: "'fraud_review'", sla_days: 1 }
          },
          { id: 'out', type: 'output', name: 'Action', config: { fields: ['action'] } }
        ],
        edges: [
          { from: 'in', to: 'device' },
          { from: 'device', to: 'fraud' },
          { from: 'fraud', to: 'grid' },
          { from: 'grid', to: 'route' },
          { from: 'route', to: 'out', branch: "action == 'allow'" },
          { from: 'route', to: 'review', branch: "action == 'review'" },
          { from: 'route', to: 'out', branch: 'true' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  },
  {
    id: 'sanctions-screen',
    name: 'Sanctions & PEP Screening',
    purpose:
      'Watchlist/PEP screening on a wire with a DMN collect-and-sum risk table and a SAR narrative.',
    nodeTypes: ['connect', 'decision_table', 'reason', 'ai'],
    doc: {
      slug: 'sanctions-screen',
      name: 'Sanctions & PEP Screening',
      input_schema: {
        type: 'object',
        required: ['origin_country', 'dest_country', 'amount'],
        properties: {
          origin_country: { type: 'string', example: 'US' },
          dest_country: { type: 'string', example: 'DE' },
          amount: { type: 'number', example: 12500 }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Wire' },
          {
            id: 'wl',
            type: 'connect',
            name: 'Watchlist',
            config: { connector: 'ofac_watchlist', output: 'sanctions' }
          },
          {
            id: 'feat',
            type: 'assignment',
            name: 'Features',
            config: {
              assignments: [
                { target: 'cross_border', expr: 'origin_country != dest_country ? 1 : 0' },
                { target: 'high_value', expr: 'amount > 10000 ? 1 : 0' }
              ]
            }
          },
          {
            id: 'risk',
            type: 'decision_table',
            name: 'Risk factors',
            config: {
              hit: 'collect',
              aggregate: 'sum',
              rows: [
                { when: 'sanctions.hit', outputs: [{ target: 'risk', expr: '100' }] },
                { when: 'cross_border == 1', outputs: [{ target: 'risk', expr: '20' }] },
                { when: 'high_value == 1', outputs: [{ target: 'risk', expr: '15' }] }
              ]
            }
          },
          {
            id: 'sar',
            type: 'ai',
            name: 'SAR narrative',
            config: {
              agent: 'sar-drafter',
              prompt: 'Draft a SAR narrative from the risk drivers',
              output: 'narrative'
            }
          },
          {
            id: 'codes',
            type: 'reason',
            name: 'Codes',
            config: {
              reasons: [
                { when: 'sanctions.hit', code: 'SANCT-HIT', description: 'Sanctions list match' }
              ]
            }
          },
          { id: 'route', type: 'split', name: 'Route on risk' },
          {
            id: 'review',
            type: 'manual_review',
            name: 'AML analyst',
            config: { company_name: "'Counterparty'", case_type: "'aml_alert'", sla_days: 5 }
          },
          {
            id: 'out',
            type: 'output',
            name: 'Decision',
            config: { fields: ['risk', 'narrative', 'reason_codes'] }
          }
        ],
        edges: [
          { from: 'in', to: 'wl' },
          { from: 'wl', to: 'feat' },
          { from: 'feat', to: 'risk' },
          { from: 'risk', to: 'sar' },
          { from: 'sar', to: 'codes' },
          { from: 'codes', to: 'route' },
          { from: 'route', to: 'review', branch: 'risk >= 100' },
          { from: 'route', to: 'review', branch: 'risk >= 20' },
          { from: 'route', to: 'out', branch: 'true' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  },
  {
    id: 'kyb-onboarding',
    name: 'Business (KYB) Onboarding',
    purpose:
      'KYB with document extraction, a beneficial-owner aggregation (code node), and a KYB scorecard.',
    nodeTypes: ['ai', 'code', 'scorecard', 'connect'],
    doc: {
      slug: 'kyb-onboarding',
      name: 'Business (KYB) Onboarding',
      // Both fields are read via data.get(..., default) in the UBO code node, so the
      // flow completes without them — nothing is required.
      input_schema: {
        type: 'object',
        required: [],
        properties: {
          beneficial_owners: {
            type: 'array',
            example: [{ name: 'Ana Ionescu', ownership_pct: 60 }]
          },
          max_ubo_risk: { type: 'number', example: 35 }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Business application' },
          {
            id: 'extract',
            type: 'ai',
            name: 'Document extract',
            config: {
              agent: 'doc-extractor',
              prompt: 'Extract registration, UBOs and MCC from the documents',
              output: 'extracted'
            }
          },
          {
            id: 'registry',
            type: 'connect',
            name: 'Registry',
            config: { connector: 'bank_core', output: 'registry' }
          },
          {
            id: 'ubo',
            type: 'code',
            name: 'UBO aggregation',
            config: {
              code: "ubo_count = len(data.get('beneficial_owners', []))\nhigh_risk_ubo = 1 if data.get('max_ubo_risk', 0) >= 70 else 0"
            }
          },
          {
            id: 'score',
            type: 'scorecard',
            name: 'KYB score',
            config: {
              output: 'kyb',
              factors: [
                { when: 'high_risk_ubo == 0', weight: 40 },
                { when: 'registry.tenure_months >= 24', weight: 30 },
                { when: 'ubo_count <= 3', weight: 30 }
              ]
            }
          },
          { id: 'band', type: 'split', name: 'KYB band' },
          {
            id: 'review',
            type: 'manual_review',
            name: 'KYB analyst',
            config: { company_name: "'Business'", case_type: "'kyb_review'", sla_days: 4 }
          },
          { id: 'out', type: 'output', name: 'Decision', config: { fields: ['kyb'] } }
        ],
        edges: [
          { from: 'in', to: 'extract' },
          { from: 'extract', to: 'registry' },
          { from: 'registry', to: 'ubo' },
          { from: 'ubo', to: 'score' },
          { from: 'score', to: 'band' },
          { from: 'band', to: 'out', branch: 'kyb >= 70' },
          { from: 'band', to: 'review', branch: 'true' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  },
  {
    id: 'bnpl-affordability',
    name: 'BNPL Affordability',
    purpose:
      'Instant point-of-sale limit from open-banking cashflow, with a rule gate and a tiered limit table.',
    nodeTypes: ['connect', 'rule', 'decision_table'],
    doc: {
      slug: 'bnpl-affordability',
      name: 'BNPL Affordability',
      input_schema: {
        type: 'object',
        required: ['amount'],
        properties: {
          amount: { type: 'number', example: 300 }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Checkout' },
          {
            id: 'bank',
            type: 'connect',
            name: 'Open banking',
            config: { connector: 'bank_core', output: 'bank' }
          },
          {
            id: 'gate',
            type: 'rule',
            name: 'Affordability gate',
            config: {
              rules: [
                { when: 'bank.nsf_12m > 0', then: [{ target: 'affordable', expr: 'false' }] },
                {
                  when: 'true',
                  then: [{ target: 'affordable', expr: 'bank.balance_usd > amount * 2' }]
                }
              ]
            }
          },
          {
            id: 'tier',
            type: 'decision_table',
            name: 'Limit tier',
            config: {
              hit: 'first',
              rows: [
                { when: 'bank.balance_usd >= 5000', outputs: [{ target: 'limit', expr: '2000' }] },
                { when: 'bank.balance_usd >= 1000', outputs: [{ target: 'limit', expr: '500' }] },
                { when: 'true', outputs: [{ target: 'limit', expr: '0' }] }
              ]
            }
          },
          { id: 'route', type: 'split', name: 'Route' },
          {
            id: 'out',
            type: 'output',
            name: 'Decision',
            config: { fields: ['affordable', 'limit'] }
          }
        ],
        edges: [
          { from: 'in', to: 'bank' },
          { from: 'bank', to: 'gate' },
          { from: 'gate', to: 'tier' },
          { from: 'tier', to: 'route' },
          { from: 'route', to: 'out', branch: 'affordable && limit > 0' },
          { from: 'route', to: 'out', branch: 'true' }
        ]
      }
    }
  },
  {
    id: 'chargeback-triage',
    name: 'Chargeback Triage',
    purpose:
      'Representment-vs-refund routing on a 2D liability × value matrix, with an AI summary.',
    nodeTypes: ['2d_matrix', 'ai', 'manual_review'],
    doc: {
      slug: 'chargeback-triage',
      name: 'Chargeback Triage',
      input_schema: {
        type: 'object',
        required: ['amount', 'reason_code'],
        properties: {
          amount: { type: 'number', example: 725 },
          reason_code: { type: 'string', example: 'fraud' }
        }
      },
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Dispute' },
          {
            id: 'feat',
            type: 'assignment',
            name: 'Features',
            config: {
              assignments: [
                { target: 'high_value', expr: 'amount > 500 ? 1 : 0' },
                { target: 'fraud_liability', expr: "reason_code == 'fraud' ? 1 : 0" }
              ]
            }
          },
          {
            id: 'summary',
            type: 'ai',
            name: 'Summary',
            config: {
              agent: 'dispute-summarizer',
              prompt: 'Summarize the dispute and recommend representment vs refund',
              output: 'summary'
            }
          },
          {
            id: 'triage',
            type: '2d_matrix',
            name: 'Triage',
            config: {
              output: 'route',
              rows: [{ when: 'fraud_liability == 1' }, { when: 'true' }],
              cols: [{ when: 'high_value == 1' }, { when: 'true' }],
              cells: [
                ['review', 'review'],
                ['review', 'auto_refund']
              ]
            }
          },
          { id: 'split', type: 'split', name: 'Route' },
          {
            id: 'review',
            type: 'manual_review',
            name: 'Disputes ops',
            config: { company_name: "'Merchant'", case_type: "'dispute'", sla_days: 7 }
          },
          { id: 'out', type: 'output', name: 'Decision', config: { fields: ['route', 'summary'] } }
        ],
        edges: [
          { from: 'in', to: 'feat' },
          { from: 'feat', to: 'summary' },
          { from: 'summary', to: 'triage' },
          { from: 'triage', to: 'split' },
          { from: 'split', to: 'out', branch: "route == 'auto_refund'" },
          { from: 'split', to: 'review', branch: 'true' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  }
];
