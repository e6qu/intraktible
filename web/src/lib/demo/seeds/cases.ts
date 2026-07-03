// SPDX-License-Identifier: AGPL-3.0-or-later
// Case queue seed: ~30 review cases spanning every case type the fleet's
// manual_review nodes open, every status and SLA state, with assignees across the
// roster and note/audit timelines. Every case links a REAL source decision — the
// n-th referred (or suspended) decision of its flow that is older than the case —
// and its context facts are read off that decision's actual record, so the case
// detail and the decision trace can never disagree.

import type { Case, Decision } from '$lib/api';
import { ago, now, AVA, MARCUS, PRIYA, DIEGO } from './base';

interface CaseSeed {
  name: string;
  type: string;
  slug: string;
  status: Case['status'];
  assignee?: string;
  slaDays: number;
  daysLeft: number;
  slaState: Case['sla_state'];
  breached?: boolean; // append the SLA-breach audit row (the "breached" story)
  suspended?: boolean; // link the flow's suspended decision instead of a referral
  extra?: Record<string, unknown>; // literal facts the decision record can't carry
  notes: { author: string; text: string; hrs: number }[];
  createdHrs: number;
  updatedHrs: number;
  resolvedAs?: string;
}

const SEEDS: CaseSeed[] = [
  // case_1..case_3 anchor the seeded notifications (assigned / due soon / overdue).
  {
    name: 'Northwind Capital',
    type: 'credit_review',
    slug: 'credit-decision',
    status: 'needs_review',
    slaDays: 3,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { segment: 'SMB', exposure_usd: 45000 },
    notes: [
      { author: DIEGO, text: 'Requested two recent pay stubs and bank statements.', hrs: 20 }
    ],
    createdHrs: 48,
    updatedHrs: 20
  },
  {
    name: 'Acme Imports LLC',
    type: 'aml_alert',
    slug: 'aml-screening',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 5,
    daysLeft: 1,
    slaState: 'due_soon',
    extra: { counterparty: 'Cayman holding entity' },
    notes: [
      {
        author: DIEGO,
        text: 'Cross-border wire to a high-risk jurisdiction; pulling counterparty KYC.',
        hrs: 30
      },
      { author: MARCUS, text: 'Escalate to SAR drafting if counterparty unverified.', hrs: 6 }
    ],
    createdHrs: 70,
    updatedHrs: 6
  },
  {
    name: 'Globex Lending',
    type: 'kyc_review',
    slug: 'kyc-onboarding',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 2,
    daysLeft: -1,
    slaState: 'overdue',
    breached: true,
    notes: [
      {
        author: DIEGO,
        text: 'PEP match on a beneficial owner; awaiting adverse-media disposition.',
        hrs: 54
      }
    ],
    createdHrs: 96,
    updatedHrs: 4
  },
  {
    name: 'Initech Finance',
    type: 'credit_review',
    slug: 'credit-decision',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 3,
    daysLeft: 1,
    slaState: 'on_track',
    extra: { decision: 'approved with reduced limit' },
    notes: [{ author: DIEGO, text: 'Approved at $18k limit after income verification.', hrs: 12 }],
    createdHrs: 60,
    updatedHrs: 12,
    resolvedAs: 'approved'
  },
  {
    name: 'Umbrella Card 4821',
    type: 'fraud_review',
    slug: 'card-fraud',
    status: 'needs_review',
    slaDays: 1,
    daysLeft: 1,
    slaState: 'on_track',
    notes: [],
    createdHrs: 8,
    updatedHrs: 8
  },
  {
    name: 'Soylent Merchant Co',
    type: 'merchant_review',
    slug: 'merchant-onboarding',
    status: 'in_progress',
    assignee: MARCUS,
    slaDays: 4,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { mcc: '7995 (gambling)' },
    notes: [
      {
        author: MARCUS,
        text: 'High-risk MCC; requesting processing history and chargeback ratios.',
        hrs: 18
      }
    ],
    createdHrs: 40,
    updatedHrs: 18
  },
  {
    name: 'Wayne Disputes #5512',
    type: 'dispute',
    slug: 'dispute-triage',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 7,
    daysLeft: 4,
    slaState: 'on_track',
    extra: { recommendation: 'representment' },
    notes: [
      {
        author: DIEGO,
        text: 'Compelling evidence on file; preparing representment package.',
        hrs: 26
      }
    ],
    createdHrs: 36,
    updatedHrs: 26
  },
  {
    name: 'Stark Industries',
    type: 'credit_review',
    slug: 'credit-decision',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 3,
    daysLeft: 0,
    slaState: 'due_soon',
    extra: { segment: 'corporate' },
    notes: [{ author: DIEGO, text: 'Awaiting guarantor financials.', hrs: 14 }],
    createdHrs: 50,
    updatedHrs: 14
  },
  {
    name: 'Hooli Payments',
    type: 'aml_alert',
    slug: 'aml-screening',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 5,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { outcome: 'no SAR — false positive' },
    notes: [
      { author: DIEGO, text: 'Structuring pattern explained by payroll batch; cleared.', hrs: 90 }
    ],
    createdHrs: 140,
    updatedHrs: 90,
    resolvedAs: 'cleared'
  },
  {
    name: 'Pied Piper Card 9913',
    type: 'fraud_review',
    slug: 'card-fraud',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 1,
    daysLeft: 0,
    slaState: 'on_track',
    extra: { outcome: 'confirmed fraud — card blocked' },
    notes: [{ author: DIEGO, text: 'Account takeover confirmed; card reissued.', hrs: 110 }],
    createdHrs: 118,
    updatedHrs: 110,
    resolvedAs: 'blocked'
  },
  {
    name: 'Cyberdyne Onboarding',
    type: 'kyc_review',
    slug: 'kyc-onboarding',
    status: 'needs_review',
    slaDays: 2,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { doc_quality_note: 'low-resolution scan' },
    notes: [],
    createdHrs: 10,
    updatedHrs: 10
  },
  {
    name: 'Tyrell Merchant',
    type: 'merchant_review',
    slug: 'merchant-onboarding',
    status: 'needs_review',
    slaDays: 4,
    daysLeft: -2,
    slaState: 'overdue',
    breached: true,
    extra: { mcc: '6051 (crypto)' },
    notes: [
      {
        author: MARCUS,
        text: 'Crypto MCC requires enhanced underwriting; chasing licensing docs.',
        hrs: 100
      }
    ],
    createdHrs: 150,
    updatedHrs: 6
  },
  {
    name: 'Oscorp Disputes #7740',
    type: 'dispute',
    slug: 'dispute-triage',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 7,
    daysLeft: 3,
    slaState: 'on_track',
    extra: { outcome: 'refunded' },
    notes: [{ author: DIEGO, text: 'Low value, product-not-received; refunded.', hrs: 160 }],
    createdHrs: 180,
    updatedHrs: 160,
    resolvedAs: 'refund'
  },
  {
    name: 'Aperture Capital',
    type: 'credit_review',
    slug: 'credit-decision',
    status: 'needs_review',
    slaDays: 3,
    daysLeft: 3,
    slaState: 'on_track',
    extra: { segment: 'SMB' },
    notes: [],
    createdHrs: 5,
    updatedHrs: 5
  },
  {
    name: 'Vandelay Industries',
    type: 'hardship_review',
    slug: 'collections-hardship',
    status: 'in_progress',
    assignee: PRIYA,
    slaDays: 5,
    daysLeft: 3,
    slaState: 'on_track',
    extra: { program: '12-month plan, 50% rate relief' },
    notes: [
      {
        author: PRIYA,
        text: 'Concession above my authority band — needs supervisor countersign.',
        hrs: 22
      }
    ],
    createdHrs: 44,
    updatedHrs: 22
  },
  {
    name: 'Bluth Household',
    type: 'hardship_review',
    slug: 'collections-hardship',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 5,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { outcome: 'enrolled — 12-month plan' },
    notes: [
      {
        author: DIEGO,
        text: 'Medical hardship documented; plan countersigned by Marcus.',
        hrs: 130
      }
    ],
    createdHrs: 170,
    updatedHrs: 130,
    resolvedAs: 'enrolled'
  },
  {
    name: 'Claim CLM-2214 · Okafor',
    type: 'claim_review',
    slug: 'claim-triage',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 3,
    daysLeft: 1,
    slaState: 'due_soon',
    extra: { item: 'laptop — theft' },
    notes: [
      {
        author: DIEGO,
        text: 'Police report attached; verifying purchase date vs policy start.',
        hrs: 9
      }
    ],
    createdHrs: 28,
    updatedHrs: 9
  },
  {
    name: 'Claim CLM-2190 · Marchetti',
    type: 'claim_review',
    slug: 'claim-triage',
    status: 'needs_review',
    slaDays: 3,
    daysLeft: -1,
    slaState: 'overdue',
    breached: true,
    extra: { item: 'phone — repeat claimant' },
    notes: [],
    createdHrs: 100,
    updatedHrs: 7
  },
  {
    name: 'Claim CLM-2145 · Watanabe',
    type: 'claim_review',
    slug: 'claim-triage',
    status: 'completed',
    assignee: MARCUS,
    slaDays: 3,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { outcome: 'paid at coverage limit' },
    notes: [{ author: MARCUS, text: 'Severity high but documentation clean; paid.', hrs: 200 }],
    createdHrs: 240,
    updatedHrs: 200,
    resolvedAs: 'paid'
  },
  {
    name: 'Hooli Marketplace Payout',
    type: 'payout_review',
    slug: 'payout-risk',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 2,
    daysLeft: 1,
    slaState: 'due_soon',
    extra: { seller_tenure: '3 years' },
    notes: [
      {
        author: DIEGO,
        text: 'Volume spike matches their holiday sale; verifying inventory.',
        hrs: 11
      }
    ],
    createdHrs: 26,
    updatedHrs: 11
  },
  {
    name: 'Wayne Home Goods Payout',
    type: 'payout_review',
    slug: 'payout-risk',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 2,
    daysLeft: 1,
    slaState: 'on_track',
    extra: { outcome: 'released after inventory check' },
    notes: [{ author: DIEGO, text: 'Shipping manifests reconcile; released.', hrs: 150 }],
    createdHrs: 190,
    updatedHrs: 150,
    resolvedAs: 'released'
  },
  {
    name: 'CLI · Card 7719',
    type: 'limit_review',
    slug: 'limit-increase',
    status: 'needs_review',
    slaDays: 2,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { requested_increase: '+50%' },
    notes: [],
    createdHrs: 15,
    updatedHrs: 15
  },
  {
    name: 'Massive Dynamic',
    type: 'aml_alert',
    slug: 'aml-screening',
    status: 'needs_review',
    slaDays: 5,
    daysLeft: 4,
    slaState: 'on_track',
    extra: { pattern: 'sub-threshold structuring' },
    notes: [],
    createdHrs: 12,
    updatedHrs: 12
  },
  {
    name: 'Duff Distribution',
    type: 'aml_alert',
    slug: 'aml-screening',
    status: 'in_progress',
    assignee: MARCUS,
    slaDays: 5,
    daysLeft: 0,
    slaState: 'due_soon',
    extra: { watchlist: 'OFAC partial match' },
    notes: [
      {
        author: MARCUS,
        text: 'Name-only match; requesting DOB corroboration before filing.',
        hrs: 30
      }
    ],
    createdHrs: 110,
    updatedHrs: 30
  },
  {
    name: 'Sirius Cybernetics Card 5150',
    type: 'fraud_review',
    slug: 'card-fraud',
    status: 'in_progress',
    assignee: DIEGO,
    slaDays: 1,
    daysLeft: -1,
    slaState: 'overdue',
    breached: true,
    notes: [
      { author: DIEGO, text: 'Cardholder unreachable; second contact attempt logged.', hrs: 5 }
    ],
    createdHrs: 30,
    updatedHrs: 5
  },
  {
    name: 'Gringotts Onboarding',
    type: 'kyc_review',
    slug: 'kyc-onboarding',
    status: 'completed',
    assignee: DIEGO,
    slaDays: 2,
    daysLeft: 1,
    slaState: 'on_track',
    extra: { outcome: 'verified after EDD' },
    notes: [{ author: DIEGO, text: 'Source-of-funds letter satisfies EDD; verified.', hrs: 210 }],
    createdHrs: 260,
    updatedHrs: 210,
    resolvedAs: 'verified'
  },
  {
    name: 'Prestige Worldwide Disputes #8103',
    type: 'dispute',
    slug: 'dispute-triage',
    status: 'needs_review',
    slaDays: 7,
    daysLeft: 6,
    slaState: 'on_track',
    notes: [],
    createdHrs: 18,
    updatedHrs: 18
  },
  // The three suspended decisions each have a case a reviewer resumes them from.
  {
    name: 'Wonka Credit Application',
    type: 'credit_review',
    slug: 'credit-decision',
    status: 'needs_review',
    suspended: true,
    slaDays: 3,
    daysLeft: 3,
    slaState: 'on_track',
    extra: { note: 'decision paused at underwriter review — resume from the trace' },
    notes: [],
    createdHrs: 2,
    updatedHrs: 2
  },
  {
    name: 'Ollivanders Onboarding',
    type: 'kyc_review',
    slug: 'kyc-onboarding',
    status: 'in_progress',
    assignee: DIEGO,
    suspended: true,
    slaDays: 2,
    daysLeft: 1,
    slaState: 'due_soon',
    extra: { note: 'decision paused at EDD review — resume from the trace' },
    notes: [
      { author: DIEGO, text: 'Requested certified translation of the registry extract.', hrs: 3 }
    ],
    createdHrs: 6,
    updatedHrs: 3
  },
  {
    name: 'Umbrella Wellness Payout',
    type: 'payout_review',
    slug: 'payout-risk',
    status: 'needs_review',
    suspended: true,
    slaDays: 2,
    daysLeft: 2,
    slaState: 'on_track',
    extra: { note: 'decision paused at payout ops review — resume from the trace' },
    notes: [],
    createdHrs: 4,
    updatedHrs: 4
  }
];

function round(v: unknown, digits = 0): number | undefined {
  const n = Number(v);
  if (!Number.isFinite(n)) return undefined;
  const f = 10 ** digits;
  return Math.round(n * f) / f;
}

// caseContext reads the flat fact grid the case detail renders OFF the source
// decision's actual record, per case type.
function caseContext(type: string, d: Decision): Record<string, unknown> {
  const data = (d.data as Record<string, unknown>) ?? {};
  switch (type) {
    case 'credit_review':
      return {
        risk: round(data.risk),
        fico_score: data.fico_score,
        dti: round(data.dti, 2),
        offered_limit: round(data.offered_limit)
      };
    case 'aml_alert':
      return {
        aml_score: round(data.aml_score, 1),
        amount_usd: data.amount,
        corridor: `${String(data.origin_country)}→${String(data.dest_country)}`,
        watchlist_score: data.watchlist_score
      };
    case 'fraud_review':
      return {
        fraud_p: round(data.fraud_p),
        amount_usd: data.amount,
        tx_count_1h: data.tx_count_1h,
        device_score: data.device_score
      };
    case 'kyc_review':
      return {
        identity_conf: data.identity_conf,
        pep_flag: data.pep_flag,
        doc_quality: data.doc_quality
      };
    case 'dispute':
      return {
        amount_usd: data.amount,
        reason: data.reason_code,
        liability: data.liability,
        triage: data.triage
      };
    case 'merchant_review':
      return {
        uw_score: round(data.uw_score, 1),
        mcc_risk: data.mcc_risk,
        monthly_volume: data.monthly_volume
      };
    case 'hardship_review':
      return {
        hardship_score: data.hardship_score,
        plan_months: data.plan_months,
        rate_relief: data.rate_relief,
        balance_usd: data.balance_usd
      };
    case 'claim_review':
      return {
        fraud_p: round(data.fraud_p),
        amount_usd: data.amount,
        amount_ratio: round(data.amount_ratio, 2),
        prior_claims_24m: data.prior_claims_24m
      };
    case 'payout_review':
      return {
        payout_score: round(data.payout_score, 1),
        amount_usd: data.amount,
        payout_ratio: round(data.payout_ratio, 2),
        account_age_days: data.account_age_days
      };
    case 'limit_review':
      return {
        risk: round(data.risk),
        utilization: round(data.utilization, 2),
        fico_score: data.fico_score,
        proposed_limit: round(data.proposed_limit)
      };
    default:
      throw new Error(`seed: no context mapping for case type ${type}`);
  }
}

// seedCases links each case to the next unused referred decision of its flow that
// is OLDER than the case (a case opens after its decision), or the flow's
// suspended decision for the resume-flow cases. Throws when the pool runs dry — a
// seed sizing bug, never silently unlinked.
export function seedCases(decisions: Decision[]): Case[] {
  const used = new Set<string>();
  const hoursOf = (d: Decision): number => (now.getTime() - Date.parse(d.started_at)) / 3600_000;
  const pickReferred = (slug: string, minHours: number): Decision => {
    const found = decisions.find(
      (d) =>
        d.slug === slug &&
        d.status === 'completed' &&
        d.disposition === 'refer' &&
        !used.has(d.decision_id) &&
        hoursOf(d) >= minHours
    );
    if (!found)
      throw new Error(`seed: no unused referred decision for ${slug} older than ${minHours}h`);
    used.add(found.decision_id);
    return found;
  };
  const pickSuspended = (slug: string): Decision => {
    const found = decisions.find(
      (d) => d.slug === slug && d.status === 'suspended' && !used.has(d.decision_id)
    );
    if (!found) throw new Error(`seed: no suspended decision for ${slug}`);
    used.add(found.decision_id);
    return found;
  };

  return SEEDS.map((s, i) => {
    const src = s.suspended ? pickSuspended(s.slug) : pickReferred(s.slug, s.createdHrs);
    const audit: Case['audit'] = [
      {
        type: 'case.opened',
        actor: 'system',
        at: ago(s.createdHrs),
        detail: `from decision ${src.decision_id}`
      }
    ];
    if (s.assignee) {
      audit.push({
        type: 'case.assigned',
        actor: AVA,
        at: ago(Math.max(s.updatedHrs, s.createdHrs - 2)),
        detail: `to ${s.assignee}`
      });
    }
    for (const n of s.notes) {
      audit.push({ type: 'case.note', actor: n.author, at: ago(n.hrs) });
    }
    if (s.breached) {
      audit.push({
        type: 'case.sla_breached',
        actor: 'system',
        at: ago(s.updatedHrs),
        detail: 'SLA exceeded'
      });
    }
    if (s.resolvedAs) {
      audit.push({
        type: 'case.resolved',
        actor: s.assignee ?? AVA,
        at: ago(s.updatedHrs),
        detail: s.resolvedAs
      });
    }
    return {
      case_id: `case_${i + 1}`,
      company_name: s.name,
      case_type: s.type,
      status: s.status,
      assignee: s.assignee,
      sla_days: s.slaDays,
      days_left: s.daysLeft,
      sla_state: s.slaState,
      source_decision_id: src.decision_id,
      context: { ...caseContext(s.type, src), ...s.extra },
      notes: s.notes.map((n) => ({ author: n.author, text: n.text, at: ago(n.hrs) })),
      audit,
      created_at: ago(s.createdHrs),
      updated_at: ago(s.updatedHrs)
    };
  });
}
