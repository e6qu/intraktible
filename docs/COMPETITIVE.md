<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Competitive landscape — intraktible vs the field

Where intraktible sits relative to the decisioning market. Compiled from a multi-agent research sweep
of vendor sites, docs, and secondary sources.

**Read this with the following caveats.** The competitor columns record published vendor and marketing
claims plus secondary research — not independent testing. We have not run these products. A "✅" for a
competitor means the vendor advertises the capability, nothing more; several claims we could not
corroborate are marked accordingly. Only the intraktible column is grounded in the codebase in this
repo. The named competitors are also **different categories**, so a single overall ranking would be
misleading — the table is per-axis.

- **Taktile** — a decisioning platform whose published architecture is the same four components as this
  project (AI Agent Manager · Decision Engine · Case Manager · Context Layer). SaaS-only.
- **Alloy** — identity / KYC / KYB / fraud onboarding orchestration; credit underwriting is a secondary
  product that runs the customer's own rules. SaaS-only.
- **Zest AI** — ML underwriting models plus explainability, fair-lending tooling, and SR 11-7 docs. Not
  a flow engine; plugs into a lender's LOS.

Legend: ✅ present / first-class · 🟡 partial, shallow, or via a partner · ❌ not present · — not
applicable (different category). Competitor marks reflect vendor claims (see caveat above).

## Feature-by-feature

| Capability | intraktible | Taktile | Alloy | Zest AI |
|---|---|---|---|---|
| Deployment | ✅ OSS, self-host (AGPL) | ❌ SaaS-only (AWS) | ❌ SaaS-only (AWS) | ❌ API into the lender's LOS |
| Visual flow builder | ✅ 14 node types | ✅ | ✅ Workflows + Journeys | ❌ (lives in the LOS) |
| Code node | 🟡 Starlark (not Python) | ✅ Python (+ AI-generated) | 🟡 no-code focus | — |
| DMN-style decision tables | ✅ 5 hit policies + aggregation | ✅ | 🟡 matrix / rules | ❌ |
| AI / agentic | ✅ AI node + agent tool-calling | ✅ Copilot + Agent workbench | ✅ fraud-signal ML + AI agent | 🟡 genAI analytics (LuLu) |
| Data-connector marketplace | 🟡 ~9 types (bureau/sanctions/plaid/sql) | ✅ ~200 claimed | ✅ ~270 claimed (KYC/KYB/AML) | — |
| Model hosting (serve) | ✅ logistic/GBM/expr/external | ✅ Python endpoints | 🟡 import custom | ✅ (core product) |
| Model training in-platform | 🟡 logistic only | ❌ | ❌ | ✅ GBM/NN/ensembles (core product) |
| Explainability / adverse-action codes | 🟡 reason codes wired; no notice generation | 🟡 "white-box" claim; fair-lending via FairPlay | 🟡 audit trail | ✅ GIG method, FCRA/ECOA reason codes |
| Fair-lending / disparate-impact | ❌ | 🟡 via FairPlay partner | ❌ | ✅ FairBoost LDA search |
| Backtesting / simulation | ✅ incl. replay on recorded traffic | ✅ | ✅ backtest + what-if | 🟡 outcomes analysis (in docs) |
| Champion/challenger + shadow | ✅ both | ✅ C/C (shadow not confirmed) | ✅ up to 5 + shadow | ❌ |
| Versioning + maker-checker | ✅ flows (four-eyes); models bypass it | ✅ sign-off workflows | ✅ + rollback | 🟡 model lifecycle only |
| Drift monitoring | ✅ PSI + covariate + actuals | ❌ not claimed | 🟡 dashboards | ✅ input/output + fair-lending |
| Audit / lineage / replay | ✅ event-sourced deterministic replay | 🟡 audit-ready; a reviewer reports weak lineage | ✅ audit trail | 🟡 model docs |
| Case management / manual review | ✅ (case↔decision resume is manual) | ✅ Case Manager | ✅ + SAR/CTR e-file to FinCEN | ❌ |
| Feature store (point-in-time) | ✅ as_of + versioning + cache | 🟡 light (Tecton partner for heavy) | ❌ | ❌ |
| RBAC / SSO / SCIM | ✅ RBAC + OIDC + SAML + SCIM | ✅ (a reviewer reports role gaps) | ✅ RBAC + ABAC | — |
| SOC 2 / ISO certs | ❌ | ✅ SOC2 II + ISO 27001 | ✅ SOC2 II | ❌ not confirmed |
| Model-risk (SR 11-7) kit | 🟡 inventory report | 🟡 editorial | ❌ | ✅ Autodoc |
| Scale / references | ❌ single-node, no load evidence, no users | ✅ Mercury/Monzo/Allianz; $184M raised | ✅ ~800 FIs; $1.55B val | ✅ Citi/CUs; $319M raised |

## Per-competitor notes

- **Taktile.** Same published architecture, different delivery model (SaaS vs self-host). Taktile
  advertises a larger data marketplace, agentic tooling, SOC 2 / ISO, and named bank customers.
  intraktible records capabilities Taktile's docs do not claim: self-host, drift monitoring, and
  deterministic replay / decision lineage (one Taktile reviewer reports lineage and search as weak).
  These are marks in different columns, not a verdict.
- **Alloy.** Mostly a different category — identity/KYC/KYB/AML onboarding plus a ~270-source data
  ecosystem and SAR/CTR e-filing. intraktible does not have that depth or any data-source relationships.
  On the overlap (rules + testing + audit), Alloy advertises SOC 2 and a large install base; intraktible
  does not. intraktible's distinct marks are self-host and event-sourced replay.
- **Zest.** Complementary rather than competing. Zest covers the axes intraktible does not: model
  training, FCRA/ECOA adverse-action reason codes (GIG), fair-lending (FairBoost), and SR 11-7 docs
  (Autodoc). In a deployment, a Zest model could be the model inside an intraktible flow.
- **Enterprise incumbents (FICO / SAS / IBM ODM / Provenir).** Long track records, self-managed
  deployment, many references. intraktible's difference is being open-source and self-hostable; it has
  none of the track record.

## Open-source landscape

**Rules / decision engines (self-hostable):** Drools / Apache KIE (Apache 2.0; the authoring UI is
deprecated and the supported distribution is now the commercial IBM BAMOE); GoRules Zen (MIT, Rust — a
modern engine whose versioning/governance layer is a paid BRMS, not in the open engine); Camunda DMN
(Apache 2.0, but C7 CE reached EOL Oct 2025 and Camunda 8 self-managed requires a paid production
licence); Flowable/Activiti; grule (Go), NRules, Clara, json-rules-engine, OpenL Tablets, CLIPS. OPA
(CNCF) is a general policy engine aimed at authorization. Ballerine (Apache 2.0, TS) is the one OSS
project aimed at fintech risk decisioning, but the company has moved commercial and states the OSS build
is not actively supported. Gandalf (PHP) had champion/challenger and decision history years ago; it is
abandoned.

**Complementary building blocks:** feature stores (Feast, Featureform, Hopsworks — AGPL); drift /
explainability / fairness (Evidently, NannyML, SHAP, InterpretML, Aequitas for fair-lending — all
permissive; Alibi → BSL, Arize Phoenix → Elastic Licence, Deepchecks → AGPL are restricted).

**Commercial decisioning:** Taktile, Oscilar, Provenir, Zest, Scienaptic (challengers); FICO, SAS, IBM
ODM, Experian PowerCurve, CRIF, Zoot, GDS Link, Pega, ACTICO, InRule (incumbents). **Identity/fraud
orchestration:** Alloy, Socure (+Effectiv), DataVisor, Sardine, Feedzai, Sift, Unit21.

## Where the gap is

Across the projects surveyed, we did not find one that is open-source, self-hostable, event-sourced, and
ships governance (four-eyes, RBAC, drift, backtesting) for fintech decisioning. The OSS engines
(GoRules, Drools/BAMOE, Camunda) ship without that governance layer — in each case it is a paid tier or
a separate product. The SaaS challengers that do ship it (Taktile, Oscilar, Zest) are cloud-only, which
is a blocker for data-residency- or model-risk-constrained buyers. intraktible occupies that
combination.

Two caveats on that observation. First, the same combination is empty in part because the governance
layer is where the commercial open-core products charge — so the open question is a business-model one,
not evidence of unmet demand. Second, the data-source relationships, SOC 2 / ISO, references, and
fair-lending depth that the incumbents have are not things code produces; they are separate work, and
they gate a regulated rollout as much as any missing feature. The forward roadmap
([PLAN.md](../PLAN.md) §8b) covers the code-addressable gaps; the non-code track is listed alongside it.
