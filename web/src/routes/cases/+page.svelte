<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { goto, afterNavigate } from '$app/navigation';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { get } from 'svelte/store';
  import { toast } from '$lib/toast';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import Hint from '$lib/Hint.svelte';
  import { caseStatusTone, slaTone } from '$lib/badge';
  import {
    listCases,
    getCaseSummary,
    getCaseAnalytics,
    exportCaseAudit,
    requestReview,
    sweepSLA,
    bulkCases,
    listCaseTypes,
    listCaseQueues,
    listCaseReviewers,
    listCaseSavedViews,
    saveCaseView,
    deleteCaseView,
    listCaseDuplicates,
    publishCaseType,
    configureCaseQueue,
    configureCaseReviewer,
    rebalanceCases,
    type Case,
    type CaseSummary,
    type CaseAnalytics,
    type CaseTypeView,
    type CaseTypeDefinition,
    type CaseQueueDefinition,
    type CaseReviewerProfile,
    type CaseSavedView,
    type CaseDuplicateGroup
  } from '$lib/api';
  import { resolvePersona, personaLens } from '$lib/persona';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function exportCsv() {
    const blob = await exportCaseAudit(key, 'csv', activeFilter());
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'case-audit.csv';
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    toast.success(`Exported the filtered audit for ${sorted.length} case(s)`);
  }
  // The backend records resolved_at for every terminal state in the case's pinned
  // definition, including tenant-defined names that the UI cannot predict.
  function isClosed(c: Case): boolean {
    return Boolean(c.resolved_at) || c.status === 'completed';
  }

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  // The status filter is URL-driven so a filtered queue is deep-linkable and
  // back/forward replays it (afterNavigate hydrates it, the control pushes to the URL).
  // Its initial value is the persona's lens (an operator lands on the open review
  // queue) only when the URL carries no status of its own; other personas see the full
  // list. Just the default focus — the control below lets the user widen or change it.
  const casesLens = personaLens(resolvePersona()).cases ?? {};
  let statusFilter = $state<string>(casesLens.status ?? '');
  let queryFilter = $state('');
  let queueFilter = $state('');
  let priorityFilter = $state('');
  let list = $state<Case[]>([]);
  // The persona may also order the queue: 'urgency' surfaces the soonest-due / overdue
  // cases first (days_left ascending — overdue cases are <= 0). Same data, re-ordered
  // for the viewer; non-urgency personas keep store order.
  const sorted = $derived(
    casesLens.sort === 'urgency' ? [...list].sort((a, b) => a.days_left - b.days_left) : list
  );
  // Empty-state copy: when the persona's lens is the active filter and it supplies a
  // role-specific message (e.g. an operator's "queue is clear"), use it; otherwise the
  // generic per-filter message.
  const emptyCopy = $derived(
    casesLens.empty && statusFilter === (casesLens.status ?? '')
      ? casesLens.empty
      : {
          title: statusFilter ? `No ${statusFilter} cases` : 'The review queue is clear',
          hint: "Cases open here when a flow's manual-review node escalates, an agent run is escalated, or you open one above."
        }
  );
  let summary = $state<CaseSummary | null>(null);
  let analytics = $state<CaseAnalytics | null>(null);
  let caseTypes = $state<CaseTypeView[]>([]);
  let queues = $state<Array<{ definition: CaseQueueDefinition }>>([]);
  let reviewers = $state<Array<{ profile: CaseReviewerProfile }>>([]);
  let savedViews = $state<CaseSavedView[]>([]);
  let duplicateGroups = $state<CaseDuplicateGroup[]>([]);
  let error = $state('');
  let loading = $state(true);

  // new-case form
  let company = $state('');
  let caseType = $state('');
  let slaDays = $state(5);
  let jurisdiction = $state('');
  let subject = $state('');
  let contextJSON = $state('{}');

  // Bulk selection on the queue (multi-select → assign / mark completed).
  let selectedIds = $state<string[]>([]);
  let bulkAssignee = $state('');
  let bulkPriority = $state('high');
  let bulkDisposition = $state('');
  let bulkReason = $state('');
  let bulkBusy = $state(false);
  const allSelected = $derived(list.length > 0 && selectedIds.length === list.length);
  const selectedCases = $derived(list.filter((c) => selectedIds.includes(c.case_id)));
  const selectedGoverned = $derived(selectedCases.some((c) => c.case_type_version > 0));
  function toggle(id: string) {
    selectedIds = selectedIds.includes(id)
      ? selectedIds.filter((x) => x !== id)
      : [...selectedIds, id];
  }
  function toggleAll() {
    selectedIds = allSelected ? [] : list.map((c) => c.case_id);
  }

  // A generation token so overlapping loads (rapid status-filter changes / Reload)
  // don't clobber: only the latest request's response is allowed to write the list,
  // matching the decisions page. Without it a slower earlier response could resolve
  // last and leave rows that no longer match the selected status.
  let loadSeq = 0;
  function activeFilter() {
    return {
      status: statusFilter,
      queue: queueFilter,
      priority: priorityFilter,
      q: queryFilter
    };
  }
  async function load() {
    const seq = ++loadSeq;
    loading = true;
    error = '';
    selectedIds = []; // the list is changing; drop a stale selection
    try {
      const [
        nextList,
        nextSummary,
        nextAnalytics,
        nextTypes,
        nextQueues,
        nextSavedViews,
        nextDuplicateGroups
      ] = await Promise.all([
        listCases(key, activeFilter()),
        getCaseSummary(key, activeFilter()),
        getCaseAnalytics(key),
        listCaseTypes(key),
        listCaseQueues(key),
        listCaseSavedViews(key),
        listCaseDuplicates(key)
      ]);
      if (seq !== loadSeq) return; // a newer load superseded this one
      list = nextList;
      summary = nextSummary;
      analytics = nextAnalytics;
      caseTypes = nextTypes;
      queues = nextQueues;
      savedViews = nextSavedViews;
      duplicateGroups = nextDuplicateGroups;
      if (!caseType && caseTypes.length > 0) caseType = caseTypes[0].key;
      if ($user?.role === 'admin') reviewers = await listCaseReviewers(key);
    } catch (e) {
      if (seq === loadSeq) error = msg(e);
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }

  async function bulkAssign() {
    if (bulkBusy || !bulkAssignee.trim() || selectedIds.length === 0) return;
    const who = bulkAssignee.trim();
    // The backend refuses to overwrite another reviewer's claim. Ask once for the
    // whole batch rather than letting most of it succeed and the owned ones fail.
    const owned = list.filter(
      (c) => selectedIds.includes(c.case_id) && c.assignee && c.assignee !== who
    );
    if (
      owned.length > 0 &&
      !confirm(`${owned.length} of these cases are assigned to someone else. Take them over?`)
    ) {
      return;
    }
    bulkBusy = true;
    error = '';
    try {
      const result = await bulkCases(
        key,
        {
          operation: 'assign',
          case_ids: selectedIds,
          target: who,
          reassign: owned.length > 0
        },
        crypto.randomUUID()
      );
      if (result.failed) {
        toast.error(`Assigned ${result.succeeded}; ${result.failed} failed`);
      } else {
        toast.success(`Assigned ${result.succeeded} case(s) to ${who}`);
      }
      bulkAssignee = '';
      await load();
    } finally {
      bulkBusy = false;
    }
  }
  async function bulkComplete() {
    if (bulkBusy || selectedIds.length === 0) return;
    if (!confirm(`Mark ${selectedIds.length} case(s) completed?`)) return;
    bulkBusy = true;
    error = '';
    try {
      const result = await bulkCases(
        key,
        { operation: 'status', case_ids: selectedIds, target: 'completed' },
        crypto.randomUUID()
      );
      if (result.failed) {
        toast.error(`Completed ${result.succeeded}; ${result.failed} failed`);
      } else {
        toast.success(`Completed ${result.succeeded} case(s)`);
      }
      await load();
    } finally {
      bulkBusy = false;
    }
  }

  async function bulkSetPriority() {
    if (bulkBusy || selectedIds.length === 0) return;
    bulkBusy = true;
    try {
      const result = await bulkCases(
        key,
        { operation: 'priority', case_ids: selectedIds, target: bulkPriority },
        crypto.randomUUID()
      );
      if (result.failed) {
        toast.error(`Prioritized ${result.succeeded}; ${result.failed} failed`);
      } else {
        toast.success(`Set priority on ${result.succeeded} case(s)`);
      }
      await load();
    } finally {
      bulkBusy = false;
    }
  }

  async function bulkSetDisposition() {
    if (bulkBusy || selectedIds.length === 0 || !bulkDisposition.trim() || !bulkReason.trim())
      return;
    if (!confirm(`Record ${bulkDisposition} on ${selectedIds.length} case(s)?`)) return;
    bulkBusy = true;
    try {
      const result = await bulkCases(
        key,
        {
          operation: 'disposition',
          case_ids: selectedIds,
          target: bulkDisposition.trim(),
          reason_code: bulkReason.trim()
        },
        crypto.randomUUID()
      );
      if (result.failed) {
        toast.error(`Disposed ${result.succeeded}; ${result.failed} failed`);
      } else {
        toast.success(`Recorded disposition on ${result.succeeded} case(s)`);
      }
      await load();
    } finally {
      bulkBusy = false;
    }
  }

  let savedViewName = $state('');
  let viewBusy = $state(false);
  async function saveCurrentView() {
    if (viewBusy || !savedViewName.trim()) return;
    viewBusy = true;
    try {
      await saveCaseView(key, savedViewName.trim(), activeFilter());
      savedViewName = '';
      toast.success('Saved this queue view');
      savedViews = await listCaseSavedViews(key);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      viewBusy = false;
    }
  }
  function applySavedView(view: CaseSavedView) {
    statusFilter = view.query.status ?? '';
    queryFilter = view.query.q ?? '';
    queueFilter = view.query.queue ?? '';
    priorityFilter = view.query.priority ?? '';
    pushURL();
  }
  async function removeSavedView(view: CaseSavedView) {
    viewBusy = true;
    try {
      await deleteCaseView(key, view.view_id);
      savedViews = savedViews.filter((candidate) => candidate.view_id !== view.view_id);
      toast.success(`Deleted ${view.name}`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      viewBusy = false;
    }
  }

  let creating = $state(false);
  async function create() {
    if (creating) return; // guard against double-submit (Enter + click) → duplicate cases
    error = '';
    creating = true;
    try {
      const context = JSON.parse(contextJSON) as Record<string, unknown>;
      await requestReview(key, {
        company_name: company,
        case_type: caseType,
        sla_days: slaDays,
        jurisdiction,
        subject,
        context
      });
      const opened = company;
      company = '';
      await load();
      toast.success(`Opened case for ${opened}`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      creating = false;
    }
  }

  let sweeping = $state(false);
  async function runSweep() {
    error = '';
    sweeping = true;
    try {
      const { count } = await sweepSLA(key);
      toast.success(count > 0 ? `${count} case(s) breached SLA` : 'No SLA breaches');
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      sweeping = false;
    }
  }

  let configBusy = $state(false);
  let definitionJSON = $state(
    JSON.stringify(
      {
        key: 'investigation',
        name: 'Investigation',
        initial_state: 'intake',
        fields: [
          { key: 'risk_score', label: 'Risk score', kind: 'number', required: true },
          {
            key: 'customer_name',
            label: 'Customer name',
            kind: 'string',
            pii: true,
            read_by: ['operator', 'admin']
          }
        ],
        transitions: [
          { from: 'intake', to: 'investigating', roles: ['operator', 'editor', 'admin'] },
          { from: 'investigating', to: 'resolved', roles: ['operator', 'editor', 'admin'] }
        ],
        dispositions: [
          {
            key: 'clear',
            label: 'Clear',
            reason_codes: ['verified'],
            terminal_state: 'resolved'
          }
        ],
        priorities: ['normal', 'high'],
        service_calendar: {
          timezone: 'UTC',
          weekdays: [1, 2, 3, 4, 5],
          start_hour: 9,
          end_hour: 17,
          sla_hours: 24
        },
        evidence_requirements: [],
        layouts: [{ role: 'operator', sections: ['risk_score', 'customer_name'] }]
      },
      null,
      2
    )
  );
  let queueKey = $state('');
  let queueName = $state('');
  let queueOrder = $state(0);
  let queueSkills = $state('');
  let queueEscalation = $state('');
  let queueMinAge = $state(0);
  let queueMaxAge = $state(0);
  let reviewerActor = $state('');
  let reviewerSkills = $state('');
  let reviewerCapacity = $state(10);

  async function publishDefinition() {
    configBusy = true;
    try {
      const result = await publishCaseType(key, JSON.parse(definitionJSON) as CaseTypeDefinition);
      toast.success(`Published ${result.key} v${result.version}`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      configBusy = false;
    }
  }

  async function saveQueue() {
    configBusy = true;
    try {
      await configureCaseQueue(key, {
        key: queueKey.trim(),
        name: queueName.trim(),
        order: queueOrder || undefined,
        case_types: caseTypes.map((item) => item.key),
        required_skills: queueSkills
          .split(',')
          .map((item) => item.trim())
          .filter(Boolean),
        capacity: 100,
        escalation_queue: queueEscalation.trim() || undefined,
        min_age_hours: queueMinAge || undefined,
        max_age_hours: queueMaxAge || undefined
      });
      toast.success(`Configured queue ${queueKey}`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      configBusy = false;
    }
  }

  async function saveReviewer() {
    configBusy = true;
    try {
      await configureCaseReviewer(key, {
        actor: reviewerActor.trim(),
        skills: reviewerSkills
          .split(',')
          .map((item) => item.trim())
          .filter(Boolean),
        jurisdictions: [],
        capacity: reviewerCapacity,
        active: true
      });
      toast.success(`Configured reviewer ${reviewerActor}`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      configBusy = false;
    }
  }

  async function rebalance() {
    configBusy = true;
    try {
      const result = await rebalanceCases(key);
      toast.success(`Rebalanced ${result.moved.length} case(s)`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      configBusy = false;
    }
  }

  // Push the selected status into the URL; afterNavigate below re-fetches. "All"
  // serializes as an explicit empty param (?status=) — dropping the param entirely
  // made the URL pristine again, so the persona-lens default instantly overrode the
  // user's "all" and an operator could never see the whole queue.
  function pushURL() {
    const query = new URLSearchParams({
      status: statusFilter,
      q: queryFilter,
      queue: queueFilter,
      priority: priorityFilter
    });
    goto(`?${query}`, {
      keepFocus: true,
      noScroll: true
    });
  }
  function hydrateFromURL(): void {
    const sp = get(page).url.searchParams;
    statusFilter = sp.has('status') ? (sp.get('status') ?? '') : (casesLens.status ?? '');
    queryFilter = sp.get('q') ?? '';
    queueFilter = sp.get('queue') ?? '';
    priorityFilter = sp.get('priority') ?? '';
    void load();
  }

  onMount(hydrateFromURL);
  afterNavigate((navigation) => {
    if (navigation.type !== 'enter') hydrateFromURL();
  });
</script>

<main>
  <h1>Cases</h1>
  <p class="lede">
    Work queue for decisions the engine escalated to a human — a <code>manual_review</code>
    node opens a case here. Each carries the triggering decision, an SLA clock, and an assignee; resolving
    one records the outcome back to the audit trail.
  </p>
  <div class="row">
    <label
      >status
      <select bind:value={statusFilter} onchange={pushURL} aria-label="status filter">
        <option value="">all</option>
        <option value="needs_review">needs_review</option>
        <option value="in_progress">in_progress</option>
        <option value="completed">completed</option>
      </select>
    </label>
    <label
      >search
      <input
        bind:value={queryFilter}
        onkeydown={(event) => event.key === 'Enter' && pushURL()}
        placeholder="company, subject, evidence…"
        aria-label="search cases"
      />
    </label>
    <label
      >queue
      <select bind:value={queueFilter} onchange={pushURL} aria-label="queue filter">
        <option value="">all</option>
        {#each queues as queue (queue.definition.key)}
          <option value={queue.definition.key}>{queue.definition.name}</option>
        {/each}
      </select>
    </label>
    <label
      >priority
      <select bind:value={priorityFilter} onchange={pushURL} aria-label="priority filter">
        <option value="">all</option>
        <option value="critical">critical</option>
        <option value="high">high</option>
        <option value="normal">normal</option>
        <option value="low">low</option>
      </select>
    </label>
    <button onclick={pushURL}>Search</button>
    <button onclick={load}>Reload</button>
    <button onclick={exportCsv} disabled={list.length === 0} data-testid="cases-csv">
      Export CSV
    </button>
    <button
      onclick={runSweep}
      disabled={sweeping || !roleAtLeast($user?.role, 'operator')}
      title={!roleAtLeast($user?.role, 'operator')
        ? 'Requires the operator role'
        : 'Flag overdue open cases as SLA-breached'}
    >
      {sweeping ? 'Sweeping…' : 'Run SLA sweep'}
    </button>
  </div>
  <div class="row saved-views" aria-label="saved case views">
    <label
      >saved view
      <select
        aria-label="apply saved case view"
        onchange={(event) => {
          const view = savedViews.find(
            (candidate) => candidate.view_id === event.currentTarget.value
          );
          if (view) applySavedView(view);
          event.currentTarget.value = '';
        }}
      >
        <option value="">choose…</option>
        {#each savedViews as view (view.view_id)}
          <option value={view.view_id}>{view.name}</option>
        {/each}
      </select>
    </label>
    <input bind:value={savedViewName} placeholder="view name" aria-label="saved view name" />
    <button onclick={saveCurrentView} disabled={viewBusy || !savedViewName.trim()}>
      Save current filters
    </button>
    {#if savedViews.length > 0}
      <details>
        <summary>Manage {savedViews.length}</summary>
        <ul class="compact">
          {#each savedViews as view (view.view_id)}
            <li>
              <button class="link" onclick={() => applySavedView(view)}>{view.name}</button>
              <button
                class="link danger-link"
                onclick={() => removeSavedView(view)}
                disabled={viewBusy}>delete</button
              >
            </li>
          {/each}
        </ul>
      </details>
    {/if}
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <label
      >Company <input
        bind:value={company}
        placeholder="Globex Corp"
        aria-label="company name"
      /></label
    >
    <label
      >Type
      <select bind:value={caseType} aria-label="case type">
        {#each caseTypes as item (item.key)}
          <option value={item.key}>{item.definition.name} · v{item.version}</option>
        {/each}
      </select></label
    >
    <label
      >SLA days
      <input
        type="number"
        bind:value={slaDays}
        aria-label="sla days"
        min="0"
        style="width:5rem"
      /></label
    >
    <label
      >Jurisdiction
      <input bind:value={jurisdiction} placeholder="eu" aria-label="jurisdiction" /></label
    >
    <label
      >Subject
      <input bind:value={subject} placeholder="customer/123" aria-label="subject" /></label
    >
    <label class="wide"
      >Context JSON
      <textarea bind:value={contextJSON} rows="2" aria-label="case context"></textarea></label
    >
    <button
      type="submit"
      disabled={creating || !roleAtLeast($user?.role, 'operator')}
      title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
      >{creating ? 'Opening…' : 'Open case'}</button
    >
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if summary}
    <div class="summary" aria-label="queue summary">
      <span class="stat">Total <b>{summary.total}</b></span>
      <span class="stat">Needs review <b>{summary.by_status?.needs_review ?? 0}</b></span>
      <span class="stat">In progress <b>{summary.by_status?.in_progress ?? 0}</b></span>
      <span class="stat">Unassigned <b>{summary.unassigned}</b></span>
      <span class="stat due">Due soon <b>{summary.due_soon}</b></span>
      <span class="stat over">Overdue <b>{summary.overdue}</b></span>
    </div>
  {/if}
  {#if analytics}
    <div class="summary" aria-label="operational analytics" data-testid="case-analytics">
      <span class="stat">Open <b>{analytics.open}</b></span>
      <span class="stat">Unrouted <b>{analytics.unrouted}</b></span>
      <span class="stat">SLA breached <b>{analytics.sla_breached}</b></span>
      <span class="stat">QA disagreement <b>{analytics.qa_disagreements}</b></span>
      <span class="stat"
        >Avg resolution <b>{Math.round(analytics.average_resolution_seconds / 3600)}h</b></span
      >
    </div>
    <details class="analytics-detail">
      <summary>Capacity and bottlenecks</summary>
      <div class="analytics-columns">
        <section>
          <h3>Reviewer workload</h3>
          {#if analytics.workloads.length === 0}<p class="muted">No reviewer profiles.</p>{/if}
          <ul class="compact">
            {#each analytics.workloads as workload (workload.actor)}
              <li>
                {workload.actor}: {workload.open}/{workload.capacity || 'unconfigured'}
                {#if workload.capacity}
                  · {Math.round(workload.utilization * 100)}%
                {/if}
              </li>
            {/each}
          </ul>
        </section>
        <section>
          <h3>Queue backlog</h3>
          {#if analytics.queues.length === 0}<p class="muted">No queue work.</p>{/if}
          <ul class="compact">
            {#each analytics.queues as queue (queue.queue)}
              <li>
                {queue.queue}: {queue.open}/{queue.capacity || 'unconfigured'}
                {#if queue.capacity}
                  · {Math.round(queue.utilization * 100)}%{/if}
                · oldest {queue.oldest_age_hours}h
              </li>
            {/each}
          </ul>
        </section>
      </div>
    </details>
  {/if}
  {#if duplicateGroups.length > 0}
    <details class="duplicate-review" data-testid="case-duplicates">
      <summary>{duplicateGroups.length} possible duplicate group(s) need human review</summary>
      <ul>
        {#each duplicateGroups as group (group.key)}
          <li>
            <strong>{group.reason}</strong>:
            {#each group.case_ids as duplicateID, index (duplicateID)}
              {#if index > 0},
              {/if}<a href={appHref(`/cases/${duplicateID}`)}>{duplicateID}</a>
            {/each}
          </li>
        {/each}
      </ul>
    </details>
  {/if}

  {#if selectedIds.length > 0}
    <div class="row bulk" data-testid="bulk-bar">
      <span class="muted">{selectedIds.length} selected</span>
      <input bind:value={bulkAssignee} placeholder="assignee" aria-label="bulk assignee" />
      <button
        onclick={bulkAssign}
        disabled={bulkBusy || !bulkAssignee.trim() || !roleAtLeast($user?.role, 'operator')}
        title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
        >Assign</button
      >
      <button
        onclick={bulkComplete}
        disabled={bulkBusy || selectedGoverned || !roleAtLeast($user?.role, 'operator')}
        title={selectedGoverned
          ? 'Governed cases require a reasoned disposition'
          : !roleAtLeast($user?.role, 'operator')
            ? 'Requires the operator role'
            : undefined}>Mark completed</button
      >
      <select bind:value={bulkPriority} aria-label="bulk priority">
        <option value="critical">critical</option>
        <option value="high">high</option>
        <option value="normal">normal</option>
        <option value="low">low</option>
      </select>
      <button onclick={bulkSetPriority} disabled={bulkBusy || !roleAtLeast($user?.role, 'operator')}
        >Set priority</button
      >
      <input bind:value={bulkDisposition} placeholder="disposition" aria-label="bulk disposition" />
      <input bind:value={bulkReason} placeholder="reason code" aria-label="bulk reason code" />
      <button
        onclick={bulkSetDisposition}
        disabled={bulkBusy ||
          !bulkDisposition.trim() ||
          !bulkReason.trim() ||
          !roleAtLeast($user?.role, 'operator')}>Record disposition</button
      >
      <button class="link" onclick={() => (selectedIds = [])}>clear</button>
    </div>
  {/if}

  {#if loading}
    <Skeleton rows={5} />
  {:else if list.length === 0}
    <!-- Only show the onboarding empty state when the load SUCCEEDED and returned
         nothing; a failed load surfaces `error` above, so an errored queue isn't
         misrepresented as a fresh/empty one. -->
    {#if !error}
      <EmptyState icon="cases" title={emptyCopy.title} hint={emptyCopy.hint} />
    {/if}
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr
            ><th
              ><input
                type="checkbox"
                checked={allSelected}
                onchange={toggleAll}
                aria-label="select all cases"
              /></th
            ><th>Company</th><th>Type</th><th>Priority</th><th>Queue</th><th>Status</th><th
              >Assignee</th
            ><th
              ><Hint label="SLA"
                >Service-level agreement — the target time to resolve a case (set by the
                manual-review node). "Days left" counts down to that deadline; a breached case is
                flagged.</Hint
              ></th
            ><th>Days left</th></tr
          >
        </thead>
        <tbody>
          {#each sorted as c (c.case_id)}
            <tr class:sel={selectedIds.includes(c.case_id)}>
              <td
                ><input
                  type="checkbox"
                  checked={selectedIds.includes(c.case_id)}
                  onchange={() => toggle(c.case_id)}
                  aria-label={`select ${c.company_name}`}
                /></td
              >
              <td><a href={appHref(`/cases/${c.case_id}`)}>{c.company_name}</a></td>
              <td><span class="chip">{c.case_type} v{c.case_type_version}</span></td>
              <td>{c.priority}</td>
              <td>{c.queue || 'unrouted'}</td>
              <td class="status-cell"><Badge tone={caseStatusTone(c.status)}>{c.status}</Badge></td>
              <td>{c.assignee || '—'}</td>
              <td>{c.sla_days}d</td>
              <td class={isClosed(c) ? '' : `sla-${c.sla_state ?? ''}`}>
                {#if isClosed(c)}
                  <span class="muted">—</span>
                {:else if c.sla_state && c.sla_state !== 'on_track'}
                  <Badge
                    tone={slaTone(c.sla_state)}
                    title={c.sla_breached
                      ? `external alert: ${c.sla_escalation_status ?? 'pending'}`
                      : undefined}>{c.days_left}d</Badge
                  >
                {:else}
                  {c.days_left}d
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if roleAtLeast($user?.role, 'editor')}
    <details class="config">
      <summary>Case operations configuration</summary>
      <p class="muted">
        Publish immutable case schemas and routing queues. Existing cases remain pinned to the
        version under which they opened.
      </p>
      <h2>Publish case type</h2>
      <textarea bind:value={definitionJSON} rows="18" aria-label="case type definition"></textarea>
      <button onclick={publishDefinition} disabled={configBusy}>Publish next version</button>

      <h2>Configure queue</h2>
      <div class="row">
        <input bind:value={queueKey} placeholder="queue key" aria-label="queue key" />
        <input bind:value={queueName} placeholder="Queue name" aria-label="queue name" />
        <input
          type="number"
          min="0"
          bind:value={queueOrder}
          placeholder="rule order"
          aria-label="queue rule order"
        />
        <input
          bind:value={queueSkills}
          placeholder="skills, comma-separated"
          aria-label="queue skills"
        />
        <input
          bind:value={queueEscalation}
          placeholder="escalation queue"
          aria-label="escalation queue"
        />
        <input
          type="number"
          min="0"
          bind:value={queueMinAge}
          placeholder="min age h"
          aria-label="queue minimum age hours"
        />
        <input
          type="number"
          min="0"
          bind:value={queueMaxAge}
          placeholder="max age h"
          aria-label="queue maximum age hours"
        />
        <button onclick={saveQueue} disabled={configBusy || !queueKey || !queueName}
          >Save queue</button
        >
        <button onclick={rebalance} disabled={configBusy}>Rebalance work</button>
      </div>

      {#if roleAtLeast($user?.role, 'admin')}
        <h2>Configure reviewer</h2>
        <div class="row">
          <input
            bind:value={reviewerActor}
            placeholder="reviewer actor"
            aria-label="reviewer actor"
          />
          <input
            bind:value={reviewerSkills}
            placeholder="skills, comma-separated"
            aria-label="reviewer skills"
          />
          <input
            type="number"
            min="1"
            bind:value={reviewerCapacity}
            aria-label="reviewer capacity"
          />
          <button onclick={saveReviewer} disabled={configBusy || !reviewerActor}
            >Save reviewer</button
          >
        </div>
        {#if reviewers.length > 0}
          <p class="muted">{reviewers.length} active/inactive reviewer profile(s) configured.</p>
        {/if}
      {/if}
    </details>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
    align-items: center;
  }
  input,
  button,
  select,
  textarea {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    font-family: var(--font-mono);
  }
  .wide {
    flex: 1 1 100%;
    align-items: flex-start !important;
  }
  .wide textarea {
    min-width: 20rem;
  }
  .config {
    margin: 1.5rem 0;
    padding: 0.8rem;
    border: 1px solid var(--border);
    border-radius: 0.6rem;
  }
  .saved-views {
    padding: 0.45rem 0.65rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  .compact {
    margin: 0.45rem 0 0;
    padding-left: 1rem;
  }
  .duplicate-review {
    margin: 0.75rem 0;
    padding: 0.65rem 0.8rem;
    border: 1px solid var(--warn);
    border-radius: 6px;
  }
  .analytics-detail {
    margin: -0.5rem 0 1rem;
  }
  .analytics-columns {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
    gap: 1rem;
  }
  .analytics-columns h3 {
    margin-bottom: 0.25rem;
  }
  .danger-link {
    color: var(--danger) !important;
  }
  /* On a narrow phone the 7-column queue would clip its right edge (Status/Days
     left) inside the scroll wrapper, reading as broken rather than scrollable. A
     min-width forces a clean horizontal scroll and the right-edge shadow signals
     there's more to see. */
  .table-wrap {
    background:
      linear-gradient(to right, var(--surface) 30%, transparent),
      linear-gradient(to right, transparent, var(--surface) 70%) 0 100% / 100% 100%,
      radial-gradient(farthest-side at 100% 50%, rgba(20, 25, 35, 0.18), transparent);
    background-repeat: no-repeat;
    background-size:
      28px 100%,
      28px 100%,
      14px 100%;
    background-position:
      0 0,
      100% 0,
      100% 0;
    background-attachment: local, local, scroll;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    min-width: 34rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  /* Keep the status pill whole when the queue scrolls/clips on a narrow phone: a
     min-width plus nowrap means the right-edge clip falls between columns, never
     through the middle of the pill's text. */
  .status-cell {
    min-width: 7.5rem;
    white-space: nowrap;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .lede {
    color: var(--fg-muted);
    margin: 0.25rem 0 0.9rem;
    max-width: 64ch;
  }
  .summary {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    margin: 0.6rem 0 1rem;
    padding: 0.6rem 0.8rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  .stat {
    color: var(--fg-muted);
    font-size: 0.9rem;
  }
  .stat b {
    color: var(--fg);
    font-size: 1.05rem;
  }
  .stat.due b {
    color: var(--warn);
  }
  .stat.over b {
    color: var(--danger);
  }
  .sla-due_soon {
    color: var(--warn);
  }
  .sla-overdue {
    color: var(--danger);
    font-weight: 600;
  }
  .row label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin: 0;
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .bulk {
    padding: 0.5rem 0.7rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  tr.sel {
    background: color-mix(in srgb, var(--accent) 8%, transparent);
  }
  button.link {
    background: none;
    border: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0.2rem;
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
</style>
