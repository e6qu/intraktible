// SPDX-License-Identifier: AGPL-3.0-or-later
// Governance seed: disposition policies (with version histories whose band edges
// match the flows' outputs — the decision generator applies these exact specs),
// pre-approvals across every lifecycle state, monitors whose thresholds sit on the
// right side of the seeded rates (a believable firing/ok mix), SLOs (meeting and
// breaching), assertions, webhooks, notifications, API keys, grants, schedules,
// comment threads and the workspace audit trail.

import type {
  Policy,
  PreApproval,
  Monitor,
  AssertionCase,
  Webhook,
  Notification,
  AuditEntry,
  ManagedApiKey,
  FlowGrant,
  ScheduledDeploy,
  Decision
} from '$lib/api';
import { ago, ahead, ACTOR, AVA, MARCUS, PRIYA, DIEGO, LENA } from './base';

export function seedPolicies(): Policy[] {
  return [
    {
      policy_id: 'pol_credit',
      name: 'Credit Disposition',
      flow_slug: 'credit-decision',
      latest: 2,
      updated_at: ago(38),
      versions: [
        {
          version: 1,
          etag: 'petag-c1',
          published_at: ago(160),
          published_by: AVA,
          spec: {
            rules: [
              {
                when: 'risk < 30',
                disposition: 'approve',
                code: 'APPROVED',
                description: 'Meets approval criteria'
              },
              {
                when: 'fico_score < 620',
                disposition: 'decline',
                code: 'LOW_SCORE',
                description: 'Credit score below threshold'
              },
              {
                when: 'risk >= 70',
                disposition: 'decline',
                code: 'DELINQUENCY_HISTORY',
                description: 'Serious delinquency on file'
              },
              {
                when: 'risk >= 30',
                disposition: 'refer',
                code: 'DTI_TOO_HIGH',
                description: 'Debt-to-income ratio too high'
              }
            ],
            default: 'refer'
          }
        },
        {
          version: 2,
          etag: 'petag-c2',
          published_at: ago(38),
          published_by: MARCUS,
          spec: {
            // FCRA / Reg B permissible adverse-action reason codes (no generic
            // risk-band labels): the matched rule's code becomes the decision's
            // adverse-action reason and feeds the adverse-action narrative node.
            rules: [
              {
                when: 'risk < 35',
                disposition: 'approve',
                code: 'APPROVED',
                description: 'Meets approval criteria'
              },
              {
                when: 'fico_score < 620',
                disposition: 'decline',
                code: 'LOW_SCORE',
                description: 'Credit score below threshold'
              },
              {
                when: 'risk >= 70',
                disposition: 'decline',
                code: 'DELINQUENCY_HISTORY',
                description: 'Serious delinquency on file'
              },
              {
                when: 'risk >= 35',
                disposition: 'refer',
                code: 'DTI_TOO_HIGH',
                description: 'Debt-to-income ratio too high'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_aml',
      name: 'AML Clearing Policy',
      flow_slug: 'aml-screening',
      latest: 2,
      updated_at: ago(18),
      versions: [
        {
          version: 1,
          etag: 'petag-a1',
          published_at: ago(90),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'aml_score >= 6',
                disposition: 'refer',
                code: 'AML_HIGH',
                description: 'Refer high AML risk to analyst'
              },
              {
                when: 'aml_score < 6',
                disposition: 'approve',
                code: 'AML_CLEAR',
                description: 'Clear low AML risk'
              }
            ],
            default: 'refer'
          }
        },
        {
          version: 2,
          etag: 'petag-a2',
          published_at: ago(18),
          published_by: MARCUS,
          spec: {
            // v2 closes the v1 gap: a confirmed sanctions hit or a structuring
            // pattern can never auto-clear, whatever the composite score says.
            rules: [
              {
                when: 'sanctions_hit == 1',
                disposition: 'refer',
                code: 'SANCTIONS_MATCH',
                description: 'Confirmed sanctions/watchlist match'
              },
              {
                when: 'structuring == 1',
                disposition: 'refer',
                code: 'AML_STRUCTURING',
                description: 'Sub-threshold structuring pattern'
              },
              {
                when: 'aml_score >= 6',
                disposition: 'refer',
                code: 'AML_HIGH',
                description: 'AML risk above clearing band'
              },
              {
                when: 'aml_score < 6',
                disposition: 'approve',
                code: 'AML_CLEAR',
                description: 'AML risk below clearing band'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_fraud',
      name: 'Card Fraud Policy',
      flow_slug: 'card-fraud',
      latest: 2,
      updated_at: ago(70),
      versions: [
        {
          version: 1,
          etag: 'petag-f1',
          published_at: ago(200),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'fraud_p >= 80',
                disposition: 'decline',
                code: 'FRAUD_BLOCK',
                description: 'Block high fraud probability'
              },
              {
                when: 'fraud_p >= 40',
                disposition: 'refer',
                code: 'FRAUD_REVIEW',
                description: 'Refer to fraud analyst'
              },
              {
                when: 'fraud_p < 40',
                disposition: 'approve',
                code: 'FRAUD_PASS',
                description: 'Allow low fraud probability'
              }
            ],
            default: 'refer'
          }
        },
        {
          version: 2,
          etag: 'petag-f2',
          published_at: ago(70),
          published_by: MARCUS,
          spec: {
            // Bands follow the flow's v3 tightening (review at 35, was 40).
            rules: [
              {
                when: 'fraud_p >= 80',
                disposition: 'decline',
                code: 'FRAUD_BLOCK',
                description: 'Block high fraud probability'
              },
              {
                when: 'fraud_p >= 35',
                disposition: 'refer',
                code: 'FRAUD_REVIEW',
                description: 'Refer to fraud analyst'
              },
              {
                when: 'fraud_p < 35',
                disposition: 'approve',
                code: 'FRAUD_PASS',
                description: 'Allow low fraud probability'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_kyc',
      name: 'KYC Onboarding Policy',
      flow_slug: 'kyc-onboarding',
      latest: 1,
      updated_at: ago(80 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-k1',
          published_at: ago(80 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'identity_conf < 60',
                disposition: 'refer',
                code: 'KYC_LOW_CONF',
                description: 'Identity confidence below threshold'
              },
              {
                when: 'identity_conf >= 60',
                disposition: 'approve',
                code: 'KYC_CLEAR',
                description: 'Identity verified'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_dispute',
      name: 'Chargeback Triage Policy',
      flow_slug: 'dispute-triage',
      latest: 1,
      updated_at: ago(60 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-d1',
          published_at: ago(60 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'triage >= 50',
                disposition: 'refer',
                code: 'DISPUTE_REVIEW',
                description: 'High triage score — route to disputes ops'
              },
              {
                when: 'triage < 50',
                disposition: 'approve',
                code: 'DISPUTE_AUTO_REFUND',
                description: 'Below triage band — auto-refund'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_merchant',
      name: 'Merchant Onboarding Policy',
      flow_slug: 'merchant-onboarding',
      latest: 1,
      updated_at: ago(70 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-m1',
          published_at: ago(70 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'uw_score >= 25',
                disposition: 'refer',
                code: 'UW_HIGH',
                description: 'Underwriting score above the review gate'
              },
              {
                when: 'uw_score < 25',
                disposition: 'approve',
                code: 'UW_PASS',
                description: 'Underwriting score below the review gate'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_collections',
      name: 'Hardship Program Policy',
      flow_slug: 'collections-hardship',
      latest: 1,
      updated_at: ago(46),
      versions: [
        {
          version: 1,
          etag: 'petag-h1',
          published_at: ago(46),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'hardship_score >= 70',
                disposition: 'refer',
                code: 'HARDSHIP_ESCALATED',
                description: 'Concession above authority — supervisor review'
              },
              {
                when: 'hardship_score >= 45',
                disposition: 'approve',
                code: 'HARDSHIP_PLAN',
                description: 'Qualifies for a hardship plan'
              },
              {
                when: 'hardship_score < 45',
                disposition: 'decline',
                code: 'HARDSHIP_NOT_ELIGIBLE',
                description: 'Does not meet hardship criteria'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_claim',
      name: 'Claim Payout Policy',
      flow_slug: 'claim-triage',
      latest: 1,
      updated_at: ago(120),
      versions: [
        {
          version: 1,
          etag: 'petag-cl1',
          published_at: ago(120),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'lapsed == 1',
                disposition: 'decline',
                code: 'POLICY_LAPSED',
                description: 'Protection plan lapsed before the loss date'
              },
              {
                when: 'fraud_p >= 60',
                disposition: 'refer',
                code: 'CLAIM_FRAUD_REVIEW',
                description: 'Abuse-pattern signals — adjuster review'
              },
              {
                when: 'fast_track == 1',
                disposition: 'approve',
                code: 'CLAIM_FAST_TRACK',
                description: 'Low-value first claim — fast-track payment'
              },
              {
                when: 'amount_ratio > 0.5',
                disposition: 'refer',
                code: 'CLAIM_HIGH_SEVERITY',
                description: 'High severity relative to coverage'
              },
              {
                when: 'amount_ratio <= 0.5',
                disposition: 'approve',
                code: 'CLAIM_PAY',
                description: 'Within coverage — pay'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_payout',
      name: 'Payout Release Policy',
      flow_slug: 'payout-risk',
      latest: 1,
      updated_at: ago(55),
      versions: [
        {
          version: 1,
          etag: 'petag-p1',
          published_at: ago(55),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'action == "hold"',
                disposition: 'decline',
                code: 'PAYOUT_HOLD',
                description: 'Funds held — risk above release threshold'
              },
              {
                when: 'action == "review"',
                disposition: 'refer',
                code: 'PAYOUT_REVIEW',
                description: 'Manual release review'
              },
              {
                when: 'action == "release"',
                disposition: 'approve',
                code: 'PAYOUT_RELEASE',
                description: 'Released within risk appetite'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_limit',
      name: 'Limit Increase Policy',
      flow_slug: 'limit-increase',
      latest: 1,
      updated_at: ago(70),
      versions: [
        {
          version: 1,
          etag: 'petag-l1',
          published_at: ago(70),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'risk < 20 && utilization < 0.6',
                disposition: 'approve',
                code: 'CLI_APPROVED',
                description: 'Auto-approved limit increase'
              },
              {
                when: 'risk < 45',
                disposition: 'refer',
                code: 'CLI_MANUAL',
                description: 'Manual credit-ops review'
              },
              {
                when: 'risk >= 45',
                disposition: 'decline',
                code: 'LOW_SCORE',
                description: 'Risk score above CLI threshold'
              }
            ],
            default: 'refer'
          }
        }
      ]
    }
  ];
}

export function seedPreApprovals(): PreApproval[] {
  return [
    {
      preapproval_id: 'pa_1',
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      disposition: 'approve',
      terms: { limit_usd: 25000, apr: 12.5 },
      policy_id: 'pol_credit',
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(20),
      status: 'active',
      honored_count: 3,
      note: 'Pre-approved gold-tier applicant',
      granted_at: ago(120),
      granted_by: AVA,
      updated_at: ago(120)
    },
    {
      preapproval_id: 'pa_2',
      entity_type: 'applicant',
      entity_id: 'APP-1002',
      disposition: 'decline',
      flow_slug: 'credit-decision',
      valid_until: ahead(-1),
      status: 'revoked',
      revoked_reason: 'Adverse media match',
      honored_count: 0,
      granted_at: ago(200),
      granted_by: MARCUS,
      updated_at: ago(10)
    },
    {
      preapproval_id: 'pa_3',
      entity_type: 'applicant',
      entity_id: 'APP-1007',
      disposition: 'approve',
      terms: { limit_usd: 40000, apr: 9.9 },
      policy_id: 'pol_credit',
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(2),
      status: 'active',
      honored_count: 6,
      note: 'Platinum relationship — expiring soon, renewal queued',
      granted_at: ago(330),
      granted_by: MARCUS,
      updated_at: ago(48)
    },
    {
      preapproval_id: 'pa_4',
      entity_type: 'merchant',
      entity_id: 'MER-4400',
      disposition: 'approve',
      terms: { mdr_bps: 240, monthly_cap_usd: 500000 },
      flow_slug: 'merchant-onboarding',
      valid_until: ahead(60),
      status: 'active',
      honored_count: 1,
      note: 'Established low-risk retail merchant',
      granted_at: ago(80),
      granted_by: MARCUS,
      updated_at: ago(80)
    },
    {
      preapproval_id: 'pa_5',
      entity_type: 'applicant',
      entity_id: 'APP-1011',
      disposition: 'approve',
      terms: { limit_usd: 12000, apr: 15.0 },
      policy_id: 'pol_credit',
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(1),
      status: 'active',
      honored_count: 0,
      note: 'Promo offer — expires within 24h',
      granted_at: ago(160),
      granted_by: AVA,
      updated_at: ago(160)
    },
    {
      preapproval_id: 'pa_6',
      entity_type: 'transaction',
      entity_id: 'TXN-9920',
      disposition: 'approve',
      flow_slug: 'aml-screening',
      valid_until: ahead(30),
      status: 'active',
      honored_count: 12,
      note: 'Whitelisted recurring payroll corridor',
      granted_at: ago(240),
      granted_by: MARCUS,
      updated_at: ago(72)
    },
    {
      preapproval_id: 'pa_7',
      entity_type: 'merchant',
      entity_id: 'MER-4515',
      disposition: 'approve',
      terms: { auto_release_cap_usd: 25000 },
      flow_slug: 'payout-risk',
      valid_until: ahead(-3),
      status: 'active',
      honored_count: 9,
      note: 'Seasonal payout fast-lane — expired, renewal under review',
      granted_at: ago(1100),
      granted_by: MARCUS,
      updated_at: ago(400)
    },
    {
      preapproval_id: 'pa_8',
      entity_type: 'applicant',
      entity_id: 'APP-1019',
      disposition: 'approve',
      flow_slug: 'kyc-onboarding',
      valid_until: ahead(14),
      status: 'revoked',
      revoked_reason: 'Document reverification failed',
      honored_count: 2,
      granted_at: ago(500),
      granted_by: AVA,
      updated_at: ago(26)
    }
  ];
}

// Monitor thresholds are set against the RATES THE SEED ACTUALLY PRODUCES (the
// router recomputes status live from flowMetrics), so the firing/ok mix is real:
// credit's failure rate breaches (the bureau outage), AML's referral rate and
// latency breach (structuring + SAR narratives), dispute automation dipped below
// its floor, merchant volume sits under its throughput floor.
export function seedMonitors(): Map<string, Monitor[]> {
  const m = new Map<string, Monitor[]>();
  const mon = (
    id: string,
    flow: string,
    metric: Monitor['metric'],
    op: Monitor['op'],
    threshold: number,
    description: string
  ): Monitor => ({
    monitor_id: id,
    flow_id: flow,
    metric,
    op,
    threshold,
    description,
    status: { actual: 0, computable: false, firing: false }
  });
  m.set('flow_credit', [
    mon('mon_c1', 'flow_credit', 'failure_rate', 'gt', 0.05, 'Decision failure rate'),
    mon('mon_c2', 'flow_credit', 'refer_rate', 'gt', 0.4, 'Manual-review referral rate'),
    mon('mon_c3', 'flow_credit', 'distribution_drift_psi', 'gt', 0.25, 'Disposition drift (PSI)')
  ]);
  m.set('flow_aml', [
    mon('mon_a1', 'flow_aml', 'volume', 'lt', 5, 'Screening throughput floor'),
    mon('mon_a2', 'flow_aml', 'refer_rate', 'gt', 0.25, 'SAR referral rate'),
    mon('mon_a3', 'flow_aml', 'avg_latency_ms', 'gt', 200, 'p50 screening latency')
  ]);
  m.set('flow_fraud', [
    mon('mon_f1', 'flow_fraud', 'decline_rate', 'gt', 0.15, 'Block rate'),
    mon('mon_f2', 'flow_fraud', 'avg_latency_ms', 'gt', 300, 'p50 scoring latency')
  ]);
  m.set('flow_kyc', [mon('mon_k1', 'flow_kyc', 'refer_rate', 'gt', 0.5, 'EDD referral rate')]);
  m.set('flow_dispute', [
    mon('mon_d1', 'flow_dispute', 'automation_rate', 'lt', 0.5, 'Auto-refund automation rate')
  ]);
  m.set('flow_merchant', [
    mon('mon_m1', 'flow_merchant', 'volume', 'lt', 20, 'Boarding throughput floor')
  ]);
  m.set('flow_collections', [
    mon('mon_h1', 'flow_collections', 'refer_rate', 'gt', 0.4, 'Supervisor escalation rate')
  ]);
  m.set('flow_claim', [
    mon('mon_cl1', 'flow_claim', 'failure_rate', 'gt', 0.1, 'Decision failure rate')
  ]);
  m.set('flow_payout', [
    mon('mon_p1', 'flow_payout', 'decline_rate', 'gt', 0.2, 'Funds-hold rate'),
    mon('mon_p2', 'flow_payout', 'avg_latency_ms', 'gt', 250, 'p50 release latency')
  ]);
  return m;
}

export function seedAssertions(): Map<string, AssertionCase[]> {
  const m = new Map<string, AssertionCase[]>();
  m.set('flow_credit', [
    {
      name: 'prime applicant approves',
      input: {
        income: 120000,
        debt: 4000,
        revolving_balance: 1000,
        credit_limit: 20000,
        delinquencies_24m: 0,
        fico_score: 800
      },
      expect: { approved: true }
    },
    {
      name: 'sub-prime high dti declines',
      input: {
        income: 30000,
        debt: 26000,
        revolving_balance: 14000,
        credit_limit: 15000,
        delinquencies_24m: 3,
        fico_score: 600
      },
      expect: { approved: false }
    },
    {
      // Mid-tier applicant (DTI ~0.47, mid-600s FICO, 1 delinquency) lands in the
      // review band (risk ~53), opens a case, and gets a conservative affordability
      // -aware line (min(4 mo disposable income, 10% of annual) = $6,000) — never
      // auto-approved or auto-declined.
      name: 'mid band refers to underwriter',
      input: {
        income: 60000,
        debt: 28000,
        revolving_balance: 8000,
        credit_limit: 15000,
        delinquencies_24m: 1,
        fico_score: 660
      },
      expect: { offered_limit: 6000 }
    }
  ]);
  m.set('flow_aml', [
    {
      name: 'small domestic clears',
      input: { amount: 2000, origin_country: 'US', dest_country: 'US', watchlist_score: 5 },
      expect: { cleared: true }
    },
    {
      name: 'large cross-border refers',
      input: { amount: 60000, origin_country: 'US', dest_country: 'KY', watchlist_score: 10 },
      expect: { cleared: false }
    },
    {
      // A confirmed sanctions/watchlist hit hard-stops: even a small domestic wire
      // that would otherwise clear can never auto-clear once watchlist_score >= 80.
      name: 'sanctions hit cannot clear',
      input: { amount: 1000, origin_country: 'US', dest_country: 'US', watchlist_score: 90 },
      expect: { cleared: false }
    },
    {
      // v3's structuring heuristics: five sub-threshold deposits in 30 days refer
      // even though the composite score alone would clear.
      name: 'structuring pattern refers',
      input: {
        amount: 9200,
        origin_country: 'US',
        dest_country: 'US',
        watchlist_score: 5,
        deposits_30d: 5,
        outflow_ratio: 0.4
      },
      expect: { cleared: false }
    }
  ]);
  m.set('flow_fraud', [
    {
      name: 'low velocity allows',
      input: { amount: 80, tx_count_1h: 1, device_score: 10, avg_ticket: 120 },
      expect: { blocked: false }
    },
    {
      name: 'high velocity blocks',
      input: { amount: 1500, tx_count_1h: 9, device_score: 95, avg_ticket: 120 },
      expect: { blocked: true }
    }
  ]);
  m.set('flow_collections', [
    {
      name: 'qualifying hardship enrolls',
      input: {
        prior_income: 5200,
        current_income: 3100,
        missed_payments_6m: 2,
        medical_event: 0,
        tenure_years: 4,
        balance_usd: 8400
      },
      expect: { enrolled: true }
    },
    {
      name: 'minor dip stays on standard collections',
      input: {
        prior_income: 5200,
        current_income: 4900,
        missed_payments_6m: 1,
        medical_event: 0,
        tenure_years: 2,
        balance_usd: 4000
      },
      expect: { enrolled: false }
    }
  ]);
  m.set('flow_claim', [
    {
      name: 'low-value first claim fast-tracks',
      input: {
        amount: 120,
        coverage_limit: 3000,
        policy_active: 1,
        prior_claims_24m: 0,
        days_since_policy_start: 400
      },
      expect: { paid: true }
    },
    {
      name: 'lapsed policy denies',
      input: {
        amount: 900,
        coverage_limit: 3000,
        policy_active: 0,
        prior_claims_24m: 0,
        days_since_policy_start: 500
      },
      expect: { paid: false }
    }
  ]);
  m.set('flow_payout', [
    {
      name: 'routine payout releases',
      input: {
        amount: 2400,
        avg_payout_30d: 2600,
        payouts_24h: 1,
        account_age_days: 220,
        chargeback_rate: 0.004
      },
      expect: { released: true }
    },
    {
      name: 'young account velocity holds',
      input: {
        amount: 14000,
        avg_payout_30d: 4000,
        payouts_24h: 5,
        account_age_days: 12,
        chargeback_rate: 0.02
      },
      expect: { released: false }
    }
  ]);
  return m;
}

export function seedWebhooks(): Webhook[] {
  return [
    {
      webhook_id: 'wh_1',
      url: 'https://hooks.slack.demo/risk-alerts',
      note: 'Risk team Slack',
      events: ['monitor.fired'],
      active: true,
      delivery_count: 42,
      last_status: 200,
      last_ok: true,
      last_delivery_at: ago(6),
      created_at: ago(400)
    },
    {
      webhook_id: 'wh_2',
      url: 'https://pager.demo/aml-oncall',
      note: 'AML on-call pager',
      events: ['monitor.fired', 'case.breached'],
      active: true,
      delivery_count: 7,
      last_status: 500,
      last_ok: false,
      last_error: 'upstream 500',
      last_delivery_at: ago(3),
      created_at: ago(200)
    },
    {
      webhook_id: 'wh_3',
      url: 'https://hooks.pagerduty.demo/payout-oncall',
      note: 'Payout ops PagerDuty (paused during migration)',
      events: ['case.breached'],
      active: false,
      delivery_count: 19,
      last_status: 200,
      last_ok: true,
      last_delivery_at: ago(320),
      created_at: ago(900)
    }
  ];
}

export function seedNotifications(): Notification[] {
  return [
    // Human-task reminders: a flow escalates to a case, and the assignee is pulled to it
    // (assigned -> due-soon -> overdue) without anything auto-resolving the human step.
    {
      notification_id: 'ntf_task_assigned',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_1',
      snippet: 'Review task assigned to you: credit_review',
      author: 'system',
      read: false,
      created_at: ago(2)
    },
    {
      notification_id: 'ntf_task_due',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_2',
      snippet: 'Review task due soon: aml_alert',
      author: 'sla-sweeper',
      read: false,
      created_at: ago(3)
    },
    {
      notification_id: 'ntf_task_overdue',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_3',
      snippet: 'Review task OVERDUE: kyc_review',
      author: 'sla-sweeper',
      read: false,
      created_at: ago(4)
    },
    {
      notification_id: 'ntf_1',
      recipient: ACTOR,
      kind: 'mention',
      subject_type: 'case',
      subject_id: 'case_2',
      snippet: '@Ava escalate to SAR drafting if counterparty unverified',
      author: MARCUS,
      read: false,
      created_at: ago(6)
    },
    {
      notification_id: 'ntf_2',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_credit',
      snippet: 'Deployment request pending your approval (credit v3 → production)',
      author: PRIYA,
      read: false,
      created_at: ago(12)
    },
    {
      notification_id: 'ntf_3',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_aml',
      snippet: 'AML v3 → production awaiting four-eyes approval',
      author: DIEGO,
      read: false,
      created_at: ago(8)
    },
    {
      notification_id: 'ntf_4',
      recipient: ACTOR,
      kind: 'monitor',
      subject_type: 'flow',
      subject_id: 'flow_aml',
      snippet: 'Monitor firing: SAR referral rate above 25%',
      author: 'system',
      read: false,
      created_at: ago(3)
    },
    {
      notification_id: 'ntf_5',
      recipient: ACTOR,
      kind: 'monitor',
      subject_type: 'flow',
      subject_id: 'flow_credit',
      snippet: 'Monitor firing: decision failure rate above 5% (bureau timeouts)',
      author: 'system',
      read: true,
      created_at: ago(5)
    },
    {
      notification_id: 'ntf_6',
      recipient: ACTOR,
      kind: 'sla',
      subject_type: 'case',
      subject_id: 'case_3',
      snippet: 'KYC review breached its SLA',
      author: 'system',
      read: false,
      created_at: ago(4)
    },
    {
      notification_id: 'ntf_7',
      recipient: ACTOR,
      kind: 'comment',
      subject_type: 'decision',
      subject_id: 'dec_2',
      snippet: 'Lena left a compliance note on this decision',
      author: LENA,
      read: true,
      created_at: ago(18)
    },
    {
      notification_id: 'ntf_8',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_claim',
      snippet: 'Claim triage v2 → production approved by Marcus',
      author: MARCUS,
      read: true,
      created_at: ago(121)
    },
    {
      notification_id: 'ntf_9',
      recipient: ACTOR,
      kind: 'mention',
      subject_type: 'policy',
      subject_id: 'pol_credit',
      snippet: '@Ava Reg B wording on the DTI reason — see the policy thread',
      author: LENA,
      read: false,
      created_at: ago(22)
    }
  ];
}

export function seedApiKeys(): ManagedApiKey[] {
  return [
    {
      id: 'key_1',
      name: 'Production server',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-prod@intraktible.dev' },
      scope: 'production',
      role: 'editor',
      created_at: ago(2000)
    },
    {
      id: 'key_2',
      name: 'CI sandbox',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-ci@intraktible.dev' },
      scope: 'sandbox',
      role: 'operator',
      created_at: ago(1000),
      expires_at: ahead(90)
    },
    {
      id: 'key_3',
      name: 'Analytics read-only',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-bi@intraktible.dev' },
      scope: '*',
      role: 'viewer',
      created_at: ago(700),
      rotated_at: ago(120)
    },
    {
      id: 'key_4',
      name: 'Decommissioned partner',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-partner@intraktible.dev' },
      scope: 'production',
      role: 'operator',
      created_at: ago(3000),
      revoked_at: ago(300)
    }
  ];
}

export function seedGrants(): Map<string, FlowGrant[]> {
  return new Map([
    [
      'flow_credit',
      [
        {
          grant_id: 'g_c1',
          flow_id: 'flow_credit',
          actor: DIEGO,
          environment: 'sandbox',
          created_by: AVA,
          created_at: ago(100)
        },
        {
          grant_id: 'g_c2',
          flow_id: 'flow_credit',
          actor: PRIYA,
          environment: '*',
          created_by: AVA,
          created_at: ago(180)
        }
      ]
    ],
    [
      'flow_aml',
      [
        {
          grant_id: 'g_a1',
          flow_id: 'flow_aml',
          actor: DIEGO,
          environment: 'staging',
          created_by: AVA,
          created_at: ago(60)
        }
      ]
    ],
    [
      'flow_fraud',
      [
        {
          grant_id: 'g_f1',
          flow_id: 'flow_fraud',
          actor: PRIYA,
          environment: '*',
          created_by: AVA,
          created_at: ago(40)
        }
      ]
    ],
    [
      'flow_payout',
      [
        {
          grant_id: 'g_p1',
          flow_id: 'flow_payout',
          actor: DIEGO,
          environment: 'staging',
          created_by: AVA,
          created_at: ago(50)
        }
      ]
    ]
  ]);
}

export function seedSchedules(): Map<string, ScheduledDeploy[]> {
  return new Map([
    [
      'flow_credit',
      [
        {
          schedule_id: 'sch_c1',
          flow_id: 'flow_credit',
          environment: 'staging',
          version: 3,
          at: ahead(2),
          status: 'pending',
          prior_version: 2,
          created_at: ago(10)
        }
      ]
    ],
    [
      'flow_fraud',
      [
        {
          schedule_id: 'sch_f1',
          flow_id: 'flow_fraud',
          environment: 'sandbox',
          version: 4,
          at: ahead(1),
          until: ahead(5),
          status: 'pending',
          prior_version: 3,
          created_at: ago(6)
        }
      ]
    ],
    [
      'flow_collections',
      [
        {
          schedule_id: 'sch_h1',
          flow_id: 'flow_collections',
          environment: 'production',
          version: 2,
          at: ago(500),
          status: 'canceled',
          prior_version: 1,
          created_at: ago(520)
        }
      ]
    ]
  ]);
}

export interface CommentRec {
  comment_id: string;
  subject_type: string;
  subject_id: string;
  body: string;
  parent_id?: string;
  author: string;
  at: string;
}

export function seedComments(decisions: Decision[]): Map<string, CommentRec[]> {
  const m = new Map<string, CommentRec[]>();
  let n = 0;
  const thread = (
    type: string,
    subject: string,
    comments: { body: string; author: string; hrs: number; reply?: boolean }[]
  ): void => {
    const list: CommentRec[] = [];
    // Replies thread under the latest TOP-LEVEL comment — the single nesting level
    // the comment UI itself produces (it only offers Reply on top-level comments).
    let lastTop: string | undefined;
    for (const cm of comments) {
      n += 1;
      const id = `cmt_${n}`;
      list.push({
        comment_id: id,
        subject_type: type,
        subject_id: subject,
        body: cm.body,
        parent_id: cm.reply ? lastTop : undefined,
        author: cm.author,
        at: ago(cm.hrs)
      });
      if (!cm.reply) lastTop = id;
    }
    m.set(`${type}/${subject}`, list);
  };
  // The suspended decisions each carry the reviewer's note on what the resume is
  // waiting on — located from the seeded decisions so the ids can never go stale.
  const suspendedOf = (slug: string): string => {
    const d = decisions.find((x) => x.slug === slug && x.status === 'suspended');
    if (!d) throw new Error(`seed: no suspended decision for ${slug}`);
    return d.decision_id;
  };

  thread('decision', 'dec_2', [
    { body: 'Counterparty KYC is stale — flagging for compliance.', author: LENA, hrs: 18 },
    { body: 'Agreed, holding the wire pending refresh.', author: DIEGO, hrs: 16, reply: true }
  ]);
  thread('decision', suspendedOf('credit-decision'), [
    {
      body: 'Paused at the underwriter review step — waiting on income verification (two recent pay stubs) before I resume; risk sits in the 35–70 refer band.',
      author: DIEGO,
      hrs: 1
    }
  ]);
  thread('decision', suspendedOf('kyc-onboarding'), [
    {
      body: 'Holding at the EDD review — waiting on the certified translation of the registry extract before resuming (identity confidence is below the 60 clearing bar without it).',
      author: DIEGO,
      hrs: 3
    }
  ]);
  thread('decision', suspendedOf('payout-risk'), [
    {
      body: 'Paused at payout ops review — waiting on shipping manifests to reconcile the volume spike before releasing the funds.',
      author: DIEGO,
      hrs: 2
    }
  ]);
  thread('case', 'case_2', [
    { body: 'SAR draft started; will attach the narrative agent output.', author: DIEGO, hrs: 5 },
    { body: 'Loop me in before filing.', author: MARCUS, hrs: 4, reply: true }
  ]);
  // Flow discussions: multi-participant threads grounded in each flow's actual
  // mechanics (versions, reason codes, thresholds), so the Discussion tab reads
  // like the team that shipped the graphs.
  thread('flow', 'flow_credit', [
    {
      body: 'v3 adds the live bureau pull + Reg B reason codes — please review before prod.',
      author: PRIYA,
      hrs: 14
    },
    {
      body: 'Reviewing. The experian connector timed out five times this week — what happens to the run when the pull fails mid-flow?',
      author: MARCUS,
      hrs: 11,
      reply: true
    },
    {
      body: 'The run fails loudly (no silent default score) and the failure-rate monitor catches it — that is the mon_c1 firing you see.',
      author: PRIYA,
      hrs: 10,
      reply: true
    },
    {
      body: 'Confirming the adverse-action wording: DTI_TOO_HIGH must cite the ratio, not the band label — Reg B wants the specific reason.',
      author: LENA,
      hrs: 8
    }
  ]);
  thread('flow', 'flow_aml', [
    {
      body: 'v3 catches sub-threshold structuring (5+ deposits under $10k in 30 days) that v2 clears — the staging challenger exists to measure exactly that gap.',
      author: PRIYA,
      hrs: 19
    },
    {
      body: 'Two of the referrals it added were payroll batches (Hooli again). Can the code node exempt whitelisted recurring corridors before we promote?',
      author: DIEGO,
      hrs: 16,
      reply: true
    },
    {
      body: 'That is what the TXN-9920 pre-approval is for — honored corridors skip the flow entirely, so the heuristic never sees them.',
      author: PRIYA,
      hrs: 15,
      reply: true
    },
    {
      body: 'SAR narrative node pushes p50 latency over the 200ms monitor — expected, the AI call dominates. Alert threshold stays as a forcing function.',
      author: MARCUS,
      hrs: 12
    }
  ]);
  thread('flow', 'flow_fraud', [
    {
      body: 'v4 runs as a 15% production challenger — same graph, review band tightened to 35 (v3 kept 40 after the Q2 loss review only moved the policy).',
      author: PRIYA,
      hrs: 30
    },
    {
      body: 'Block rate is holding under the 15% monitor so far; watching FRAUD_REVIEW referral volume before we widen the arm.',
      author: DIEGO,
      hrs: 22,
      reply: true
    }
  ]);
  thread('flow', 'flow_dispute', [
    {
      body: 'v2 adds the reason-code liability table: 10.4 card-absent fraud auto-assigns issuer liability, quality goes to representment scoring.',
      author: DIEGO,
      hrs: 36
    },
    {
      body: 'Chargeback season has the refer rate way above the captured baseline — the drift panel is genuinely red, not noise. Baseline recapture after the season?',
      author: PRIYA,
      hrs: 28,
      reply: true
    },
    {
      body: 'Keep the old baseline until the season ends — recapturing now would normalize the anomaly away.',
      author: LENA,
      hrs: 24,
      reply: true
    }
  ]);
  thread('flow', 'flow_payout', [
    {
      body: 'Matrix auto-releases medium-risk small payouts now — watch the chargeback cohort for two weeks.',
      author: PRIYA,
      hrs: 50
    },
    {
      body: 'Cohort is clean so far (hold rate under the 20% monitor). The 250ms latency breach is the core-banking ledger read, not the matrix.',
      author: DIEGO,
      hrs: 26,
      reply: true
    },
    {
      body: 'MER-4515 fast-lane pre-approval expired last quarter — renewal review is on me before we re-grant the $25k auto-release cap.',
      author: MARCUS,
      hrs: 20
    }
  ]);
  thread('policy', 'pol_credit', [
    {
      body: 'Reg B requires specific reasons — LOW_SCORE alone is too generic when DTI drove the decline.',
      author: LENA,
      hrs: 26
    },
    {
      body: 'v2 orders DTI_TOO_HIGH ahead of the band label for exactly that.',
      author: MARCUS,
      hrs: 24,
      reply: true
    }
  ]);
  thread('policy', 'pol_claim', [
    {
      body: 'Fast-track cap stays at $200 until the abuse model clears its drift review.',
      author: MARCUS,
      hrs: 100
    },
    { body: 'Drift review scheduled — see the MRM row.', author: PRIYA, hrs: 96, reply: true }
  ]);
  // Deployment-request approval discussions: the decided requests carry the
  // exchange that led to the outcome; one pending request holds an open question.
  thread('deployment_request', 'req_c0', [
    {
      body: 'Backtest replayed 400 production decisions through v2 — dispositions match v1 except the intended band shift at risk 30→35.',
      author: PRIYA,
      hrs: 199
    },
    {
      body: 'Parity report looks right and the reason codes are Reg B-clean. Approving.',
      author: MARCUS,
      hrs: 196,
      reply: true
    }
  ]);
  thread('deployment_request', 'req_c1', [
    {
      body: 'Before I approve: the experian pull failed 5 times this week in staging. What is the blast radius in prod if the bureau has another bad day?',
      author: MARCUS,
      hrs: 10
    }
  ]);
  thread('deployment_request', 'req_f0', [
    {
      body: 'Q2 losses concentrated in the 35–40 score band v2 was approving — tightening the review band to 35 catches them.',
      author: PRIYA,
      hrs: 78
    },
    {
      body: 'Referral volume impact is within what the fraud queue can absorb. Approved.',
      author: MARCUS,
      hrs: 74,
      reply: true
    }
  ]);
  thread('deployment_request', 'req_cl0', [
    {
      body: 'Staging backtest: v2 refers 9% more claims than v1. That is the CLAIM_FRAUD_REVIEW band at 55 pulling in seasoned single-claim customers — adjusters cannot absorb it.',
      author: MARCUS,
      hrs: 158
    },
    {
      body: 'The extra referrals are exactly the repeat-claimant cohort the abuse model flags — is the volume really the problem?',
      author: PRIYA,
      hrs: 157,
      reply: true
    },
    {
      body: 'Half of them are first claims on mature policies — false positives. Rejecting; re-tune the fraud band and resubmit.',
      author: MARCUS,
      hrs: 156,
      reply: true
    }
  ]);
  thread('deployment_request', 'req_cl1', [
    {
      body: 'Fraud band re-tuned to 60: the mature-policy first claims drop out, referral delta is +2% and it is the repeat-claimant cohort we want reviewed.',
      author: PRIYA,
      hrs: 125
    },
    {
      body: 'That matches the abuse-model intent. Approved — keep the $200 fast-track cap until the drift review clears.',
      author: MARCUS,
      hrs: 121,
      reply: true
    }
  ]);
  return m;
}

// --- Audit trail -----------------------------------------------------------------

// A believable workspace timeline across the roster and ~30 days, on the real
// event taxonomy (stream → type) with resource ids in the payload — exactly how a
// real deployment's log reads, so the Audit UI's filters work the same way.
const AUDIT_TEMPLATES: {
  actor: string;
  stream: string;
  type: string;
  payload: Record<string, unknown>;
}[] = [
  {
    actor: AVA,
    stream: 'auth',
    type: 'auth.managed_key.created',
    payload: { key_id: 'key_1', name: 'Production server' }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_credit', version: 3 }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.deployment_requested',
    payload: { flow_id: 'flow_credit', environment: 'production', version: 3 }
  },
  {
    actor: MARCUS,
    stream: 'decision.flows',
    type: 'decision.flow.deployment_approved',
    payload: { flow_id: 'flow_claim', environment: 'production', version: 2 }
  },
  {
    actor: MARCUS,
    stream: 'decision.flows',
    type: 'decision.flow.deployment_rejected',
    payload: { flow_id: 'flow_claim', environment: 'production', version: 2 }
  },
  {
    actor: AVA,
    stream: 'decision.flows',
    type: 'decision.flow.version_deployed',
    payload: { flow_id: 'flow_credit', environment: 'staging', version: 3 }
  },
  {
    actor: MARCUS,
    stream: 'decision.policies',
    type: 'decision.policy.version_published',
    payload: { policy_id: 'pol_credit', version: 2 }
  },
  {
    actor: MARCUS,
    stream: 'decision.policies',
    type: 'decision.policy.version_published',
    payload: { policy_id: 'pol_aml', version: 2 }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_aml', version: 3 }
  },
  {
    actor: DIEGO,
    stream: 'decision.flows',
    type: 'decision.flow.deployment_requested',
    payload: { flow_id: 'flow_aml', environment: 'production', version: 3 }
  },
  {
    actor: 'system',
    stream: 'decision.monitors',
    type: 'decision.monitor_alerted',
    payload: { monitor_id: 'mon_a2', flow_id: 'flow_aml', metric: 'refer_rate' }
  },
  { actor: DIEGO, stream: 'cases', type: 'cases.note_added', payload: { case_id: 'case_2' } },
  {
    actor: AVA,
    stream: 'cases',
    type: 'cases.assigned',
    payload: { case_id: 'case_2', assignee: DIEGO }
  },
  {
    actor: 'system',
    stream: 'cases',
    type: 'cases.sla_breached',
    payload: { case_id: 'case_3' }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_fraud', version: 4 }
  },
  {
    actor: AVA,
    stream: 'decision.flows',
    type: 'decision.flow.shadow_set',
    payload: { flow_id: 'flow_payout', environment: 'staging', version: 1 }
  },
  {
    actor: MARCUS,
    stream: 'decision.models',
    type: 'decision.model.baseline_captured',
    payload: { model: 'credit_pd' }
  },
  {
    actor: AVA,
    stream: 'decision.models',
    type: 'decision.model.monitor_set',
    payload: { model: 'claim_fraud', threshold: 0.1 }
  },
  {
    actor: 'system',
    stream: 'decision.monitors',
    type: 'decision.monitor_alerted',
    payload: { monitor_id: 'mon_c1', flow_id: 'flow_credit', metric: 'failure_rate' }
  },
  {
    actor: 'system',
    stream: 'decision.monitors',
    type: 'decision.monitor_alerted',
    payload: { monitor_id: 'mon_m1', flow_id: 'flow_merchant', metric: 'volume' }
  },
  {
    actor: DIEGO,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_dispute', version: 2 }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_collections', version: 2 }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_payout', version: 2 }
  },
  {
    actor: PRIYA,
    stream: 'decision.flows',
    type: 'decision.flow.version_published',
    payload: { flow_id: 'flow_limit', version: 1 }
  },
  {
    actor: PRIYA,
    stream: 'decision.agents',
    type: 'decision.agent.version_published',
    payload: { agent: 'aml-narrative', version: 3 }
  },
  {
    actor: MARCUS,
    stream: 'decision.preapprovals',
    type: 'decision.preapproval.granted',
    payload: { preapproval_id: 'pa_4', entity_type: 'merchant', entity_id: 'MER-4400' }
  },
  {
    actor: AVA,
    stream: 'decision.preapprovals',
    type: 'decision.preapproval.revoked',
    payload: { preapproval_id: 'pa_8', reason: 'Document reverification failed' }
  },
  {
    actor: AVA,
    stream: 'decision.flows',
    type: 'decision.flow.deploy_scheduled',
    payload: { flow_id: 'flow_credit', environment: 'staging', version: 3 }
  },
  {
    actor: DIEGO,
    stream: 'cases',
    type: 'cases.resolved',
    payload: { case_id: 'case_9', resolution: 'cleared' }
  },
  {
    actor: AVA,
    stream: 'auth',
    type: 'auth.managed_key.rotated',
    payload: { key_id: 'key_3', name: 'Analytics read-only' }
  },
  {
    actor: AVA,
    stream: 'auth',
    type: 'auth.managed_key.revoked',
    payload: { key_id: 'key_4', name: 'Decommissioned partner' }
  },
  {
    actor: PRIYA,
    stream: 'decision.models',
    type: 'decision.model.defined',
    payload: { model: 'claim_fraud', kind: 'gbm' }
  }
];

export function seedAudit(decisions: Decision[]): AuditEntry[] {
  const events: {
    time: string;
    actor: string;
    stream: string;
    type: string;
    payload: unknown;
  }[] = [];
  const total = 110;
  for (let i = 0; i < total; i++) {
    // ~30 days of workspace history, slightly jittered so rows don't grid-align.
    events.push({ time: ago(i * 6.4 + (i % 5)), ...AUDIT_TEMPLATES[i % AUDIT_TEMPLATES.length] });
  }
  // Mirror the run journal (started / node_evaluated per step / terminal) for the most
  // recent seeded decisions, so the raw log opens dominated by node steps — the real
  // event-log noise the audit page's "Hide node steps" toggle exists for.
  const recent = [...decisions]
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .slice(0, 6);
  for (const d of recent) {
    const actor =
      d.environment === 'production' ? 'svc-prod@intraktible.dev' : 'svc-ci@intraktible.dev';
    events.push({
      time: d.started_at,
      actor,
      stream: 'decision.runs',
      type: 'decision.run.started',
      payload: { decision_id: d.decision_id, flow_id: d.flow_id, environment: d.environment }
    });
    for (const n of d.nodes ?? []) {
      events.push({
        time: d.started_at,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.node_evaluated',
        payload: { decision_id: d.decision_id, node_id: n.node_id, node_type: n.type }
      });
    }
    if (d.case_id) {
      events.push({
        time: d.started_at,
        actor: 'system',
        stream: 'decision.runs',
        type: 'decision.manual_review_requested',
        payload: { decision_id: d.decision_id, case_id: d.case_id }
      });
    }
    const ended = d.ended_at ?? d.started_at;
    if (d.status === 'completed') {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.completed',
        payload: { decision_id: d.decision_id, disposition: d.disposition }
      });
    } else if (d.status === 'failed') {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.failed',
        payload: { decision_id: d.decision_id, error: d.error }
      });
    } else {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.suspended',
        payload: { decision_id: d.decision_id }
      });
    }
  }
  // Chronological seq numbering, stored newest-first like the live log.
  events.sort((a, b) => a.time.localeCompare(b.time));
  return events.map((e, i) => ({ seq: i + 1, id: `aud_${i + 1}`, ...e })).reverse();
}

// SLOs with a deliberate meeting/breaching mix over the REAL seeded metrics:
// credit misses its success target (the bureau-outage failures), AML misses its
// latency target (SAR narratives), everything else holds.
export function seedFlowSlos(): Map<string, { success_target: number; latency_target_ms: number }> {
  return new Map([
    ['flow_credit', { success_target: 0.95, latency_target_ms: 400 }],
    ['flow_aml', { success_target: 0.97, latency_target_ms: 200 }],
    ['flow_fraud', { success_target: 0.99, latency_target_ms: 300 }],
    ['flow_kyc', { success_target: 0.92, latency_target_ms: 400 }],
    ['flow_dispute', { success_target: 0.95, latency_target_ms: 300 }],
    ['flow_collections', { success_target: 0.9, latency_target_ms: 400 }],
    ['flow_claim', { success_target: 0.92, latency_target_ms: 300 }],
    ['flow_payout', { success_target: 0.9, latency_target_ms: 250 }]
  ]);
}

// FlowBaseline mirrors the real engine's monitor.Baseline: a captured disposition
// distribution as SHARES summing to ~1, plus the decision count at capture time.
export interface FlowBaseline {
  approve: number;
  decline: number;
  refer: number;
  total: number;
}

// Captured disposition baselines: shares close to the seeded mixes (small PSI)
// except dispute, whose refer-heavy chargeback season has genuinely drifted from
// the captured baseline.
export function seedFlowBaselines(): Map<string, FlowBaseline> {
  return new Map([
    ['flow_credit', { approve: 0.566, decline: 0.17, refer: 0.264, total: 53 }],
    ['flow_aml', { approve: 0.671, decline: 0, refer: 0.329, total: 82 }],
    ['flow_fraud', { approve: 0.703, decline: 0.099, refer: 0.198, total: 111 }],
    ['flow_dispute', { approve: 0.667, decline: 0, refer: 0.333, total: 42 }],
    ['flow_payout', { approve: 0.654, decline: 0.115, refer: 0.231, total: 26 }]
  ]);
}
