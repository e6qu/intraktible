<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import Breadcrumb from '$lib/Breadcrumb.svelte';
  import Badge from '$lib/Badge.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';
  import {
    getAgentTemplate,
    listAgentReleases,
    createAgentRelease,
    listAgentEvalSuites,
    runAgentCampaign,
    listAgentCampaigns,
    adjudicateAgentCampaignTrial,
    compareAgentCampaigns,
    downloadAgentCampaign,
    requestAgentReleaseReview,
    reviewAgentRelease,
    deployAgentRelease,
    listAgentDeployments,
    activateAgentDeployment,
    pauseAgentDeployment,
    resumeAgentDeployment,
    retireAgentRelease,
    type AgentTemplate,
    type AgentRelease,
    type AgentEvalSuite,
    type AgentDeployment,
    type AgentCampaign,
    type AgentCampaignComparison,
    type AgentTrialAdjudication,
    type AgentReleaseSpec,
    type Environment
  } from '$lib/api';

  const key = '';
  const templateID = $derived($page.params.templateId ?? '');
  let template = $state<AgentTemplate | null>(null);
  let releases = $state<AgentRelease[]>([]);
  let suites = $state<AgentEvalSuite[]>([]);
  let deployments = $state<AgentDeployment[]>([]);
  let campaigns = $state<AgentCampaign[]>([]);
  let comparison = $state<AgentCampaignComparison | null>(null);
  let baselineCampaignID = $state('');
  let challengerCampaignID = $state('');
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');
  let releaseOpen = $state(false);
  let selectedSuite = $state('');
  let reviewer = $state('');
  let environment = $state<Environment>('sandbox');
  let deployReason = $state('');
  let activateAt = $state('');
  let expiresAt = $state('');
  let specJSON = $state(
    JSON.stringify(
      {
        instructions:
          'Assist the accountable case reviewer using only governed case context and selected evidence. Cite each factual claim. Never make the terminal decision.',
        provider: 'stub',
        model: 'governed-case-assistant',
        input_schema: { type: 'object' },
        output_schema: { type: 'object' },
        tools: [
          {
            name: 'case_evidence',
            mode: 'automatic',
            purpose: 'case_review',
            parameter_schema: { type: 'object' }
          }
        ],
        data_purposes: ['case_review'],
        dependencies: [
          {
            kind: 'policy',
            name: 'case-review-policy',
            version: '1',
            hash: '3e59b7230fd4e898745e5bf29ab298cd501d97cbfdff96424340b32694db87df'
          }
        ],
        budget: {
          max_prompt_tokens: 8000,
          max_completion_tokens: 1500,
          max_tool_calls: 3,
          max_cost_usd: 0.25,
          input_cost_per_mtok: 3,
          output_cost_per_mtok: 15,
          pricing_source: 'provider-contract',
          pricing_version: '2026-07',
          period: 'day',
          period_cost_usd: 100
        },
        timeout_ms: 60000,
        max_attempts: 2,
        circuit_breaker: {
          window_minutes: 15,
          min_samples: 20,
          failure_rate: 0.5
        },
        require_citations: true,
        require_human_gate: true,
        allow_remote_agent: false
      },
      null,
      2
    )
  );

  async function load() {
    loading = true;
    error = '';
    try {
      [template, releases, suites, deployments] = await Promise.all([
        getAgentTemplate(key, templateID),
        listAgentReleases(key, templateID),
        listAgentEvalSuites(key),
        listAgentDeployments(key)
      ]);
      campaigns = (
        await Promise.all(
          releases.map((release) => listAgentCampaigns(key, templateID, release.release))
        )
      ).flat();
      if (!baselineCampaignID && campaigns[1]) baselineCampaignID = campaigns[1].campaign_id;
      if (!challengerCampaignID && campaigns[0]) challengerCampaignID = campaigns[0].campaign_id;
      if (!selectedSuite && suites[0]) selectedSuite = `${suites[0].suite_id}@${suites[0].version}`;
      if (releases.length === 0) releaseOpen = true;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  async function createRelease() {
    busy = 'release';
    try {
      await createAgentRelease(key, templateID, JSON.parse(specJSON) as AgentReleaseSpec);
      releaseOpen = false;
      toast.success('Immutable release created');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  function suiteSelection(): [string, number] {
    const index = selectedSuite.lastIndexOf('@');
    if (index < 1) throw new Error('Select an evaluation suite');
    return [selectedSuite.slice(0, index), Number(selectedSuite.slice(index + 1))];
  }

  async function evaluate(release: AgentRelease) {
    busy = `eval-${release.release}`;
    try {
      const [suiteID, version] = suiteSelection();
      const campaign = await runAgentCampaign(key, templateID, release.release, suiteID, version);
      if (campaign.blocking) toast.error('Evaluation recorded: release is blocked');
      else toast.success(`${campaign.passed}/${campaign.total} repeated trials passed`);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function adjudicate(
    campaign: AgentCampaign,
    trial: AgentCampaign['trials'][number],
    passed: boolean
  ) {
    const reason = prompt(
      `Why should ${trial.case_id} trial ${trial.trial} be adjudicated ${passed ? 'pass' : 'fail'}?`
    )?.trim();
    if (!reason) return;
    busy = `adjudicate-${campaign.campaign_id}-${trial.case_id}-${trial.trial}`;
    try {
      await adjudicateAgentCampaignTrial(
        key,
        campaign.campaign_id,
        trial.case_id,
        trial.trial,
        passed,
        reason
      );
      toast.success('Independent judgment recorded; original trial evidence is unchanged');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function compare() {
    if (!baselineCampaignID || !challengerCampaignID) return;
    busy = 'compare';
    try {
      comparison = await compareAgentCampaigns(key, baselineCampaignID, challengerCampaignID);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function exportCampaign(campaign: AgentCampaign, format: 'json' | 'csv') {
    busy = `export-${campaign.campaign_id}`;
    try {
      const blob = await downloadAgentCampaign(key, campaign.campaign_id, format);
      const href = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = href;
      anchor.download = `agent-campaign-${campaign.campaign_id}.${format}`;
      anchor.click();
      URL.revokeObjectURL(href);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  function campaignForRelease(release: number): AgentCampaign[] {
    return campaigns.filter((campaign) => campaign.release === release);
  }

  function adjudicationFor(
    campaign: AgentCampaign,
    caseID: string,
    trial: number
  ): AgentTrialAdjudication | undefined {
    return campaign.adjudications?.find(
      (adjudication) => adjudication.case_id === caseID && adjudication.trial === trial
    );
  }

  async function requestReview(release: AgentRelease) {
    if (release.campaign_ids.length === 0) return;
    busy = `review-request-${release.release}`;
    try {
      await requestAgentReleaseReview(
        key,
        templateID,
        release.release,
        release.campaign_ids,
        release.campaign_ids.map((id) => `evaluation:${id}`),
        reviewer.trim() ? [reviewer.trim()] : [],
        new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString()
      );
      toast.success('Independent review requested; expires in 24 hours');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function decide(release: AgentRelease, decision: 'approve' | 'reject') {
    if (!release.review) return;
    const reason = prompt(`Reason to ${decision} release ${release.release}?`)?.trim();
    if (!reason) return;
    busy = `review-${release.release}`;
    try {
      await reviewAgentRelease(
        key,
        templateID,
        release.release,
        release.review.request_id,
        decision,
        reason
      );
      toast.success(`Release ${decision === 'approve' ? 'approved' : 'rejected'}`);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function deploy(release: AgentRelease) {
    if (!deployReason.trim()) {
      toast.error('A deployment reason is required');
      return;
    }
    busy = `deploy-${release.release}`;
    try {
      const deployment = await deployAgentRelease(key, {
        template_id: templateID,
        release: release.release,
        environment,
        at: activateAt ? new Date(activateAt).toISOString() : undefined,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
        reason: deployReason.trim()
      });
      if (!activateAt) {
        await activateAgentDeployment(key, deployment.deployment_id);
      }
      toast.success(activateAt ? 'Deployment scheduled' : 'Deployment activated');
      deployReason = '';
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function pause(deployment: AgentDeployment) {
    const reason = prompt('Why are you pausing this deployment?')?.trim();
    if (!reason) return;
    busy = `deploy-${deployment.release}`;
    try {
      await pauseAgentDeployment(key, deployment.deployment_id, reason);
      toast.success('Deployment paused');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function resume(deployment: AgentDeployment) {
    const reason = prompt(
      'Why is it safe to resume? Resolve the critical incident before continuing.'
    )?.trim();
    if (!reason) return;
    busy = `deploy-${deployment.release}`;
    try {
      await resumeAgentDeployment(key, deployment.deployment_id, reason);
      toast.success('Deployment resumed with a fresh circuit window');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  async function retire(release: AgentRelease) {
    const reason = prompt(`Why retire release ${release.release}?`)?.trim();
    if (!reason) return;
    busy = `retire-${release.release}`;
    try {
      await retireAgentRelease(key, templateID, release.release, reason);
      toast.success('Release retired');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = '';
    }
  }

  function binding(release: number): AgentDeployment | undefined {
    return deployments.find(
      (deployment) => deployment.template_id === templateID && deployment.release === release
    );
  }

  $effect(() => {
    void templateID;
    void load();
  });
</script>

<main>
  <Breadcrumb
    sectionHref="/agents/governance"
    sectionLabel="Governed agents"
    current={template?.name ?? templateID}
  />
  {#if loading}
    <Skeleton rows={6} />
  {:else if error}
    <p class="err">{error}</p>
  {:else if template}
    <header>
      <div>
        <p class="eyebrow">{template.slug}</p>
        <h1>{template.name}</h1>
        <p class="lede">{template.task}</p>
      </div>
      {#if template.high_impact}<Badge tone="warn">high impact · human accountable</Badge>{/if}
    </header>

    <div class="controls">
      <label>
        Evaluation suite
        <select bind:value={selectedSuite}>
          {#each suites as suite (`${suite.suite_id}-${suite.version}`)}
            <option value={`${suite.suite_id}@${suite.version}`}>
              {suite.name} · v{suite.version}{suite.required ? ' · required' : ''}
            </option>
          {/each}
        </select>
      </label>
      <label
        >Assigned reviewer <input
          bind:value={reviewer}
          placeholder="checker actor (optional)"
        /></label
      >
      <button
        onclick={() => (releaseOpen = !releaseOpen)}
        disabled={!roleAtLeast($user?.role, 'editor')}
      >
        + Create release
      </button>
    </div>

    {#if releaseOpen}
      <form
        class="panel"
        onsubmit={(event) => {
          event.preventDefault();
          createRelease();
        }}
      >
        <div>
          <h2>Immutable release specification</h2>
          <p class="muted">
            Prompt, provider/model, schemas, tool grants, purposes, dependency pins, budgets, retry,
            and human controls are reviewed together.
          </p>
        </div>
        <textarea bind:value={specJSON} rows="24" aria-label="release specification JSON"
        ></textarea>
        <button disabled={busy === 'release'}
          >{busy === 'release' ? 'Creating…' : 'Create draft release'}</button
        >
      </form>
    {/if}

    <h2>Release history</h2>
    {#each releases as release (release.release)}
      {@const deployed = binding(release.release)}
      <article class="release">
        <div class="release-head">
          <div>
            <h3>Release {release.release}</h3>
            <span class="hash" title={release.spec_hash}>{release.spec_hash.slice(0, 12)}</span>
          </div>
          <div class="badges">
            <Badge
              tone={release.status === 'approved'
                ? 'ok'
                : release.status === 'rejected' || release.status === 'retired'
                  ? 'danger'
                  : release.status === 'review_requested'
                    ? 'warn'
                    : 'neutral'}>{release.status.replace('_', ' ')}</Badge
            >
            {#if deployed}<Badge tone={deployed.status === 'active' ? 'ok' : 'warn'}
                >{deployed.environment} · {deployed.status}</Badge
              >{/if}
          </div>
        </div>
        <div class="facts">
          <span>Provider <b>{release.spec.provider}</b></span>
          <span>Model <b>{release.spec.model}</b></span>
          <span>Tools <b>{release.spec.tools.length}</b></span>
          <span>Per-run budget <b>${release.spec.budget.max_cost_usd}</b></span>
          {#if release.spec.circuit_breaker}
            <span>
              Circuit
              <b>
                {Math.round(release.spec.circuit_breaker.failure_rate * 100)}% /
                {release.spec.circuit_breaker.min_samples} samples /
                {release.spec.circuit_breaker.window_minutes}m
              </b>
            </span>
          {/if}
          <span>Created <b><RelativeTime value={release.created_at} /></b></span>
        </div>
        {#if release.review}
          <div class="review">
            <strong>Review evidence</strong>
            {#if release.review.expired_at}
              <span class="err"
                >Expired <RelativeTime value={release.review.expired_at} />. Fresh evaluation
                evidence and a new review request are required.</span
              >
            {:else}
              <span
                >{release.review.campaign_ids.length} campaign(s) · expires <RelativeTime
                  value={release.review.expires_at}
                /></span
              >
            {/if}
            {#if release.review.reviewed_by}<span
                >{release.review.decision} by {release.review.reviewed_by}: {release.review
                  .reason}</span
              >{/if}
          </div>
        {/if}
        {#each campaignForRelease(release.release) as campaign (campaign.campaign_id)}
          {@const assessment = campaign.assessment ?? campaign}
          <details class="campaign">
            <summary>
              <span>
                Evaluation {campaign.suite_id}@{campaign.suite_version} ·
                {assessment.passed}/{assessment.total} effective trials
              </span>
              <Badge tone={assessment.blocking ? 'danger' : 'ok'}
                >{assessment.blocking ? 'blocking' : 'passing'}</Badge
              >
            </summary>
            <p class="muted">
              Original provider evidence: {campaign.passed}/{campaign.total}. Effective pass rate
              {Math.round(assessment.pass_rate * 100)}% (95% CI {Math.round(
                assessment.confidence_low * 100
              )}–{Math.round(assessment.confidence_high * 100)}%). Adjudication layers human
              judgment over the immutable output hash; it never rewrites the trial.
            </p>
            <div class="actions">
              <button onclick={() => exportCampaign(campaign, 'json')} disabled={busy !== ''}
                >Export reproducible JSON</button
              >
              <button onclick={() => exportCampaign(campaign, 'csv')} disabled={busy !== ''}
                >Export trial CSV</button
              >
            </div>
            <div class="trial-list">
              {#each campaign.trials as trial (`${trial.case_id}-${trial.trial}`)}
                {@const adjudication = adjudicationFor(campaign, trial.case_id, trial.trial)}
                <div class="trial">
                  <span>
                    <code>{trial.case_id}/{trial.trial}</code>
                    <Badge tone={trial.passed ? 'ok' : 'danger'}
                      >original {trial.status.replaceAll('_', ' ')}</Badge
                    >
                    {#if adjudication}
                      <Badge tone={adjudication.passed ? 'ok' : 'danger'}
                        >adjudicated {adjudication.passed ? 'pass' : 'fail'}</Badge
                      >
                    {/if}
                  </span>
                  <small>
                    {trial.detail ??
                      (trial.grade?.kind === 'semantic'
                        ? 'Semantic grader passed.'
                        : 'Deterministic grader passed.')}
                  </small>
                  {#if trial.grade}
                    <small>
                      {trial.grade.kind} grader
                      {#if trial.grade.kind === 'semantic'}
                        · score {Math.round((trial.grade.score ?? 0) * 100)}% · {trial.grade
                          .provider}/{trial.grade.model}
                        · ${(trial.grade.cost_usd ?? 0).toFixed(4)}
                      {/if}
                      · evidence
                      <code title={trial.grade.grader_hash}
                        >{trial.grade.grader_hash.slice(0, 12)}…</code
                      >
                    </small>
                  {/if}
                  {#if adjudication}
                    <small>
                      {adjudication.reason} — {adjudication.adjudicated_by},
                      <RelativeTime value={adjudication.adjudicated_at} />
                    </small>
                  {:else if release.status === 'evaluated'}
                    <span class="actions">
                      <button
                        onclick={() => adjudicate(campaign, trial, true)}
                        disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}
                        >Adjudicate pass</button
                      >
                      <button
                        onclick={() => adjudicate(campaign, trial, false)}
                        disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}
                        >Adjudicate fail</button
                      >
                    </span>
                  {/if}
                </div>
              {/each}
            </div>
          </details>
        {/each}
        <div class="actions">
          {#if deployed && (deployed.status === 'active' || deployed.status === 'scheduled')}
            <button
              onclick={() => pause(deployed)}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}
              >Pause deployment</button
            >
          {/if}
          {#if deployed?.status === 'paused'}
            <button
              onclick={() => resume(deployed)}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}
              >Resume after incident resolution</button
            >
          {/if}
          {#if release.status === 'draft' || release.status === 'evaluated'}
            <button
              onclick={() => evaluate(release)}
              disabled={busy !== '' || !selectedSuite || !roleAtLeast($user?.role, 'operator')}
            >
              {busy === `eval-${release.release}` ? 'Running repeated trials…' : 'Run evaluation'}
            </button>
          {/if}
          {#if release.status === 'evaluated'}
            <button
              onclick={() => requestReview(release)}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'editor')}
            >
              Request independent review
            </button>
          {/if}
          {#if release.status === 'review_requested'}
            <button
              onclick={() => decide(release, 'approve')}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}>Approve</button
            >
            <button
              class="danger"
              onclick={() => decide(release, 'reject')}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}>Reject</button
            >
          {/if}
          {#if release.status !== 'retired' && (!deployed || deployed.status === 'paused')}
            <button
              class="danger"
              onclick={() => retire(release)}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}>Retire release</button
            >
          {/if}
        </div>
        {#if release.status === 'approved' && !deployed}
          <div class="deploy">
            <label
              >Environment <select bind:value={environment}
                ><option>sandbox</option><option>staging</option><option>production</option></select
              ></label
            >
            <label>Activate at <input type="datetime-local" bind:value={activateAt} /></label>
            <label>Expire at <input type="datetime-local" bind:value={expiresAt} /></label>
            <label class="reason"
              >Reason <input
                bind:value={deployReason}
                placeholder="Why this exact release belongs here"
              /></label
            >
            <button
              onclick={() => deploy(release)}
              disabled={busy !== '' || !roleAtLeast($user?.role, 'approver')}
              >Deploy exact release</button
            >
          </div>
        {/if}
      </article>
    {/each}
    {#if campaigns.length >= 2}
      <section class="panel">
        <div>
          <p class="eyebrow">Paired evidence</p>
          <h2>Baseline / challenger comparison</h2>
          <p class="muted">
            Campaigns must use the exact same immutable suite version. The comparison shows per-case
            regressions, uncertainty, latency, and spend.
          </p>
        </div>
        <div class="controls">
          <label>
            Baseline
            <select bind:value={baselineCampaignID}>
              {#each campaigns as campaign (campaign.campaign_id)}
                <option value={campaign.campaign_id}>
                  r{campaign.release} · {campaign.suite_id}@{campaign.suite_version}
                </option>
              {/each}
            </select>
          </label>
          <label>
            Challenger
            <select bind:value={challengerCampaignID}>
              {#each campaigns as campaign (campaign.campaign_id)}
                <option value={campaign.campaign_id}>
                  r{campaign.release} · {campaign.suite_id}@{campaign.suite_version}
                </option>
              {/each}
            </select>
          </label>
          <button
            onclick={compare}
            disabled={busy !== '' || baselineCampaignID === challengerCampaignID}
            >Compare exact campaigns</button
          >
        </div>
        {#if comparison}
          <div class="facts">
            <span>Pass-rate delta <b>{Math.round(comparison.delta_pass_rate * 100)} pp</b></span>
            <span>
              95% interval
              <b>
                {Math.round(comparison.delta_confidence_low * 100)} to {Math.round(
                  comparison.delta_confidence_high * 100
                )} pp
              </b>
            </span>
            <span>Regressions <b>{comparison.regressions}</b></span>
            <span>Improvements <b>{comparison.improvements}</b></span>
            <span>
              Cost
              <b>
                ${comparison.baseline_cost_usd.toFixed(4)} → ${comparison.challenger_cost_usd.toFixed(
                  4
                )}
              </b>
            </span>
          </div>
          <div class="trial-list">
            {#each comparison.rows as row (row.case_id)}
              <div class="trial">
                <code>{row.case_id}</code>
                <span>
                  {Math.round(row.baseline_pass_rate * 100)}% →
                  {Math.round(row.challenger_pass_rate * 100)}%
                </span>
                {#if row.regression}<Badge tone="danger">regression</Badge>{/if}
              </div>
            {/each}
          </div>
        {/if}
      </section>
    {/if}
  {/if}
</main>

<style>
  main {
    max-width: 66rem;
    margin: 2rem auto;
    padding: 0 1.25rem 4rem;
  }
  header,
  .release-head,
  .controls,
  .actions,
  .badges {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    flex-wrap: wrap;
  }
  .eyebrow {
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--accent-ink);
    font-size: 0.72rem;
    margin: 0;
  }
  .lede,
  .muted {
    color: var(--muted);
  }
  .controls {
    margin: 1.5rem 0;
    padding: 1rem;
    border: 1px solid var(--border);
    border-radius: 0.6rem;
    align-items: end;
  }
  label {
    display: grid;
    gap: 0.3rem;
    font-size: 0.78rem;
  }
  .panel,
  .release {
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: 0.65rem;
    padding: 1rem;
    margin: 1rem 0;
  }
  .panel {
    display: grid;
    gap: 0.8rem;
  }
  textarea {
    width: 100%;
    font-family: var(--font-mono);
  }
  .release h3 {
    margin: 0;
  }
  .hash {
    font-family: var(--font-mono);
    font-size: 0.72rem;
    color: var(--muted);
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
    gap: 0.6rem;
    margin: 1rem 0;
  }
  .facts span {
    border-left: 2px solid var(--border);
    padding-left: 0.6rem;
    color: var(--muted);
    font-size: 0.78rem;
  }
  .facts b {
    display: block;
    color: var(--text);
    margin-top: 0.2rem;
  }
  .review {
    display: grid;
    gap: 0.3rem;
    padding: 0.8rem;
    background: var(--surface-raised);
    border-radius: 0.5rem;
    font-size: 0.82rem;
  }
  .campaign {
    margin-top: 1rem;
    padding: 0.8rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  .campaign summary,
  .trial,
  .trial > span {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
  }
  .trial-list {
    display: grid;
    gap: 0.4rem;
    margin-top: 0.8rem;
  }
  .trial {
    padding: 0.55rem;
    background: var(--surface-raised);
    border-radius: 0.4rem;
    font-size: 0.78rem;
  }
  .actions {
    justify-content: flex-start;
    margin-top: 1rem;
  }
  .deploy {
    display: grid;
    grid-template-columns: repeat(4, 1fr) auto;
    gap: 0.6rem;
    align-items: end;
    border-top: 1px solid var(--border);
    margin-top: 1rem;
    padding-top: 1rem;
  }
  .deploy .reason {
    grid-column: span 2;
  }
  button.danger {
    color: var(--danger);
  }
  .err {
    color: var(--danger);
  }
  @media (max-width: 800px) {
    .deploy {
      grid-template-columns: 1fr;
    }
    .deploy .reason {
      grid-column: auto;
    }
  }
</style>
