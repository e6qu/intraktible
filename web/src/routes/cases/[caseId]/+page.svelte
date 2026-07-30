<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import Copyable from '$lib/Copyable.svelte';
  import {
    getCase,
    assignCase,
    setCaseStatus,
    addCaseNote,
    getCaseTypeVersion,
    setCasePriority,
    updateCaseFields,
    dispositionCase,
    linkCaseEvidence,
    registerCaseAttachment,
    accessCaseAttachment,
    selectCaseQA,
    reviewCaseQA,
    addCaseQAFeedback,
    retryCaseWebhook,
    routeCase,
    getDecision,
    ApiError,
    type Case,
    type CaseFieldDefinition,
    type CaseTypeView,
    type Decision
  } from '$lib/api';
  import { displayEntries } from '$lib/kv';
  import Breadcrumb from '$lib/Breadcrumb.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Badge from '$lib/Badge.svelte';
  import { caseStatusTone, slaTone, statusTone } from '$lib/badge';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { appHref } from '$lib/paths';
  import { toast } from '$lib/toast';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let c = $state<Case | null>(null);
  let sourceDecision = $state<Decision | null>(null);
  let error = $state('');
  // A 404 is a distinct, expected state — a mistyped/stale id — and gets a polished
  // EmptyState rather than the raw red error string used for real failures (network,
  // 5xx). Keyed off the HTTP status (ApiError.status), not a fragile message regex.
  let notFound = $state(false);

  let assignee = $state('');
  let newStatus = $state('in_progress');
  let definition = $state<CaseTypeView | null>(null);
  let noteText = $state('');
  let priority = $state('normal');
  let disposition = $state('');
  let reasonCode = $state('');
  let dispositionNote = $state('');
  let evidenceKind = $state('decision');
  let evidenceSubject = $state('');
  let evidenceLabel = $state('');
  let attachmentName = $state('');
  let attachmentSHA = $state('');
  let attachmentRef = $state('');
  let attachmentBasis = $state('legal_obligation');
  let qaSample = $state('');
  let qaReviewer = $state('');
  let qaDisposition = $state('');
  let qaReason = $state('');
  let qaNote = $state('');
  let qaOverride = $state(false);
  let qaFeedback = $state('');
  let retryReason = $state('');
  let revealedAttachmentRefs = $state<Record<string, string>>({});
  let fieldDrafts = $state<Record<string, string>>({});
  let fieldOriginals = $state<Record<string, string>>({});
  let seededFieldCase = $state('');
  // Seed the status <select> from the case's real status once, on first load, so it
  // doesn't default to in_progress (which invites an accidental backward
  // transition). Only the first load seeds it — later reloads must not clobber a
  // selection the user is mid-way through making.
  let statusSeeded = false;

  // Derive from the route param so navigating between sibling cases reloads.
  const caseID = $derived($page.params.caseId ?? '');

  const allowedTransitions = $derived(
    definition?.definition.transitions.filter(
      (transition) =>
        transition.from === c?.status &&
        (!transition.roles?.length || transition.roles.includes($user?.role ?? 'viewer')) &&
        !definition?.definition.dispositions.some(
          (disposition) => disposition.terminal_state === transition.to
        )
    ) ?? []
  );
  const selectedDisposition = $derived(
    definition?.definition.dispositions.find((item) => item.key === disposition)
  );
  const closed = $derived(
    c != null &&
      (Boolean(c.resolved_at) ||
        c.status === 'completed' ||
        definition?.definition.dispositions.some((item) => item.terminal_state === c?.status) ===
          true)
  );
  const sourceSuspended = $derived(sourceDecision?.status === 'suspended');

  // The SLA state is a wire enum (on_track/due_soon/overdue) — render it as a
  // human label, not the raw underscored value.
  function slaLabel(s: string): string {
    return s.replace(/_/g, ' ');
  }

  async function load() {
    error = '';
    notFound = false;
    // Drop a stale response when sibling navigation changes caseID mid-flight.
    const reqID = caseID;
    try {
      // Only refresh the displayed case; the action inputs are user-controlled
      // (resetting them on every reload would race with the user's selection).
      const got = await getCase(key, caseID);
      const decision = got.source_decision_id
        ? await getDecision(key, got.source_decision_id)
        : null;
      const pinned =
        got.case_type_version > 0
          ? await getCaseTypeVersion(key, got.case_type, got.case_type_version)
          : null;
      if (caseID !== reqID) return;
      c = got;
      sourceDecision = decision;
      definition = pinned;
      if (seededFieldCase !== got.case_id) {
        const context =
          got.context && typeof got.context === 'object' && !Array.isArray(got.context)
            ? (got.context as Record<string, unknown>)
            : {};
        fieldDrafts = Object.fromEntries(
          (pinned?.definition.fields ?? []).map((field) => {
            const value = context[field.key];
            return [
              field.key,
              typeof value === 'object' && value !== null
                ? JSON.stringify(value)
                : value == null
                  ? ''
                  : String(value)
            ];
          })
        );
        fieldOriginals = { ...fieldDrafts };
        seededFieldCase = got.case_id;
      }
      priority = got.priority;
      if (!statusSeeded) {
        newStatus = got.status;
        statusSeeded = true;
      }
      if (!disposition && pinned?.definition.dispositions[0]) {
        disposition = pinned.definition.dispositions[0].key;
        reasonCode = pinned.definition.dispositions[0].reason_codes[0] ?? '';
      }
    } catch (e) {
      if (caseID === reqID) {
        if (e instanceof ApiError && e.status === 404) notFound = true;
        else error = e instanceof Error ? e.message : String(e);
      }
    }
  }

  // Format a context value for the scannable fact grid: *_usd / *_amount keys get
  // currency formatting; everything else is shown verbatim (displayEntries has
  // already stringified nested objects to compact JSON).
  const usdFmt = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 0
  });
  function factValue(key: string, value: string): string {
    const n = Number(value);
    if (/(_usd|_amount)$/.test(key) && Number.isFinite(n)) return usdFmt.format(n);
    return value;
  }
  // The risk figure is the headline number a reviewer scans for — emphasise it.
  function isRisk(key: string): boolean {
    return /risk|score/i.test(key);
  }
  // Published layouts control scan order for the active role. The backend has
  // already enforced field-level PII visibility, so fields not named by the
  // layout remain visible after the preferred sections instead of being hidden
  // by a presentation-only client rule.
  function contextEntries(): Array<[string, string]> {
    if (!c) return [];
    const entries = displayEntries(c.context);
    const layout = definition?.definition.layouts.find(
      (candidate) => candidate.role === ($user?.role ?? 'viewer')
    );
    if (!layout) return entries;
    const rank = new Map(layout.sections.map((field, index) => [field, index]));
    return [...entries].sort((a, b) => {
      const left = rank.get(a[0]) ?? Number.MAX_SAFE_INTEGER;
      const right = rank.get(b[0]) ?? Number.MAX_SAFE_INTEGER;
      return left - right || a[0].localeCompare(b[0]);
    });
  }
  function fieldLabel(field: string): string {
    return (
      definition?.definition.fields.find((candidate) => candidate.key === field)?.label ?? field
    );
  }
  const editableFields = $derived.by((): CaseFieldDefinition[] => {
    const editable =
      definition?.definition.layouts.find(
        (candidate) => candidate.role === ($user?.role ?? 'viewer')
      )?.editable ?? [];
    return (definition?.definition.fields ?? []).filter((field) => editable.includes(field.key));
  });
  function parsedFieldValue(field: CaseFieldDefinition): unknown {
    // Svelte coerces a bound input[type=number] to a number at runtime even
    // though drafts are seeded as display strings. Normalize before checking
    // emptiness and parsing so a valid numeric edit cannot fail on String.trim.
    const raw: unknown = fieldDrafts[field.key];
    const value = raw == null ? '' : String(raw);
    if (field.kind !== 'string' && value.trim() === '') return null;
    switch (field.kind) {
      case 'number': {
        const parsed = Number(value);
        if (!Number.isFinite(parsed)) throw new Error(`${field.label} must be a number`);
        return parsed;
      }
      case 'boolean':
        return value === 'true';
      case 'object':
      case 'array':
        return JSON.parse(value);
      default:
        return value;
    }
  }
  async function saveFields() {
    const fields = Object.fromEntries(
      editableFields
        .filter((field) => fieldDrafts[field.key] !== fieldOriginals[field.key])
        .map((field) => [field.key, parsedFieldValue(field)])
    );
    if (Object.keys(fields).length === 0) throw new Error('No field values changed');
    await updateCaseFields(key, caseID, fields);
    seededFieldCase = '';
  }

  let busy = $state(false);
  async function run(action: () => Promise<void>, success: string) {
    error = '';
    busy = true;
    try {
      await action();
      await load();
      toast.success(success);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }

  // The backend refuses to overwrite another reviewer's claim, so taking a case over
  // is an explicit act the reviewer confirms — not something an Assign click does
  // silently while its owner is halfway through the review.
  async function claim(target: string) {
    const current = c?.assignee ?? '';
    const takeover = current !== '' && current !== target;
    if (takeover && !confirm(`${current} is reviewing this case. Take it over?`)) return;
    await assignCase(key, caseID, target, takeover);
  }

  $effect(() => {
    void caseID; // reload on initial mount and sibling navigation
    // Reset the rendered case and the status seed — otherwise a failed sibling
    // load keeps showing the previous case (and its status in the select).
    c = null;
    sourceDecision = null;
    definition = null;
    statusSeeded = false;
    void load();
  });
</script>

<main>
  <Breadcrumb sectionHref="/cases" sectionLabel="Cases" current={caseID} />
  {#if !notFound}
    <div class="row reload-row">
      <button onclick={load} title="Re-fetch this case and its activity"
        ><Icon name="reload" size={15} /> Reload</button
      >
    </div>
  {/if}
  {#if c}
    <div class="head">
      <h1>{c.company_name}</h1>
      <Badge tone={caseStatusTone(c.status)}
        ><span data-testid="case-status">{c.status}</span></Badge
      >
      {#if !closed && c.sla_state && c.sla_state !== 'on_track'}
        <Badge tone={slaTone(c.sla_state)} title="SLA urgency">
          {c.sla_state === 'overdue' ? '⚠ overdue' : 'due soon'} · {c.days_left}d left
        </Badge>
      {/if}
    </div>
    <dl>
      <dt>type</dt>
      <dd><span class="chip">{c.case_type} · v{c.case_type_version || 'legacy'}</span></dd>
      <dt>priority</dt>
      <dd>{c.priority}</dd>
      <dt>queue</dt>
      <dd>{c.queue || 'unrouted'}</dd>
      <dt>assignee</dt>
      <dd>{c.assignee || '—'}</dd>
      <dt>SLA</dt>
      <dd>{c.sla_days} day{c.sla_days === 1 ? '' : 's'}</dd>
      <dt>days left</dt>
      <dd class={closed ? '' : `sla-${c.sla_state ?? ''}`} data-testid="days-left">
        {#if closed}<span class="muted">—</span>{:else}{c.days_left}{#if c.sla_state}<span
              class="muted">{' ('}{slaLabel(c.sla_state)})</span
            >{/if}{/if}
      </dd>
      {#if c.sla_breached}
        <dt>external SLA alert</dt>
        <dd>
          {#if c.sla_escalation_status === 'delivered'}
            delivered
          {:else if c.sla_escalation_status === 'no_channel'}
            <span class="muted">not sent — no matching webhook is configured</span>
          {:else if c.sla_escalation_status === 'permanent_failure'}
            <span class="error">not sent — webhook rejected the alert permanently</span>
          {:else}
            <span class="muted">delivery pending · retry scheduled</span>
          {/if}
        </dd>
      {/if}
      {#if c.source_decision_id}<dt>source decision</dt>
        <dd>
          <a href={appHref(`/decisions/${c.source_decision_id}`)}>{c.source_decision_id} →</a>
          {#if sourceDecision}
            <Badge tone={statusTone(sourceDecision.status)}>{sourceDecision.status}</Badge>
          {/if}
        </dd>
      {/if}
      {#if c.subject_governance}
        <dt>data governance</dt>
        <dd>
          {#if c.subject_governance.erased}<Badge tone="danger">erased</Badge>{/if}
          {#if c.subject_governance.legal_hold}<Badge tone="warn">legal hold</Badge>{/if}
          {#if c.subject_governance.retained}
            retained until {c.subject_governance.retain_until?.slice(0, 10)}
          {:else}
            no active statutory hold
          {/if}
        </dd>
      {/if}
    </dl>

    {#if c.sla_delivery_attempts.length > 0}
      <h2>Webhook delivery</h2>
      <ol class="timeline" data-testid="sla-attempts">
        {#each c.sla_delivery_attempts as attempt (`${attempt.round}-${attempt.attempt}`)}
          <li>
            <span class="when muted"><RelativeTime value={attempt.at} /></span>
            <span class="what"
              >round {attempt.round + 1}, attempt {attempt.attempt}: {attempt.outcome}</span
            >
          </li>
        {/each}
      </ol>
      {#if c.sla_escalation_status === 'no_channel' || c.sla_escalation_status === 'permanent_failure'}
        <div class="row">
          <input
            bind:value={retryReason}
            placeholder="retry reason"
            aria-label="webhook retry reason"
          />
          <button
            onclick={() =>
              run(async () => {
                await retryCaseWebhook(key, caseID, retryReason);
                retryReason = '';
              }, 'Webhook delivery requeued')}
            disabled={busy || !retryReason.trim()}>Retry delivery</button
          >
        </div>
      {/if}
    {/if}

    {#if contextEntries().length > 0}
      <h2>Context</h2>
      <div class="facts" data-testid="context">
        {#each contextEntries() as [k, v] (k)}
          <div class="fact" class:risk={isRisk(k)}>
            <span class="fact-key">{fieldLabel(k)}</span>
            <span class="fact-val">{factValue(k, v)}</span>
          </div>
        {/each}
      </div>
    {/if}
    {#if editableFields.length > 0 && !closed}
      <div class="actions" data-testid="editable-case-fields">
        <strong>Editable fields</strong>
        <div class="row">
          {#each editableFields as field (field.key)}
            <label>
              {field.label}
              {#if field.kind === 'boolean'}
                <select bind:value={fieldDrafts[field.key]} aria-label={`edit ${field.label}`}>
                  <option value="">unset</option>
                  <option value="true">true</option>
                  <option value="false">false</option>
                </select>
              {:else if field.kind === 'object' || field.kind === 'array'}
                <textarea
                  bind:value={fieldDrafts[field.key]}
                  rows="2"
                  aria-label={`edit ${field.label}`}
                ></textarea>
              {:else}
                <input
                  bind:value={fieldDrafts[field.key]}
                  type={field.kind === 'number' ? 'number' : 'text'}
                  aria-label={`edit ${field.label}`}
                />
              {/if}
            </label>
          {/each}
          <button
            onclick={() => run(saveFields, 'Case fields updated')}
            disabled={busy || !roleAtLeast($user?.role, 'operator')}>Save fields</button
          >
        </div>
      </div>
    {/if}
  {:else if notFound}
    <EmptyState
      icon="cases"
      title="Case not found"
      hint="No case matches this id. It may have been deleted, or the id may be mistyped."
    >
      {#snippet action()}
        <a href={appHref('/cases')}>← Back to the queue</a>
      {/snippet}
    </EmptyState>
  {:else if error}
    <h1>{caseID}</h1>
  {:else}
    <h1>{caseID}</h1>
    <Skeleton rows={5} />
  {/if}
  {#if error && !notFound}<p class="err">{error}</p>{/if}

  {#if c && !closed && sourceSuspended}
    <div class="resolve-bar resume-required">
      <div>
        <strong>Record the human outcome to finish this task</strong>
        <p class="muted">
          This case owns a suspended decision. Approve, decline, or refer it on the decision trace;
          the same recorded action resumes the flow and completes this case.
        </p>
      </div>
      <a
        class="resolve"
        data-testid="case-resume-decision"
        href={appHref(`/decisions/${c.source_decision_id}`)}>Review decision →</a
      >
    </div>
  {:else if c && !closed && c.case_type_version === 0}
    <div class="resolve-bar">
      <button
        class="resolve"
        onclick={() => run(() => setCaseStatus(key, caseID, 'completed'), 'Case resolved')}
        disabled={busy || !roleAtLeast($user?.role, 'operator')}
        title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
      >
        {busy ? 'Working…' : '✓ Resolve case'}
      </button>
      <span class="muted">Mark this case completed.</span>
    </div>
  {:else if c && closed}
    <p class="resolved muted">✓ This case is resolved.</p>
  {:else if c}
    <p class="resolved muted">
      This governed case is active. Complete it with a reasoned disposition below.
    </p>
  {/if}

  {#if c}
    <h2>Actions</h2>
    <div class="actions">
      <div class="row">
        {#if !c.queue}
          <button
            onclick={() => run(() => routeCase(key, caseID), 'Case routed')}
            disabled={busy || closed || !roleAtLeast($user?.role, 'operator')}>Route now</button
          >
        {/if}
        <input
          bind:value={assignee}
          placeholder="assignee"
          aria-label="assignee"
          disabled={closed}
        />
        <button
          onclick={() =>
            run(async () => {
              await claim(assignee);
              assignee = ''; // only clear after a successful save (run() surfaces errors)
            }, 'Assignee updated')}
          disabled={busy || closed || !assignee.trim() || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
          >{c.assignee ? 'Reassign' : 'Assign'}</button
        >
        <button
          onclick={() => run(() => claim($user?.actor ?? ''), 'Assigned to you')}
          disabled={busy ||
            closed ||
            !$user?.actor ||
            c.assignee === $user?.actor ||
            !roleAtLeast($user?.role, 'operator')}
          title={c.assignee === $user?.actor
            ? 'This case is already yours'
            : !roleAtLeast($user?.role, 'operator')
              ? 'Requires the operator role'
              : undefined}>{c.assignee ? 'Take over' : 'Assign to me'}</button
        >
      </div>
      <div class="row">
        <select bind:value={newStatus} aria-label="set status" disabled={closed || sourceSuspended}>
          {#if c.case_type_version === 0}
            <option value="needs_review">needs_review</option>
            <option value="in_progress">in_progress</option>
            <option value="completed">completed</option>
          {:else}
            <option value={c.status}>{c.status} (current)</option>
            {#each allowedTransitions as transition (`${transition.from}-${transition.to}`)}
              <option value={transition.to}>{transition.to}</option>
            {/each}
          {/if}
        </select>
        <button
          onclick={() => run(() => setCaseStatus(key, caseID, newStatus), 'Status updated')}
          disabled={busy || closed || sourceSuspended || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator')
            ? 'Requires the operator role'
            : sourceSuspended
              ? 'Record the outcome on the suspended decision to change this case lifecycle'
              : undefined}>Set status</button
        >
      </div>
      <div class="row">
        <select bind:value={priority} aria-label="case priority" disabled={closed}>
          {#each definition?.definition.priorities ?? ['low', 'normal', 'high', 'critical'] as item}
            <option value={item}>{item}</option>
          {/each}
        </select>
        <button
          onclick={() => run(() => setCasePriority(key, caseID, priority), 'Priority updated')}
          disabled={busy ||
            closed ||
            priority === c.priority ||
            !roleAtLeast($user?.role, 'operator')}>Set priority</button
        >
      </div>
      <div class="row">
        <input bind:value={noteText} placeholder="note" aria-label="note" />
        <button
          onclick={() =>
            run(async () => {
              await addCaseNote(key, caseID, noteText);
              noteText = ''; // only clear after a successful save (run() surfaces errors)
            }, 'Note added')}
          disabled={busy || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
        >
          Add note
        </button>
      </div>
    </div>
  {/if}

  {#if c && definition && !closed && !sourceSuspended}
    <h2>Disposition</h2>
    <p class="muted">Required evidence is checked by the pinned v{definition.version} schema.</p>
    <div class="actions">
      <div class="row">
        <select bind:value={disposition} aria-label="disposition">
          {#each definition.definition.dispositions as item (item.key)}
            <option value={item.key}>{item.label}</option>
          {/each}
        </select>
        <select bind:value={reasonCode} aria-label="reason code">
          {#each selectedDisposition?.reason_codes ?? [] as reason (reason)}
            <option value={reason}>{reason}</option>
          {/each}
        </select>
        <input
          bind:value={dispositionNote}
          placeholder="decision note"
          aria-label="disposition note"
        />
        <button
          onclick={() =>
            run(
              () => dispositionCase(key, caseID, disposition, reasonCode, dispositionNote),
              'Disposition recorded'
            )}
          disabled={busy || !disposition || !reasonCode || !roleAtLeast($user?.role, 'operator')}
          >Record outcome</button
        >
      </div>
    </div>
  {/if}

  {#if c}
    <h2>Evidence</h2>
    {#if c.evidence.length === 0 && c.attachments.length === 0}
      <p class="muted">No evidence linked yet.</p>
    {/if}
    <ul data-testid="case-evidence">
      {#each c.evidence as evidence (evidence.evidence_id)}
        <li>{evidence.label} · {evidence.kind} · {evidence.subject_type}/{evidence.subject_id}</li>
      {/each}
      {#each c.attachments as attachment (attachment.attachment_id)}
        <li>
          {attachment.name} · {attachment.media_type} · SHA-256 {attachment.sha256.slice(0, 12)}…
          {#if attachment.legal_hold}<Badge tone="warn">legal hold</Badge>{/if}
          {#if attachment.erased}
            <Badge tone="danger">erased</Badge>
          {:else if revealedAttachmentRefs[attachment.attachment_id]}
            <Copyable
              value={revealedAttachmentRefs[attachment.attachment_id]}
              label={`${attachment.name} storage reference`}
            />
          {:else}
            <button
              class="link"
              onclick={() =>
                run(async () => {
                  const storageRef = await accessCaseAttachment(
                    key,
                    caseID,
                    attachment.attachment_id,
                    'case review'
                  );
                  revealedAttachmentRefs = {
                    ...revealedAttachmentRefs,
                    [attachment.attachment_id]: storageRef
                  };
                }, 'Attachment access audited; storage reference revealed')}
              disabled={busy || !roleAtLeast($user?.role, 'operator')}
              >Reveal storage reference</button
            >
          {/if}
        </li>
      {/each}
    </ul>
    <div class="actions">
      <strong>Link evidence</strong>
      <div class="row">
        <select bind:value={evidenceKind} aria-label="evidence kind">
          <option value="decision">decision</option>
          <option value="entity">entity</option>
          <option value="agent_run">agent run</option>
          <option value="connector">connector</option>
          <option value="case">case</option>
          <option value="alert">alert</option>
        </select>
        <input
          bind:value={evidenceSubject}
          placeholder="subject id"
          aria-label="evidence subject"
        />
        <input bind:value={evidenceLabel} placeholder="label" aria-label="evidence label" />
        <button
          onclick={() =>
            run(async () => {
              await linkCaseEvidence(key, caseID, {
                evidence_id: crypto.randomUUID(),
                kind: evidenceKind,
                subject_type: evidenceKind,
                subject_id: evidenceSubject,
                label: evidenceLabel
              });
              evidenceSubject = '';
              evidenceLabel = '';
            }, 'Evidence linked')}
          disabled={busy || !evidenceSubject || !evidenceLabel}>Link</button
        >
      </div>
      <strong>Register attachment metadata</strong>
      <div class="row">
        <input bind:value={attachmentName} placeholder="filename" aria-label="attachment name" />
        <input
          bind:value={attachmentSHA}
          placeholder="64-character SHA-256"
          aria-label="attachment hash"
        />
        <input
          bind:value={attachmentRef}
          placeholder="approved storage reference"
          aria-label="attachment storage"
        />
        <input
          bind:value={attachmentBasis}
          placeholder="lawful basis"
          aria-label="attachment lawful basis"
        />
        <button
          onclick={() =>
            run(async () => {
              await registerCaseAttachment(key, caseID, {
                attachment_id: crypto.randomUUID(),
                name: attachmentName,
                media_type: 'application/octet-stream',
                size: 0,
                sha256: attachmentSHA,
                storage_ref: attachmentRef,
                subject: c?.subject,
                lawful_basis: attachmentBasis
              });
              attachmentName = '';
              attachmentSHA = '';
              attachmentRef = '';
            }, 'Attachment metadata registered')}
          disabled={busy || !attachmentName || !attachmentSHA || !attachmentRef}>Register</button
        >
      </div>
    </div>
  {/if}

  {#if c?.disposition}
    <h2>Quality assurance</h2>
    {#if c.qa}
      <p data-testid="qa-state">
        Assigned to <b>{c.qa.reviewer}</b> · {c.qa.status}
        {#if c.qa.validated}<Badge tone="ok">validated</Badge>{/if}
        {#if c.qa.disputed}<Badge tone="danger">disputed</Badge>{/if}
      </p>
      {#if c.qa.status !== 'completed' && c.qa.reviewer === $user?.actor}
        <div class="row">
          <select bind:value={qaDisposition} aria-label="QA disposition">
            <option value="">select disposition</option>
            {#each definition?.definition.dispositions ?? [] as item (item.key)}
              <option value={item.key}>{item.label}</option>
            {/each}
          </select>
          <input bind:value={qaReason} placeholder="reason code" aria-label="QA reason code" />
          <input bind:value={qaNote} placeholder="QA note" aria-label="QA note" />
          <label><input type="checkbox" bind:checked={qaOverride} /> override</label>
          <button
            onclick={() =>
              run(
                () =>
                  reviewCaseQA(
                    key,
                    caseID,
                    c?.qa?.sample_id ?? '',
                    qaDisposition,
                    qaReason,
                    qaNote,
                    qaOverride
                  ),
                'QA review recorded'
              )}
            disabled={busy || !qaDisposition || !qaReason}>Complete QA</button
          >
        </div>
      {:else if c.qa.status === 'completed'}
        <div class="row">
          <input bind:value={qaFeedback} placeholder="reviewer feedback" aria-label="QA feedback" />
          <button
            onclick={() =>
              run(async () => {
                await addCaseQAFeedback(key, caseID, c?.qa?.sample_id ?? '', qaFeedback);
                qaFeedback = '';
              }, 'Feedback recorded')}
            disabled={busy || !qaFeedback}>Add feedback</button
          >
        </div>
      {/if}
    {:else}
      <div class="row">
        <input bind:value={qaSample} placeholder="sample id" aria-label="QA sample id" />
        <input
          bind:value={qaReviewer}
          placeholder="independent reviewer"
          aria-label="QA reviewer"
        />
        <button
          onclick={() =>
            run(
              () => selectCaseQA(key, caseID, qaSample, qaReviewer, 10000),
              'Case selected for QA'
            )}
          disabled={busy || !qaSample || !qaReviewer}>Select for QA</button
        >
      </div>
    {/if}
  {/if}

  {#if c}
    <h2>Notes</h2>
    {#if c.notes.length === 0}<p class="muted">No notes.</p>{/if}
    <ul>
      {#each c.notes as n, i (i)}<li>
          <b>{n.author}</b>: {n.text} <span class="muted"><RelativeTime value={n.at} /></span>
        </li>{/each}
    </ul>

    <h2>Activity</h2>
    <ol class="timeline" data-testid="audit">
      {#each c.audit as a, i (i)}
        <li>
          <span class="when muted" title={new Date(a.at).toLocaleString()}>
            <RelativeTime value={a.at} />
          </span>
          <span class="what"><code>{a.type}</code> {a.detail}</span>
          <span class="who muted">{a.actor}</span>
        </li>
      {/each}
    </ol>

    <h2>Discussion</h2>
    <p class="muted disc-hint">
      Talk the case through with the team — @mention a colleague to notify them. Notes above stay
      the immutable work record; this thread is for collaboration.
    </p>
    <CommentThread subjectType="case" subjectId={caseID} title="Case discussion" />
  {/if}
</main>

<style>
  .reload-row {
    justify-content: flex-end;
    margin-top: -2.4rem;
  }
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.4rem 0;
    align-items: center;
  }
  input,
  button,
  select {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
    margin-bottom: 0.4rem;
  }
  .head h1 {
    margin: 0;
  }
  dl {
    display: grid;
    grid-template-columns: 8rem 1fr;
    gap: 0.2rem 1rem;
  }
  dt {
    color: var(--fg-subtle);
  }
  .chip {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
    border: 1px solid var(--border);
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
    gap: 0.5rem;
    margin: 0.5rem 0 1rem;
  }
  .fact {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.5rem 0.65rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  .fact-key {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
  }
  .fact-val {
    font-size: 0.95rem;
    color: var(--fg);
    word-break: break-word;
  }
  .fact.risk {
    border-color: color-mix(in srgb, var(--accent) 40%, transparent);
    background: color-mix(in srgb, var(--accent) 8%, var(--surface-2));
  }
  .fact.risk .fact-val {
    font-size: 1.35rem;
    font-weight: 700;
    color: var(--accent-ink, var(--accent));
  }
  .resolve-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
    margin: 1rem 0;
  }
  .resume-required {
    justify-content: space-between;
  }
  .resume-required p {
    margin: 0.3rem 0 0;
    max-width: 38rem;
  }
  .resume-required a.resolve {
    text-decoration: none;
    white-space: nowrap;
  }
  .resolve {
    font: inherit;
    font-weight: 600;
    padding: 0.5rem 1rem;
    border: 1px solid color-mix(in srgb, var(--ok, #16a34a) 40%, transparent);
    border-radius: 0.5rem;
    background: color-mix(in srgb, var(--ok, #16a34a) 16%, transparent);
    color: var(--ok, #16a34a);
    cursor: pointer;
  }
  .resolve:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .resolved {
    margin: 1rem 0;
    font-weight: 600;
  }
  .actions {
    margin: 1rem 0;
    padding: 0.6rem;
    background: #8881;
    border-radius: 0.5rem;
  }
  button.link {
    border: 0;
    background: transparent;
    color: var(--link, var(--accent-ink));
    cursor: pointer;
  }
  ul {
    padding-left: 1rem;
  }
  code {
    background: #8881;
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .disc-hint {
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
  }
  .sla-due_soon {
    color: var(--warn);
  }
  .sla-overdue {
    color: var(--danger);
    font-weight: 600;
  }
  ol.timeline {
    list-style: none;
    padding: 0;
    margin: 0.5rem 0;
    border-left: 2px solid var(--border);
  }
  ol.timeline li {
    position: relative;
    padding: 0.4rem 0 0.4rem 1rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: baseline;
  }
  ol.timeline li::before {
    content: '';
    position: absolute;
    left: -5px;
    top: 0.75rem;
    width: 8px;
    height: 8px;
    border-radius: 999px;
    background: var(--accent);
  }
  ol.timeline .when {
    min-width: 7rem;
    font-size: 0.82rem;
  }
  ol.timeline .who {
    font-size: 0.82rem;
  }
</style>
