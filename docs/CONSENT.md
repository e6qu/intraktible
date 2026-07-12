# Lawful basis & consent records

Intraktible's users are businesses — banks, insurers, fintechs — deciding on *their*
customers. They, as data controllers, must be able to prove the lawful basis for each
data pull a decision performs, and (where the basis is consent) produce the evidence.
This is the record `platform/consent` holds and the decide path enforces.

The name "consent" is a misnomer we keep for continuity; the model records a **lawful
basis**, of which consent is one branch. The research below is why.

## What the research found (US / UK / EU, July 2026)

Four jurisdiction/practice sweeps, cross-checked. The load-bearing conclusions:

- **For credit decisioning, consent is usually the wrong basis.** A lender/insurer and
  its customer sit in a power imbalance, so consent is rarely "freely given" (GDPR Art.
  6; EDPB Guidelines 05/2020). The ICO's own worked example is a credit card sending
  data to a credit-reference agency: because the company would process anyway, "asking
  for consent is misleading and inappropriate — there is no real choice." The realistic
  bases are **contract** (6(1)(b) — scoring an application the customer submitted),
  **legal obligation** (6(1)(c) — affordability/AML), or **legitimate interest**
  (6(1)(f) — credit-reference sharing, fraud). US law agrees by a different route: the
  FCRA authorizes a pull on a **permissible purpose**, and GLBA runs on **opt-out**, so
  statutory opt-in consent is not the gate for a consumer-initiated application.
- **Where documented consent *is* load-bearing:** special-category data (e.g. health in
  insurance underwriting → GDPR Art. 9 *explicit* consent), FCRA §604(b) employment
  screening, and TCPA / PECR marketing contact.
- **What a defensible record must capture** (intersection of US FCRA/GLBA/ECOA/E-SIGN,
  UK ICO records-of-consent, EU EDPB 05/2020 §108, and the ISO/IEC 27560 / Kantara
  consent-receipt schemas): subject identity; purpose; lawful basis; timestamp; **how
  it was obtained**; **the exact disclosure/notice version shown** (the most-cited
  defensibility field — reproduce what was agreed to); the **signed artifact plus a
  content hash** for tamper-evidence; and withdrawal + when.
- **UK/EU divergence on automated decisions (worth tracking, not yet modelled):** the UK
  DUAA 2025 (Arts 22A–22D, in force 2026-02-05) moved from the EU's prohibition-plus-
  three-gates to permission-with-safeguards, allowing solely-automated significant
  decisions on non-special data under legitimate interests without consent. The EU Art.
  22 is unchanged. Cross-border controllers run two frameworks.

Caveat, per the sources: almost none of the specific *field lists* are enumerated in
statute — they are engineered backward from what a controller must be able to *prove*.
Only the retention durations (ECOA 25 months, CCPA request-records 24 months, FCRA/TCPA
statutes of limitation) and structural rules (FCRA standalone disclosure, TCPA written-
consent elements, GLBA opt-out, E-SIGN validity/integrity) are black-letter. The ISO/IEC
27560 field breakdown was drawn from secondary analyses, not the paywalled standard.

## What we built

- `consent.LawfulBasis` already constrained the basis to the six GDPR Art. 6 values.
- `consent.Evidence` now backs a grant with: `method` (a controlled vocabulary —
  `e_signature`, `wet_signature`, `scanned_document`, `click_through`, `verbal`,
  `api_assertion`), a `reference` locating the signed artifact in the controller's own
  system of record, a `content_hash` + `hash_algo` (tamper-evidence), and the
  `notice_version` shown. An unknown method or a hash with no algorithm is rejected at
  the boundary — the same fail-loud discipline as the basis.
- **The document's bytes never enter Intraktible.** On the subject's data page the
  operator can attach a file; the browser hashes it locally (SHA-256, `crypto.subtle`)
  and stores only the fingerprint plus filename. This respects data residency — the
  artifact stays in the tenant's store — while still giving an auditable, tamper-evident
  link. Storing the bytes (a WORM object store, as e-signature vendors do) is out of
  scope; the reference + hash is the honest minimum.
- The demo seed records the **correct basis** — `contract` for an applicant's
  credit-underwriting pull, `legitimate_interest` for account servicing — not `consent`,
  and attaches a worked evidence record for applicants.

Enforcement is unchanged: a Connect node with `requires_consent` refuses to fetch unless
the subject bears an active record for that purpose (see `decision-engine` decide path).
The basis/evidence is what a compliance reviewer inspects and what answers a DSAR, since
the record is keyed to the same subject identity as PII sealing and erasure.

## Not done

- The Art. 22 **post-decision** human-review safeguard is now recorded (see the
  `reconsideration` package: a person upholds/overturns a solely-automated decline with
  a rationale). Still open: the **in-flow** safeguards (a standing right-to-contest
  channel, the "meaningful information about the logic" explanation as a first-class
  artifact) and the UK DUAA 2025 (22A–22D) divergence.
- No byte-level artifact storage / WORM retention lock; we hold the reference only.
- Retention clocks (ECOA/CCPA/FCRA) are not enforced against consent records yet.
