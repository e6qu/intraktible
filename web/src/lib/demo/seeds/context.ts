// SPDX-License-Identifier: AGPL-3.0-or-later
// Context-layer seed: the model registry (real, evaluatable specs — the walker
// scores decisions with these exact coefficients), connectors (configs redacted
// where credential-ish, like a real workspace), the connector catalog, feature
// definitions and the entity store with event timelines that actually fall inside
// the features' windows so computed features come out non-zero.

import type { Model, Connector, ConnectorTemplate, Feature, Entity, EntityEvent } from '$lib/api';
import { ago, AVA, MARCUS, PRIYA } from './base';

export function seedModels(): Model[] {
  return [
    {
      name: 'credit_pd',
      kind: 'logistic',
      spec: {
        kind: 'logistic',
        // FICO carries the intercept: 700 is the reference score, so the +5.3 base
        // is ~5.3 - 0.012*fico. A negative fico weight (higher score -> lower PD)
        // plus DTI/utilization/delinquency drivers; tuned so a mid-tier applicant
        // (DTI ~0.45, utilization ~0.55, 1 delinquency, mid-600s FICO) lands ~0.5
        // probability and routes to manual review rather than auto-approving/declining.
        intercept: 5.3,
        coefficients: { dti: 3.0, utilization: 1.2, delinquencies: 0.7, fico_score: -0.012 }
      },
      owner: AVA,
      updated_at: ago(96)
    },
    {
      name: 'fraud_score',
      kind: 'gbm',
      spec: {
        kind: 'gbm',
        base: 0.1,
        link: 'logit',
        // Three shallow trees: velocity×device carries most of the signal, ticket
        // inflation and absolute amount shade it. Small enough to read in the UI,
        // real enough that different inputs land in different bands.
        trees: [
          {
            feature: 'velocity',
            threshold: 5,
            left: { leaf: true, value: -0.6 },
            right: {
              feature: 'device_risk',
              threshold: 60,
              left: { leaf: true, value: 0.8 },
              right: { leaf: true, value: 1.9 }
            }
          },
          {
            feature: 'amount_ratio',
            threshold: 3,
            left: { leaf: true, value: -0.2 },
            right: { leaf: true, value: 1.1 }
          },
          {
            feature: 'amount',
            threshold: 900,
            left: { leaf: true, value: -0.15 },
            right: { leaf: true, value: 0.5 }
          }
        ]
      },
      owner: MARCUS,
      updated_at: ago(50)
    },
    {
      name: 'aml_risk',
      kind: 'expression',
      spec: { kind: 'expression', expr: 'amount / 10000 + cross_border * 2 + high_value' },
      owner: MARCUS,
      updated_at: ago(120)
    },
    {
      name: 'kyc_score',
      kind: 'external',
      spec: { kind: 'external', endpoint: 'kyc-vendor:/score', timeout_ms: 800 },
      owner: PRIYA,
      updated_at: ago(70)
    },
    {
      name: 'repayment_propensity',
      kind: 'logistic',
      spec: {
        kind: 'logistic',
        // Logistic classifier of on-time repayment propensity from employment
        // signals; an auxiliary score the credit flow enriches alongside PD.
        // No baseline captured (MRM monitoring gap, intact).
        intercept: -1.1,
        coefficients: { tenure_years: 0.4, employment_stability: 0.9 }
      },
      owner: AVA,
      updated_at: ago(310)
    },
    {
      name: 'claim_fraud',
      kind: 'gbm',
      spec: {
        kind: 'gbm',
        base: -0.4,
        link: 'logit',
        // Claim-abuse GBM: repeat claimants on young policies claiming near the
        // coverage limit score high; a seasoned single-claim customer scores low.
        trees: [
          {
            feature: 'prior_claims_24m',
            threshold: 2,
            left: { leaf: true, value: -0.5 },
            right: { leaf: true, value: 1.2 }
          },
          {
            feature: 'days_since_policy_start',
            threshold: 60,
            left: { leaf: true, value: 1.0 },
            right: { leaf: true, value: -0.3 }
          },
          {
            feature: 'amount_ratio',
            threshold: 0.8,
            left: { leaf: true, value: -0.2 },
            right: { leaf: true, value: 0.7 }
          }
        ]
      },
      owner: PRIYA,
      updated_at: ago(130)
    },
    {
      name: 'payout_risk',
      kind: 'expression',
      spec: {
        kind: 'expression',
        expr: 'payouts_24h * 6 + payout_ratio * 8 + new_account * 25 + chargeback_rate * 200'
      },
      owner: MARCUS,
      updated_at: ago(58)
    }
  ];
}

export function seedConnectors(): Connector[] {
  return [
    {
      name: 'experian',
      type: 'http',
      config: {
        base_url: 'https://api.experian.demo',
        client_id: 'ikt-prod-2291',
        client_secret: '••••••••••••'
      },
      updated_at: ago(80)
    },
    {
      name: 'core-banking',
      type: 'postgres',
      config: { dsn: 'postgres://svc_decisioning:••••••••@core-banking:5432/ledger' },
      updated_at: ago(160)
    },
    {
      name: 'ofac-sanctions',
      type: 'http',
      config: { base_url: 'https://api.sanctions.demo', api_key: '••••••••••••' },
      updated_at: ago(50)
    },
    {
      name: 'device-intel',
      type: 'http',
      config: { base_url: 'https://api.deviceintel.demo', api_key: '••••••••••••' },
      updated_at: ago(36)
    },
    {
      name: 'jumio-kyc',
      type: 'http',
      config: { base_url: 'https://api.jumio.demo', api_token: '••••••••••••' },
      updated_at: ago(72)
    },
    {
      name: 'plaid-transactions',
      type: 'http',
      config: {
        base_url: 'https://api.plaid.demo',
        client_id: 'plaid-client-6614',
        secret: '••••••••••••'
      },
      updated_at: ago(30)
    },
    {
      name: 'lexisnexis-kyb',
      type: 'http',
      config: { base_url: 'https://api.lexisnexis.demo', api_key: '••••••••••••' },
      updated_at: ago(44)
    }
  ];
}

export function seedCatalog(): ConnectorTemplate[] {
  return [
    {
      id: 'experian',
      name: 'Experian Bureau',
      category: 'Credit Bureau',
      type: 'http',
      description: 'Pull a consumer credit report and FICO score.',
      config: { base_url: 'https://api.experian.com' }
    },
    {
      id: 'sanctions',
      name: 'OFAC Sanctions',
      category: 'Compliance',
      type: 'http',
      description: 'Screen an entity against sanctions and watchlists.',
      config: { base_url: 'https://api.sanctions.demo' }
    },
    {
      id: 'device-intel',
      name: 'Device Intelligence',
      category: 'Fraud',
      type: 'http',
      description: 'Device fingerprint and risk score for an authorization.',
      config: { base_url: 'https://api.deviceintel.demo' }
    },
    {
      id: 'jumio-kyc',
      name: 'Jumio Identity',
      category: 'Identity',
      type: 'http',
      description: 'Document verification and liveness for KYC onboarding.',
      config: { base_url: 'https://api.jumio.demo' }
    },
    {
      id: 'core-banking',
      name: 'Core Banking (Postgres)',
      category: 'Data',
      type: 'postgres',
      description: 'Read account balances and transaction history.',
      config: { dsn: 'postgres://core-banking' }
    },
    {
      id: 'plaid',
      name: 'Plaid Transactions',
      category: 'Banking Data',
      type: 'http',
      description: 'Linked-account balances and transaction streams for affordability.',
      config: { base_url: 'https://api.plaid.com' }
    },
    {
      id: 'lexisnexis-kyb',
      name: 'LexisNexis KYB',
      category: 'Compliance',
      type: 'http',
      description: 'Business registry, beneficial owners and adverse media for merchants.',
      config: { base_url: 'https://api.lexisnexis.com' }
    },
    {
      id: 'snowflake',
      name: 'Snowflake Warehouse',
      category: 'Data',
      type: 'postgres',
      description: 'Batch feature reads from the analytics warehouse.',
      config: { dsn: 'snowflake://analytics' }
    }
  ];
}

export function seedFeatures(): Feature[] {
  return [
    {
      name: 'tx_count_30d',
      entity_type: 'applicant',
      event_name: 'transaction',
      aggregation: 'count',
      window_hours: 720,
      updated_at: ago(60)
    },
    {
      name: 'tx_sum_7d',
      entity_type: 'applicant',
      event_name: 'transaction',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 168,
      updated_at: ago(60)
    },
    {
      name: 'tx_count_1h',
      entity_type: 'customer',
      event_name: 'authorization',
      aggregation: 'count',
      window_hours: 1,
      updated_at: ago(36)
    },
    {
      name: 'auth_sum_24h',
      entity_type: 'customer',
      event_name: 'authorization',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 24,
      updated_at: ago(36)
    },
    {
      name: 'wire_count_7d',
      entity_type: 'transaction',
      event_name: 'wire',
      aggregation: 'count',
      window_hours: 168,
      updated_at: ago(48)
    },
    {
      name: 'wire_sum_30d',
      entity_type: 'transaction',
      event_name: 'wire',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 720,
      updated_at: ago(48)
    },
    {
      name: 'chargeback_count_90d',
      entity_type: 'merchant',
      event_name: 'chargeback',
      aggregation: 'count',
      window_hours: 2160,
      updated_at: ago(90)
    },
    {
      name: 'settlement_sum_30d',
      entity_type: 'merchant',
      event_name: 'settlement',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 720,
      updated_at: ago(90)
    },
    {
      name: 'payout_sum_7d',
      entity_type: 'merchant',
      event_name: 'payout',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 168,
      updated_at: ago(40)
    },
    {
      name: 'login_count_24h',
      entity_type: 'customer',
      event_name: 'login',
      aggregation: 'count',
      window_hours: 24,
      updated_at: ago(20)
    },
    {
      name: 'dispute_count_180d',
      entity_type: 'customer',
      event_name: 'dispute',
      aggregation: 'count',
      window_hours: 4320,
      updated_at: ago(70)
    },
    {
      name: 'claim_count_180d',
      entity_type: 'customer',
      event_name: 'claim',
      aggregation: 'count',
      window_hours: 4320,
      updated_at: ago(52)
    }
  ];
}

// One raw seed row per entity: attributes plus its event timeline (hours-ago +
// payload). event_count and first_seen/updated_at are DERIVED from the timeline in
// seedEntities/seedEntityEvents so the list and the detail can never disagree.
interface EntitySeed {
  type: string;
  id: string;
  attributes: Record<string, unknown>;
  events: { name: string; hrs: number; data?: Record<string, unknown> }[];
}

const ENTITIES: EntitySeed[] = [
  {
    type: 'applicant',
    id: 'APP-1001',
    attributes: { name: 'Jane Doe', segment: 'gold', country: 'US' },
    events: [
      { name: 'transaction', hrs: 300, data: { amount: 1200 } },
      { name: 'transaction', hrs: 100, data: { amount: 4500 } },
      { name: 'login', hrs: 12 }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1002',
    attributes: { name: 'John Roe', segment: 'standard', country: 'GB' },
    events: [
      { name: 'transaction', hrs: 200, data: { amount: 50000 } },
      { name: 'login', hrs: 48 }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1007',
    attributes: { name: 'Mei Lin', segment: 'platinum', country: 'SG' },
    events: [
      { name: 'transaction', hrs: 330, data: { amount: 9800 } },
      { name: 'transaction', hrs: 150, data: { amount: 15000 } },
      { name: 'transaction', hrs: 90, data: { amount: 6200 } },
      { name: 'login', hrs: 48 }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1011',
    attributes: { name: 'Carlos Reyes', segment: 'standard', country: 'MX' },
    events: [{ name: 'transaction', hrs: 160, data: { amount: 2300 } }]
  },
  {
    type: 'applicant',
    id: 'APP-1019',
    attributes: { name: 'Fatima Al-Sayed', segment: 'standard', country: 'AE' },
    events: [
      { name: 'transaction', hrs: 260, data: { amount: 7200 } },
      { name: 'transaction', hrs: 40, data: { amount: 1900 } },
      { name: 'login', hrs: 40 }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1023',
    attributes: { name: 'Tomasz Nowak', segment: 'gold', country: 'PL' },
    events: [
      { name: 'transaction', hrs: 380, data: { amount: 3400 } },
      { name: 'transaction', hrs: 120, data: { amount: 5100 } },
      { name: 'transaction', hrs: 60, data: { amount: 780 } },
      { name: 'login', hrs: 9 }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7781',
    attributes: { name: 'Ada Stark', tenure_years: 6, card_present: true },
    events: [
      { name: 'authorization', hrs: 500, data: { amount: 120 } },
      { name: 'authorization', hrs: 200, data: { amount: 95 } },
      { name: 'authorization', hrs: 8, data: { amount: 1290 } },
      { name: 'login', hrs: 8 }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7790',
    attributes: { name: 'Bruce Pied', tenure_years: 1, card_present: false },
    events: [
      { name: 'authorization', hrs: 118, data: { amount: 60 } },
      { name: 'authorization', hrs: 112, data: { amount: 2400 } },
      { name: 'dispute', hrs: 110, data: { amount: 2400 } }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7804',
    attributes: { name: 'Nina Okafor', tenure_years: 3, card_present: true },
    events: [
      { name: 'authorization', hrs: 90, data: { amount: 240 } },
      { name: 'authorization', hrs: 16, data: { amount: 88 } },
      { name: 'claim', hrs: 64, data: { amount: 1900 } },
      { name: 'login', hrs: 16 }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7811',
    attributes: { name: 'Ken Watanabe', tenure_years: 8, card_present: true },
    events: [
      { name: 'authorization', hrs: 300, data: { amount: 410 } },
      { name: 'authorization', hrs: 30, data: { amount: 65 } },
      { name: 'login', hrs: 5 }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7825',
    attributes: { name: 'Sofia Marchetti', tenure_years: 2, card_present: false },
    events: [
      { name: 'authorization', hrs: 140, data: { amount: 520 } },
      { name: 'dispute', hrs: 96, data: { amount: 520 } },
      { name: 'claim', hrs: 700, data: { amount: 240 } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4400',
    attributes: { name: 'Soylent Retail', mcc: '5411', risk: 'low' },
    events: [
      { name: 'settlement', hrs: 300, data: { amount: 220000 } },
      { name: 'settlement', hrs: 120, data: { amount: 245000 } },
      { name: 'payout', hrs: 100, data: { amount: 180000 } },
      { name: 'chargeback', hrs: 48, data: { amount: 740 } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4471',
    attributes: { name: 'Tyrell Digital', mcc: '6051', risk: 'high' },
    events: [
      { name: 'settlement', hrs: 150, data: { amount: 90000 } },
      { name: 'chargeback', hrs: 40, data: { amount: 1200 } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4488',
    attributes: { name: 'Wayne Home Goods', mcc: '5712', risk: 'low' },
    events: [
      { name: 'settlement', hrs: 400, data: { amount: 64000 } },
      { name: 'payout', hrs: 72, data: { amount: 41000 } },
      { name: 'payout', hrs: 24, data: { amount: 12500 } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4502',
    attributes: { name: 'Umbrella Wellness', mcc: '8099', risk: 'medium' },
    events: [
      { name: 'settlement', hrs: 210, data: { amount: 38000 } },
      { name: 'chargeback', hrs: 130, data: { amount: 310 } },
      { name: 'chargeback', hrs: 20, data: { amount: 95 } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4515',
    attributes: { name: 'Hooli Marketplace', mcc: '5999', risk: 'medium' },
    events: [
      { name: 'settlement', hrs: 340, data: { amount: 152000 } },
      { name: 'payout', hrs: 60, data: { amount: 98000 } },
      { name: 'payout', hrs: 12, data: { amount: 14000 } },
      { name: 'chargeback', hrs: 220, data: { amount: 2100 } }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9920',
    attributes: { corridor: 'US→US', type: 'payroll', recurring: true },
    events: [
      { name: 'wire', hrs: 240, data: { amount: 32000, dest: 'US' } },
      { name: 'wire', hrs: 168, data: { amount: 32000, dest: 'US' } },
      { name: 'wire', hrs: 72, data: { amount: 32000, dest: 'US' } }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9931',
    attributes: { corridor: 'US→KY', type: 'wire', recurring: false },
    events: [
      { name: 'wire', hrs: 120, data: { amount: 48000, dest: 'KY' } },
      { name: 'wire', hrs: 30, data: { amount: 51000, dest: 'KY' } }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9944',
    attributes: { corridor: 'US→US', type: 'vendor_batch', recurring: true },
    events: [
      { name: 'wire', hrs: 200, data: { amount: 8600, dest: 'US' } },
      { name: 'wire', hrs: 130, data: { amount: 9200, dest: 'US' } },
      { name: 'wire', hrs: 55, data: { amount: 8900, dest: 'US' } }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9958',
    attributes: { corridor: 'DE→US', type: 'invoice', recurring: false },
    events: [{ name: 'wire', hrs: 20, data: { amount: 27500, dest: 'US' } }]
  }
];

export function seedEntities(): Entity[] {
  return ENTITIES.map((e) => {
    const hrs = e.events.map((ev) => ev.hrs);
    return {
      entity_type: e.type,
      entity_id: e.id,
      attributes: e.attributes,
      event_count: e.events.length,
      first_seen: ago(Math.max(...hrs) + 60),
      updated_at: ago(Math.min(...hrs))
    };
  });
}

export function seedEntityEvents(): Map<string, EntityEvent[]> {
  const m = new Map<string, EntityEvent[]>();
  for (const e of ENTITIES) {
    // Oldest first with a stable seq, the way the real event store returns them.
    const ordered = [...e.events].sort((a, b) => b.hrs - a.hrs);
    m.set(
      `${e.type}/${e.id}`,
      ordered.map((ev, i) => ({
        entity_type: e.type,
        entity_id: e.id,
        event_name: ev.name,
        data: ev.data ?? {},
        seq: i + 1,
        occurred_at: ago(ev.hrs),
        recorded_at: ago(ev.hrs)
      }))
    );
  }
  return m;
}
