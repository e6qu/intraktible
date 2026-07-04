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
// The hero entities — the ones cases, suspended decisions and pre-approvals point
// at (APP-1001, APP-1007, MER-4400, MER-4515, TXN-9920, CUST-7804) — carry full
// profiles and 8–12 fully-payloaded events, since a prospect lands on them from
// those surfaces; the rest hold believable 4–6 event histories. Event payloads
// fall inside the seeded feature windows so computed features come out non-zero.
interface EntitySeed {
  type: string;
  id: string;
  attributes: Record<string, unknown>;
  events: { name: string; hrs: number; data?: Record<string, unknown> }[];
}

const ENTITIES: EntitySeed[] = [
  {
    // HERO — pa_1's pre-approved gold-tier applicant.
    type: 'applicant',
    id: 'APP-1001',
    attributes: {
      name: 'Jane Doe',
      email: 'jane.doe@example.com',
      dob: '1988-04-12',
      address: '1184 Maple Ave, Columbus, OH',
      country: 'US',
      segment: 'gold',
      kyc_level: 'verified',
      risk_rating: 'low',
      since: '2019-03-08'
    },
    events: [
      {
        name: 'transaction',
        hrs: 640,
        data: {
          amount: 2350,
          merchant: 'Delta Air Lines',
          mcc: '3058',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'transaction',
        hrs: 480,
        data: {
          amount: 890,
          merchant: 'Whole Foods',
          mcc: '5411',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      { name: 'login', hrs: 470, data: { device: 'iPhone 15 · iOS 18', ip_country: 'US' } },
      {
        name: 'transaction',
        hrs: 300,
        data: {
          amount: 1200,
          merchant: 'Best Buy',
          mcc: '5732',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'transaction',
        hrs: 210,
        data: {
          amount: 340,
          merchant: 'Shell Oil',
          mcc: '5541',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'transaction',
        hrs: 100,
        data: {
          amount: 4500,
          merchant: 'Apple Store',
          mcc: '5732',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'transaction',
        hrs: 60,
        data: {
          amount: 95,
          merchant: 'Trader Joes',
          mcc: '5411',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'transaction',
        hrs: 20,
        data: {
          amount: 610,
          merchant: 'Marriott Hotels',
          mcc: '3509',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      { name: 'login', hrs: 12, data: { device: 'MacBook · Safari 18', ip_country: 'US' } },
      { name: 'login', hrs: 2, data: { device: 'iPhone 15 · iOS 18', ip_country: 'US' } }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1002',
    attributes: {
      name: 'John Roe',
      email: 'john.roe@example.co.uk',
      dob: '1975-09-03',
      country: 'GB',
      segment: 'standard',
      kyc_level: 'verified',
      risk_rating: 'high'
    },
    events: [
      {
        name: 'transaction',
        hrs: 200,
        data: {
          amount: 50000,
          merchant: 'Sothebys',
          mcc: '5971',
          channel: 'ecom',
          geo: 'GB',
          currency: 'GBP'
        }
      },
      {
        name: 'transaction',
        hrs: 130,
        data: {
          amount: 1450,
          merchant: 'Harrods',
          mcc: '5311',
          channel: 'card_present',
          geo: 'GB',
          currency: 'GBP'
        }
      },
      { name: 'login', hrs: 48, data: { device: 'Windows · Chrome 126', ip_country: 'GB' } },
      { name: 'login', hrs: 30, data: { device: 'Windows · Chrome 126', ip_country: 'CY' } }
    ]
  },
  {
    // HERO — pa_3's platinum relationship with the expiring renewal.
    type: 'applicant',
    id: 'APP-1007',
    attributes: {
      name: 'Mei Lin',
      email: 'mei.lin@example.sg',
      dob: '1979-11-30',
      address: '12 Marina Blvd, Singapore',
      country: 'SG',
      segment: 'platinum',
      kyc_level: 'enhanced',
      risk_rating: 'low',
      since: '2015-07-21'
    },
    events: [
      {
        name: 'transaction',
        hrs: 600,
        data: {
          amount: 22000,
          merchant: 'Singapore Airlines',
          mcc: '3012',
          channel: 'ecom',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      {
        name: 'transaction',
        hrs: 420,
        data: {
          amount: 3800,
          merchant: 'Takashimaya',
          mcc: '5311',
          channel: 'card_present',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      { name: 'login', hrs: 336, data: { device: 'iPad · iPadOS 18', ip_country: 'SG' } },
      {
        name: 'transaction',
        hrs: 330,
        data: {
          amount: 9800,
          merchant: 'Cartier',
          mcc: '5944',
          channel: 'card_present',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      {
        name: 'transaction',
        hrs: 250,
        data: {
          amount: 520,
          merchant: 'Cold Storage',
          mcc: '5411',
          channel: 'card_present',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      {
        name: 'transaction',
        hrs: 150,
        data: {
          amount: 15000,
          merchant: 'Raffles Fine Art',
          mcc: '5971',
          channel: 'ecom',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      {
        name: 'transaction',
        hrs: 90,
        data: {
          amount: 6200,
          merchant: 'Mandarin Oriental',
          mcc: '3603',
          channel: 'ecom',
          geo: 'HK',
          currency: 'HKD'
        }
      },
      { name: 'login', hrs: 48, data: { device: 'iPhone 16 Pro · iOS 18', ip_country: 'SG' } },
      {
        name: 'transaction',
        hrs: 30,
        data: {
          amount: 1450,
          merchant: 'Grab',
          mcc: '4121',
          channel: 'ecom',
          geo: 'SG',
          currency: 'SGD'
        }
      },
      { name: 'login', hrs: 6, data: { device: 'iPhone 16 Pro · iOS 18', ip_country: 'SG' } }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1011',
    attributes: {
      name: 'Carlos Reyes',
      email: 'carlos.reyes@example.mx',
      dob: '1993-02-17',
      country: 'MX',
      segment: 'standard',
      kyc_level: 'verified',
      risk_rating: 'medium'
    },
    events: [
      {
        name: 'transaction',
        hrs: 340,
        data: {
          amount: 880,
          merchant: 'Liverpool',
          mcc: '5311',
          channel: 'card_present',
          geo: 'MX',
          currency: 'MXN'
        }
      },
      {
        name: 'transaction',
        hrs: 160,
        data: {
          amount: 2300,
          merchant: 'Aeromexico',
          mcc: '3011',
          channel: 'ecom',
          geo: 'MX',
          currency: 'MXN'
        }
      },
      { name: 'login', hrs: 88, data: { device: 'Android 15 · Samsung S24', ip_country: 'MX' } },
      {
        name: 'transaction',
        hrs: 26,
        data: {
          amount: 410,
          merchant: 'Oxxo',
          mcc: '5499',
          channel: 'card_present',
          geo: 'MX',
          currency: 'MXN'
        }
      }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1019',
    attributes: {
      name: 'Fatima Al-Sayed',
      email: 'fatima.alsayed@example.ae',
      dob: '1986-07-09',
      country: 'AE',
      segment: 'standard',
      kyc_level: 'reverification_failed',
      risk_rating: 'high'
    },
    events: [
      {
        name: 'transaction',
        hrs: 260,
        data: {
          amount: 7200,
          merchant: 'Dubai Duty Free',
          mcc: '5309',
          channel: 'card_present',
          geo: 'AE',
          currency: 'AED'
        }
      },
      {
        name: 'transaction',
        hrs: 190,
        data: {
          amount: 3100,
          merchant: 'Emirates',
          mcc: '3040',
          channel: 'ecom',
          geo: 'AE',
          currency: 'AED'
        }
      },
      {
        name: 'transaction',
        hrs: 40,
        data: {
          amount: 1900,
          merchant: 'Gold Souk Traders',
          mcc: '5944',
          channel: 'card_present',
          geo: 'AE',
          currency: 'AED'
        }
      },
      { name: 'login', hrs: 40, data: { device: 'iPhone 14 · iOS 17', ip_country: 'AE' } },
      { name: 'login', hrs: 18, data: { device: 'Windows · Edge 126', ip_country: 'TR' } }
    ]
  },
  {
    type: 'applicant',
    id: 'APP-1023',
    attributes: {
      name: 'Tomasz Nowak',
      email: 'tomasz.nowak@example.pl',
      dob: '1990-12-01',
      country: 'PL',
      segment: 'gold',
      kyc_level: 'verified',
      risk_rating: 'low'
    },
    events: [
      {
        name: 'transaction',
        hrs: 380,
        data: {
          amount: 3400,
          merchant: 'LOT Polish Airlines',
          mcc: '3050',
          channel: 'ecom',
          geo: 'PL',
          currency: 'PLN'
        }
      },
      {
        name: 'transaction',
        hrs: 120,
        data: {
          amount: 5100,
          merchant: 'Media Markt',
          mcc: '5732',
          channel: 'card_present',
          geo: 'PL',
          currency: 'PLN'
        }
      },
      {
        name: 'transaction',
        hrs: 60,
        data: {
          amount: 780,
          merchant: 'Allegro',
          mcc: '5999',
          channel: 'ecom',
          geo: 'PL',
          currency: 'PLN'
        }
      },
      { name: 'login', hrs: 9, data: { device: 'Android 15 · Pixel 8', ip_country: 'PL' } }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7781',
    attributes: {
      name: 'Ada Stark',
      email: 'ada.stark@example.com',
      tenure_years: 6,
      card_present: true,
      country: 'US',
      risk_rating: 'low'
    },
    events: [
      {
        name: 'authorization',
        hrs: 500,
        data: {
          amount: 120,
          merchant: 'REI',
          mcc: '5941',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'authorization',
        hrs: 200,
        data: {
          amount: 95,
          merchant: 'Safeway',
          mcc: '5411',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'authorization',
        hrs: 8,
        data: {
          amount: 1290,
          merchant: 'Alaska Airlines',
          mcc: '3057',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      { name: 'login', hrs: 8, data: { device: 'iPhone 15 · iOS 18', ip_country: 'US' } }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7790',
    attributes: {
      name: 'Bruce Pied',
      email: 'bruce.pied@example.com',
      tenure_years: 1,
      card_present: false,
      country: 'US',
      risk_rating: 'high'
    },
    events: [
      {
        name: 'authorization',
        hrs: 118,
        data: {
          amount: 60,
          merchant: 'Steam',
          mcc: '5816',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'authorization',
        hrs: 112,
        data: {
          amount: 2400,
          merchant: 'Newegg',
          mcc: '5732',
          channel: 'ecom',
          geo: 'RO',
          currency: 'USD'
        }
      },
      { name: 'dispute', hrs: 110, data: { amount: 2400, reason: 'fraud', network: 'visa' } },
      { name: 'login', hrs: 111, data: { device: 'Windows · Firefox 127', ip_country: 'RO' } },
      { name: 'login', hrs: 96, data: { device: 'iPhone 13 · iOS 17', ip_country: 'US' } }
    ]
  },
  {
    // HERO — the Okafor claim case (CLM-2214, stolen laptop) reads off this record.
    type: 'customer',
    id: 'CUST-7804',
    attributes: {
      name: 'Nina Okafor',
      email: 'nina.okafor@example.com',
      dob: '1991-06-25',
      address: '88 Lakeshore Dr, Chicago, IL',
      country: 'US',
      tenure_years: 3,
      card_present: true,
      kyc_level: 'verified',
      risk_rating: 'low'
    },
    events: [
      {
        name: 'authorization',
        hrs: 600,
        data: {
          amount: 1900,
          merchant: 'Best Buy',
          mcc: '5732',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'authorization',
        hrs: 400,
        data: {
          amount: 64,
          merchant: 'Peets Coffee',
          mcc: '5814',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      { name: 'login', hrs: 380, data: { device: 'Pixel 9 · Android 15', ip_country: 'US' } },
      {
        name: 'authorization',
        hrs: 180,
        data: {
          amount: 132,
          merchant: 'Target',
          mcc: '5310',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'authorization',
        hrs: 90,
        data: {
          amount: 240,
          merchant: 'Walgreens',
          mcc: '5912',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      },
      {
        name: 'claim',
        hrs: 64,
        data: { amount: 1900, reason: 'theft', item: 'laptop', police_report: true }
      },
      {
        name: 'authorization',
        hrs: 16,
        data: {
          amount: 88,
          merchant: 'Uber Eats',
          mcc: '5814',
          channel: 'ecom',
          geo: 'US',
          currency: 'USD'
        }
      },
      { name: 'login', hrs: 16, data: { device: 'Pixel 9 · Android 15', ip_country: 'US' } },
      {
        name: 'authorization',
        hrs: 3,
        data: {
          amount: 41,
          merchant: 'Shell Oil',
          mcc: '5541',
          channel: 'card_present',
          geo: 'US',
          currency: 'USD'
        }
      }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7811',
    attributes: {
      name: 'Ken Watanabe',
      email: 'ken.watanabe@example.jp',
      tenure_years: 8,
      card_present: true,
      country: 'JP',
      risk_rating: 'low'
    },
    events: [
      {
        name: 'authorization',
        hrs: 300,
        data: {
          amount: 410,
          merchant: 'Bic Camera',
          mcc: '5732',
          channel: 'card_present',
          geo: 'JP',
          currency: 'JPY'
        }
      },
      {
        name: 'claim',
        hrs: 250,
        data: { amount: 2900, reason: 'accidental_damage', item: 'camera lens' }
      },
      {
        name: 'authorization',
        hrs: 30,
        data: {
          amount: 65,
          merchant: 'Lawson',
          mcc: '5411',
          channel: 'card_present',
          geo: 'JP',
          currency: 'JPY'
        }
      },
      { name: 'login', hrs: 5, data: { device: 'iPhone 15 · iOS 18', ip_country: 'JP' } }
    ]
  },
  {
    type: 'customer',
    id: 'CUST-7825',
    attributes: {
      name: 'Sofia Marchetti',
      email: 'sofia.marchetti@example.it',
      tenure_years: 2,
      card_present: false,
      country: 'IT',
      risk_rating: 'medium'
    },
    events: [
      {
        name: 'claim',
        hrs: 700,
        data: { amount: 240, reason: 'accidental_damage', item: 'phone screen' }
      },
      {
        name: 'authorization',
        hrs: 140,
        data: {
          amount: 520,
          merchant: 'Zalando',
          mcc: '5651',
          channel: 'ecom',
          geo: 'IT',
          currency: 'EUR'
        }
      },
      { name: 'dispute', hrs: 96, data: { amount: 520, reason: 'quality', network: 'mastercard' } },
      {
        name: 'claim',
        hrs: 60,
        data: { amount: 310, reason: 'accidental_damage', item: 'phone — repeat claimant' }
      },
      { name: 'login', hrs: 58, data: { device: 'iPhone 12 · iOS 17', ip_country: 'IT' } }
    ]
  },
  {
    // HERO — pa_4's pre-approved low-risk retail merchant.
    type: 'merchant',
    id: 'MER-4400',
    attributes: {
      name: 'Soylent Retail',
      mcc: '5411',
      risk: 'low',
      incorporation: '2012-05-17',
      address: '400 Industrial Way, Fresno, CA',
      country: 'US',
      kyc_level: 'verified',
      since: '2021-02-03'
    },
    events: [
      {
        name: 'settlement',
        hrs: 640,
        data: { amount: 208000, currency: 'USD', transactions: 7910 }
      },
      {
        name: 'settlement',
        hrs: 470,
        data: { amount: 231000, currency: 'USD', transactions: 8480 }
      },
      {
        name: 'settlement',
        hrs: 300,
        data: { amount: 220000, currency: 'USD', transactions: 8122 }
      },
      { name: 'payout', hrs: 280, data: { amount: 175000, currency: 'USD', rail: 'ACH' } },
      {
        name: 'chargeback',
        hrs: 260,
        data: { amount: 415, reason: 'product_not_received', network: 'visa' }
      },
      {
        name: 'settlement',
        hrs: 120,
        data: { amount: 245000, currency: 'USD', transactions: 9034 }
      },
      { name: 'payout', hrs: 100, data: { amount: 180000, currency: 'USD', rail: 'ACH' } },
      {
        name: 'chargeback',
        hrs: 48,
        data: { amount: 740, reason: 'fraud', network: 'mastercard' }
      },
      {
        name: 'settlement',
        hrs: 24,
        data: { amount: 238000, currency: 'USD', transactions: 8761 }
      },
      { name: 'payout', hrs: 6, data: { amount: 190000, currency: 'USD', rail: 'RTP' } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4471',
    attributes: {
      name: 'Tyrell Digital',
      mcc: '6051',
      risk: 'high',
      incorporation: '2023-01-30',
      country: 'US',
      kyc_level: 'enhanced_underwriting'
    },
    events: [
      { name: 'settlement', hrs: 150, data: { amount: 90000, currency: 'USD', transactions: 410 } },
      { name: 'chargeback', hrs: 40, data: { amount: 1200, reason: 'fraud', network: 'visa' } },
      { name: 'settlement', hrs: 26, data: { amount: 112000, currency: 'USD', transactions: 522 } },
      { name: 'chargeback', hrs: 10, data: { amount: 2600, reason: 'fraud', network: 'visa' } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4488',
    attributes: {
      name: 'Wayne Home Goods',
      mcc: '5712',
      risk: 'low',
      incorporation: '2009-08-11',
      country: 'US',
      kyc_level: 'verified'
    },
    events: [
      { name: 'settlement', hrs: 400, data: { amount: 64000, currency: 'USD', transactions: 310 } },
      { name: 'settlement', hrs: 96, data: { amount: 71000, currency: 'USD', transactions: 342 } },
      { name: 'payout', hrs: 72, data: { amount: 41000, currency: 'USD', rail: 'ACH' } },
      { name: 'payout', hrs: 24, data: { amount: 12500, currency: 'USD', rail: 'ACH' } }
    ]
  },
  {
    type: 'merchant',
    id: 'MER-4502',
    attributes: {
      name: 'Umbrella Wellness',
      mcc: '8099',
      risk: 'medium',
      incorporation: '2019-04-02',
      country: 'US',
      kyc_level: 'verified'
    },
    events: [
      { name: 'settlement', hrs: 210, data: { amount: 38000, currency: 'USD', transactions: 205 } },
      { name: 'chargeback', hrs: 130, data: { amount: 310, reason: 'quality', network: 'visa' } },
      { name: 'settlement', hrs: 50, data: { amount: 46000, currency: 'USD', transactions: 238 } },
      {
        name: 'chargeback',
        hrs: 20,
        data: { amount: 95, reason: 'duplicate', network: 'mastercard' }
      },
      { name: 'payout', hrs: 14, data: { amount: 29500, currency: 'USD', rail: 'ACH' } }
    ]
  },
  {
    // HERO — pa_7's expired payout fast-lane; the Hooli payout case's volume spike
    // (their holiday sale) shows in the recent settlements and payouts.
    type: 'merchant',
    id: 'MER-4515',
    attributes: {
      name: 'Hooli Marketplace',
      mcc: '5999',
      risk: 'medium',
      incorporation: '2016-09-02',
      address: '1 Hooli Way, Palo Alto, CA',
      country: 'US',
      kyc_level: 'verified',
      since: '2022-11-14'
    },
    events: [
      {
        name: 'settlement',
        hrs: 340,
        data: { amount: 152000, currency: 'USD', transactions: 3120 }
      },
      {
        name: 'settlement',
        hrs: 260,
        data: { amount: 139000, currency: 'USD', transactions: 2870 }
      },
      { name: 'chargeback', hrs: 220, data: { amount: 2100, reason: 'quality', network: 'visa' } },
      { name: 'payout', hrs: 180, data: { amount: 87000, currency: 'USD', rail: 'ACH' } },
      {
        name: 'settlement',
        hrs: 120,
        data: { amount: 186000, currency: 'USD', transactions: 3910 }
      },
      { name: 'payout', hrs: 60, data: { amount: 98000, currency: 'USD', rail: 'ACH' } },
      {
        name: 'settlement',
        hrs: 40,
        data: { amount: 243000, currency: 'USD', transactions: 5230 }
      },
      {
        name: 'chargeback',
        hrs: 30,
        data: { amount: 380, reason: 'product_not_received', network: 'mastercard' }
      },
      { name: 'payout', hrs: 12, data: { amount: 14000, currency: 'USD', rail: 'RTP' } },
      { name: 'payout', hrs: 4, data: { amount: 121000, currency: 'USD', rail: 'ACH' } }
    ]
  },
  {
    // HERO — pa_6's whitelisted recurring payroll corridor: a steady weekly batch.
    type: 'transaction',
    id: 'TXN-9920',
    attributes: {
      corridor: 'US→US',
      type: 'payroll',
      recurring: true,
      originator: 'Initech Payroll Services',
      beneficiary_bank: 'First National Bank',
      currency: 'USD',
      purpose: 'weekly payroll batch'
    },
    events: [
      {
        name: 'wire',
        hrs: 696,
        data: { amount: 31800, dest: 'US', currency: 'USD', reference: 'PAYROLL-2418' }
      },
      {
        name: 'wire',
        hrs: 600,
        data: { amount: 32450, dest: 'US', currency: 'USD', reference: 'PAYROLL-2419' }
      },
      {
        name: 'wire',
        hrs: 432,
        data: { amount: 31950, dest: 'US', currency: 'USD', reference: 'PAYROLL-2420' }
      },
      {
        name: 'wire',
        hrs: 336,
        data: { amount: 32000, dest: 'US', currency: 'USD', reference: 'PAYROLL-2421' }
      },
      {
        name: 'wire',
        hrs: 240,
        data: { amount: 32000, dest: 'US', currency: 'USD', reference: 'PAYROLL-2422' }
      },
      {
        name: 'wire',
        hrs: 168,
        data: { amount: 32000, dest: 'US', currency: 'USD', reference: 'PAYROLL-2423' }
      },
      {
        name: 'wire',
        hrs: 96,
        data: { amount: 32600, dest: 'US', currency: 'USD', reference: 'PAYROLL-2424' }
      },
      {
        name: 'wire',
        hrs: 48,
        data: { amount: 31700, dest: 'US', currency: 'USD', reference: 'PAYROLL-2425' }
      },
      {
        name: 'wire',
        hrs: 8,
        data: { amount: 32150, dest: 'US', currency: 'USD', reference: 'PAYROLL-2426' }
      }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9931',
    attributes: {
      corridor: 'US→KY',
      type: 'wire',
      recurring: false,
      originator: 'Acme Imports LLC',
      currency: 'USD'
    },
    events: [
      {
        name: 'wire',
        hrs: 300,
        data: { amount: 39000, dest: 'KY', currency: 'USD', reference: 'INV-0781' }
      },
      {
        name: 'wire',
        hrs: 120,
        data: { amount: 48000, dest: 'KY', currency: 'USD', reference: 'INV-0802' }
      },
      {
        name: 'wire',
        hrs: 74,
        data: { amount: 44500, dest: 'KY', currency: 'USD', reference: 'INV-0809' }
      },
      {
        name: 'wire',
        hrs: 30,
        data: { amount: 51000, dest: 'KY', currency: 'USD', reference: 'INV-0815' }
      }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9944',
    attributes: {
      corridor: 'US→US',
      type: 'vendor_batch',
      recurring: true,
      originator: 'Massive Dynamic Procurement',
      currency: 'USD'
    },
    events: [
      {
        name: 'wire',
        hrs: 200,
        data: { amount: 8600, dest: 'US', currency: 'USD', reference: 'VB-1141' }
      },
      {
        name: 'wire',
        hrs: 130,
        data: { amount: 9200, dest: 'US', currency: 'USD', reference: 'VB-1150' }
      },
      {
        name: 'wire',
        hrs: 55,
        data: { amount: 8900, dest: 'US', currency: 'USD', reference: 'VB-1158' }
      },
      {
        name: 'wire',
        hrs: 12,
        data: { amount: 9400, dest: 'US', currency: 'USD', reference: 'VB-1163' }
      }
    ]
  },
  {
    type: 'transaction',
    id: 'TXN-9958',
    attributes: {
      corridor: 'DE→US',
      type: 'invoice',
      recurring: false,
      originator: 'Rhein Maschinenbau GmbH',
      currency: 'EUR'
    },
    events: [
      {
        name: 'wire',
        hrs: 380,
        data: { amount: 24800, dest: 'US', currency: 'EUR', reference: 'RE-2026-031' }
      },
      {
        name: 'wire',
        hrs: 20,
        data: { amount: 27500, dest: 'US', currency: 'EUR', reference: 'RE-2026-047' }
      }
    ]
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
