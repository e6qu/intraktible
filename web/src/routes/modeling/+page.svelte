<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Badge from '$lib/Badge.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { toast } from '$lib/toast';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import {
    listSourceSchemas,
    defineSourceSchema,
    requestSourceSchemaApproval,
    decideSourceSchemaApproval,
    retireSourceSchema,
    listQualityIncidents,
    acknowledgeQualityIncident,
    resolveQualityIncident,
    listSourceHealth,
    listGovernedFeatures,
    listDatasets,
    defineDataset,
    requestDatasetSnapshot,
    listDatasetSnapshots,
    downloadDatasetSnapshot,
    listModelingJobs,
    pauseModelingJob,
    resumeModelingJob,
    retryModelingJob,
    cancelModelingJob,
    requestModelTraining,
    requestModelEvaluation,
    listModelArtifacts,
    registerExternalArtifact,
    changeModelArtifactStage,
    verifyModelArtifact,
    listModelEvaluations,
    requestFeatureBackfill,
    listMaterializations,
    getModelLineage,
    compareGovernedModels,
    type SourceSchema,
    type SourceSchemaSpec,
    type QualityIncident,
    type SourceHealth,
    type GovernedFeature,
    type Dataset,
    type DatasetSpec,
    type DatasetSnapshot,
    type ModelingJob,
    type ModelArtifact,
    type ExternalArtifactRegistration,
    type ModelEvaluation,
    type Materialization,
    type BinaryEvaluation,
    type EventAck
  } from '$lib/api';

  const key = '';
  let loading = $state(true);
  let error = $state('');
  let busy = $state('');
  let schemas = $state<SourceSchema[]>([]);
  let incidents = $state<QualityIncident[]>([]);
  let health = $state<SourceHealth[]>([]);
  let governedFeatures = $state<GovernedFeature[]>([]);
  let datasets = $state<Dataset[]>([]);
  let snapshots = $state<DatasetSnapshot[]>([]);
  let jobs = $state<ModelingJob[]>([]);
  let artifacts = $state<ModelArtifact[]>([]);
  let evaluations = $state<ModelEvaluation[]>([]);
  let materializations = $state<Materialization[]>([]);
  let loadSeq = 0;

  const openIncidents = $derived(incidents.filter((item) => item.status !== 'resolved'));
  const runningJobs = $derived(
    jobs.filter((job) => ['queued', 'running', 'pausing', 'cancelling'].includes(job.state))
  );
  const availableSnapshots = $derived(snapshots.filter((item) => item.state !== 'expired'));
  const trainedArtifacts = $derived(
    artifacts.filter((item) => item.origin === 'platform_trained' && item.stage !== 'archived')
  );

  function message(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  async function load(showLoading = true): Promise<void> {
    const seq = ++loadSeq;
    if (showLoading) loading = true;
    error = '';
    try {
      const result = await Promise.all([
        listSourceSchemas(key),
        listQualityIncidents(key),
        listSourceHealth(key),
        listGovernedFeatures(key),
        listDatasets(key),
        listDatasetSnapshots(key),
        listModelingJobs(key),
        listModelArtifacts(key),
        listModelEvaluations(key),
        listMaterializations(key)
      ]);
      if (seq !== loadSeq) return;
      [
        schemas,
        incidents,
        health,
        governedFeatures,
        datasets,
        snapshots,
        jobs,
        artifacts,
        evaluations,
        materializations
      ] = result;
    } catch (reason) {
      if (seq === loadSeq) error = message(reason);
    } finally {
      if (seq === loadSeq && showLoading) loading = false;
    }
  }

  async function act(label: string, action: () => Promise<unknown>): Promise<void> {
    if (busy) return;
    busy = label;
    try {
      await action();
      toast.success(label);
      await load();
    } catch (reason) {
      toast.error(message(reason));
    } finally {
      busy = '';
    }
  }

  async function saveSnapshot(
    snapshotId: string,
    datasetName: string,
    format: 'json' | 'csv'
  ): Promise<void> {
    const blob = await downloadDatasetSnapshot(key, snapshotId, format);
    const href = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = href;
    anchor.download = `${datasetName}-${snapshotId}.${format}`;
    anchor.click();
    URL.revokeObjectURL(href);
  }

  let schemaJSON = $state(`{
  "ref": { "kind": "event", "entity_type": "applicant", "event_name": "application" },
  "description": "Application source contract",
  "owner_team": "data-platform",
  "purposes": ["underwriting"],
  "compatibility": "backward",
  "additional_properties": false,
  "fields": [
    { "name": "amount", "type": "number", "required": true, "classification": "confidential" }
  ],
  "quality": { "action": "refer", "completeness_min": 1, "freshness_seconds": 3600 }
}`);
  let datasetJSON = $state(`{
  "name": "credit-risk",
  "description": "Point-in-time credit risk cohort",
  "owner_team": "risk-science",
  "entity_type": "applicant",
  "features": ["transaction_sum_72h"],
  "label": {
    "event_name": "outcome",
    "field": "defaulted",
    "kind": "binary",
    "positive_value": "true",
    "horizon_hours": 720
  },
  "segment_fields": ["region"],
  "purpose": "model-development",
  "consent_requirement": { "mode": "not_required" },
  "retention_days": 90,
  "partitions": { "train_bps": 7000, "validation_bps": 1500, "test_bps": 1500 }
}`);
  let snapshotDataset = $state('');
  let snapshotVersion = $state(1);
  let observationAt = $state('');
  let knowledgeAt = $state('');
  let trainingModel = $state('');
  let trainingSnapshot = $state('');
  let codeRevision = $state('');
  let evaluationArtifact = $state('');
  let evaluationSnapshot = $state('');
  let externalArtifactJSON = $state(`{
  "artifact_id": "external-credit-v1",
  "model_name": "external-credit",
  "owner_team": "risk-science",
  "format": "onnx",
  "runtime": "onnxruntime-1.19",
  "size_bytes": 1048576,
  "artifact_hash": "<64-character SHA-256 hex>",
  "signature": "<base64url Ed25519 signature over decoded artifact_hash>",
  "public_key": "<base64url Ed25519 public key>",
  "storage_ref": "s3://governed-models/external-credit-v1.onnx",
  "source_revision": "git:0123456789ab",
  "build_id": "build-2026-001",
  "sbom_hash": "<64-character SHA-256 hex>",
  "dependencies": [{ "name": "onnxruntime", "version": "1.19.0" }],
  "vulnerability": {
    "scanner": "trivy",
    "scanner_version": "0.58.0",
    "scanned_at": "2026-01-01T00:00:00Z",
    "report_hash": "<64-character SHA-256 hex>",
    "critical": 0,
    "high": 0
  },
  "explanation": {
    "local_supported": false,
    "global_supported": false,
    "limitations": "No platform-verified faithful explanation adapter is configured."
  },
  "purpose": "underwriting",
  "retention_until": "2027-01-01T00:00:00Z"
}`);
  let backfillEntity = $state('');
  let backfillFeatures = $state('');
  let backfillAsOf = $state('');
  let backfillKnowledge = $state('');
  let champion = $state('');
  let challenger = $state('');
  let comparison = $state<{
    same_snapshot: boolean;
    auc_delta: number;
    brier_delta: number;
    accuracy_delta: number;
    champion_evaluation: BinaryEvaluation;
    challenger_evaluation: BinaryEvaluation;
  } | null>(null);
  let lineageModel = $state('');
  let lineage = $state<Record<string, unknown> | null>(null);

  function parseObject<T>(raw: string, label: string): T {
    const value = JSON.parse(raw) as unknown;
    if (value === null || Array.isArray(value) || typeof value !== 'object') {
      throw new Error(`${label} must be a JSON object.`);
    }
    return value as T;
  }

  function utc(value: string, label: string): string {
    if (!value) throw new Error(`${label} is required.`);
    const parsed = new Date(value);
    if (Number.isNaN(parsed.valueOf())) throw new Error(`${label} is not a valid timestamp.`);
    return parsed.toISOString();
  }

  function activeVersion(schema: SourceSchema) {
    return schema.versions.find((version) => version.version === schema.active_version);
  }

  function decidePending(schema: SourceSchema, approve: boolean): Promise<EventAck> {
    if (!schema.pending) throw new Error('The schema no longer has a pending review.');
    return decideSourceSchemaApproval(
      key,
      schema.pending.request_id,
      schema.ref,
      approve,
      approve ? 'Independent contract review passed' : 'Contract requires revision'
    );
  }

  function requestLatest(schema: SourceSchema): Promise<{ request_id: string }> {
    const latest = schema.versions.at(-1);
    if (!latest) throw new Error('The schema has no version to review.');
    return requestSourceSchemaApproval(key, schema.ref, latest.version);
  }

  onMount(() => void load());

  $effect(() => {
    if (loading || runningJobs.length === 0) return;
    const timer = window.setTimeout(() => void load(false), 1000);
    return () => window.clearTimeout(timer);
  });
</script>

<svelte:head
  ><meta name="description" content="Governed model-data science control plane" /></svelte:head
>

<main class="page-shell" data-testid="modeling-page">
  <header class="page-head">
    <div>
      <p class="eyebrow">Model data science</p>
      <h1>Modeling cockpit</h1>
      <p class="lede">
        Govern source contracts, point-in-time datasets, reproducible jobs, signed artifacts,
        independent validation, and complete production lineage.
      </p>
    </div>
    <button class="secondary" disabled={loading} onclick={() => void load()}>Reload evidence</button
    >
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  <section class="kpis" aria-label="Modeling health">
    <article>
      <span>Active schemas</span><strong
        >{schemas.filter((s) => s.active_version > 0).length}</strong
      >
    </article>
    <article class:danger={openIncidents.length > 0}>
      <span>Open quality incidents</span><strong>{openIncidents.length}</strong>
    </article>
    <article><span>Available snapshots</span><strong>{availableSnapshots.length}</strong></article>
    <article class:warn={runningJobs.length > 0}>
      <span>Jobs in flight</span><strong>{runningJobs.length}</strong>
    </article>
    <article><span>Signed artifacts</span><strong>{artifacts.length}</strong></article>
  </section>

  {#if loading}
    <Skeleton rows={8} />
  {:else}
    <section class="panel" id="source-contracts">
      <div class="section-head">
        <div>
          <p class="eyebrow">Contract → admission → incident</p>
          <h2>Sources & quality</h2>
        </div>
        <Badge tone={openIncidents.length ? 'danger' : 'ok'}>{openIncidents.length} open</Badge>
      </div>
      <div class="split">
        <div>
          <h3>Governed schemas</h3>
          {#if schemas.length === 0}
            <EmptyState
              title="No source contracts"
              hint="Define a version, then send it to an independent checker."
            />
          {:else}
            <div class="cards">
              {#each schemas as schema (schema.ref.kind + schema.ref.entity_type + (schema.ref.event_name ?? ''))}
                {@const active = activeVersion(schema)}
                <article class="card">
                  <div class="row">
                    <strong>{schema.ref.event_name ?? schema.ref.entity_type}</strong>
                    <Badge tone={active ? 'ok' : schema.pending ? 'warn' : 'neutral'}>
                      {active ? `active v${active.version}` : schema.pending ? 'pending' : 'draft'}
                    </Badge>
                  </div>
                  <p>
                    {schema.ref.kind} · {active?.spec.owner_team ??
                      schema.versions.at(-1)?.spec.owner_team}
                  </p>
                  <small
                    >{active?.hash.slice(0, 16) ??
                      schema.versions.at(-1)?.hash.slice(0, 16)}…</small
                  >
                  {#if schema.pending && roleAtLeast($user?.role, 'approver')}
                    <div class="actions">
                      <button
                        disabled={!!busy}
                        onclick={() =>
                          void act('Schema approved', () => decidePending(schema, true))}
                        >Approve</button
                      >
                      <button
                        class="danger-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Schema rejected', () => decidePending(schema, false))}
                        >Reject</button
                      >
                    </div>
                  {:else if !schema.pending && schema.versions.length > 0 && roleAtLeast($user?.role, 'editor')}
                    <button
                      class="text-btn"
                      disabled={!!busy}
                      onclick={() => void act('Schema sent to review', () => requestLatest(schema))}
                      >Request review for v{schema.versions.at(-1)?.version}</button
                    >
                  {/if}
                  {#if active && roleAtLeast($user?.role, 'approver')}
                    <button
                      class="text-btn"
                      disabled={!!busy}
                      onclick={() =>
                        void act('Schema retired', () =>
                          retireSourceSchema(
                            key,
                            schema.ref,
                            active.version,
                            'Superseded source contract'
                          )
                        )}>Retire active version</button
                    >
                  {/if}
                </article>
              {/each}
            </div>
          {/if}
        </div>
        {#if roleAtLeast($user?.role, 'editor')}
          <form
            onsubmit={(event) => {
              event.preventDefault();
              void act('Schema version defined', () =>
                defineSourceSchema(key, parseObject<SourceSchemaSpec>(schemaJSON, 'Schema'))
              );
            }}
          >
            <h3>Define immutable version</h3>
            <label>Schema JSON<textarea bind:value={schemaJSON} rows="15"></textarea></label>
            <button disabled={!!busy}>Define schema version</button>
          </form>
        {/if}
      </div>

      <div class="split lower">
        <div>
          <h3>Open incidents</h3>
          {#if openIncidents.length === 0}
            <EmptyState
              title="Quality queue clear"
              hint="Record violations and stale source episodes appear here."
            />
          {:else}
            <div class="cards">
              {#each openIncidents as incident (incident.observation_id)}
                <article class="card danger-edge">
                  <div class="row">
                    <strong>{incident.ref.event_name ?? incident.ref.entity_type}</strong><Badge
                      tone="danger">{incident.incident_type}</Badge
                    >
                  </div>
                  <p>{incident.violations.map((item) => item.message).join(' · ')}</p>
                  <p class="muted">
                    {incident.owner_team} owns {incident.affected_assets.join(', ')} ·
                    {incident.affected_subjects.length} affected subject{incident.affected_subjects
                      .length === 1
                      ? ''
                      : 's'}
                  </p>
                  <small
                    ><Badge tone={incident.severity === 'critical' ? 'danger' : 'warn'}
                      >{incident.severity}</Badge
                    >
                    observed <RelativeTime value={incident.observed_at} /> · {incident.status}</small
                  >
                  {#if roleAtLeast($user?.role, 'operator')}
                    {#if incident.status === 'open'}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Incident acknowledged', () =>
                            acknowledgeQualityIncident(
                              key,
                              incident.observation_id,
                              'Operator accepted ownership and began triage'
                            )
                          )}>Acknowledge ownership</button
                      >
                    {:else}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Incident resolved', () =>
                            resolveQualityIncident(
                              key,
                              incident.observation_id,
                              'Operator verified remediation and downstream impact'
                            )
                          )}>Resolve with evidence</button
                      >
                    {/if}
                  {/if}
                </article>
              {/each}
            </div>
          {/if}
        </div>
        <div>
          <h3>Source watermarks</h3>
          <div class="table-wrap">
            <table>
              <thead
                ><tr
                  ><th>Source</th><th>Records</th><th>Late</th><th>Corrections</th><th>Lag</th></tr
                ></thead
              >
              <tbody>
                {#each health as source (source.ref.kind + source.ref.entity_type + (source.ref.event_name ?? ''))}
                  <tr>
                    <td>{source.ref.event_name ?? source.ref.entity_type}</td>
                    <td>{source.record_count}</td><td>{source.late_count}</td>
                    <td>{source.correction_count + source.retraction_count}</td>
                    <td>{source.watermark_lag_seconds ?? 0}s</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </section>

    <section class="panel" id="datasets">
      <div class="section-head">
        <div>
          <p class="eyebrow">Observation time × knowledge time</p>
          <h2>Datasets & materialization</h2>
        </div>
        <Badge tone="neutral">{snapshots.length} snapshots</Badge>
      </div>
      <div class="split">
        <div>
          <h3>Dataset registry</h3>
          <div class="cards">
            {#each datasets as dataset (dataset.name)}
              <article class="card">
                <div class="row">
                  <strong>{dataset.name}</strong><Badge tone="neutral"
                    >v{dataset.versions.at(-1)?.version}</Badge
                  >
                </div>
                <p>{dataset.versions.at(-1)?.spec.purpose}</p>
                <small
                  >{dataset.versions.at(-1)?.spec.features.length} features · {dataset.versions.at(
                    -1
                  )?.spec.retention_days}d retention</small
                >
              </article>
            {/each}
          </div>
          {#if roleAtLeast($user?.role, 'editor')}
            <form
              onsubmit={(event) => {
                event.preventDefault();
                void act('Dataset version defined', () =>
                  defineDataset(key, parseObject<DatasetSpec>(datasetJSON, 'Dataset'))
                );
              }}
            >
              <label>Dataset JSON<textarea bind:value={datasetJSON} rows="13"></textarea></label>
              <button disabled={!!busy}>Define dataset version</button>
            </form>
          {/if}
        </div>
        <div>
          {#if roleAtLeast($user?.role, 'editor')}
            <h3>Create point-in-time snapshot</h3>
            <form
              onsubmit={(event) => {
                event.preventDefault();
                void act('Snapshot queued', () =>
                  requestDatasetSnapshot(key, snapshotDataset, snapshotVersion, {
                    observation_at: utc(observationAt, 'Observation time'),
                    knowledge_at: utc(knowledgeAt, 'Knowledge time'),
                    idempotency_key: `ui-snapshot-${snapshotDataset}-${observationAt}`
                  })
                );
              }}
            >
              <label
                >Dataset<select bind:value={snapshotDataset}
                  ><option value="">Choose…</option>{#each datasets as dataset}<option
                      value={dataset.name}>{dataset.name}</option
                    >{/each}</select
                ></label
              >
              <label>Version<input type="number" min="1" bind:value={snapshotVersion} /></label>
              <label
                >Observation time<input type="datetime-local" bind:value={observationAt} /></label
              >
              <label>Knowledge cutoff<input type="datetime-local" bind:value={knowledgeAt} /></label
              >
              <button disabled={!!busy || !snapshotDataset}>Queue snapshot</button>
            </form>
          {/if}
          <h3>Published snapshots</h3>
          <div class="cards">
            {#each snapshots as snapshot (snapshot.manifest.snapshot_id)}
              <article class="card">
                <div class="row">
                  <strong
                    >{snapshot.manifest.dataset_name} v{snapshot.manifest.dataset_version}</strong
                  ><Badge tone={snapshot.state === 'expired' ? 'danger' : 'ok'}
                    >{snapshot.state}</Badge
                  >
                </div>
                <p>
                  {snapshot.manifest.row_count} / {snapshot.manifest.candidate_count} candidates ·
                  {snapshot.manifest.population_excluded_count +
                    snapshot.manifest.consent_excluded_count} excluded ·
                  {snapshot.manifest.censored_count} censored
                </p>
                <p class="muted">
                  {Math.round(snapshot.manifest.feature_completeness * 100)}% feature completeness ·
                  {snapshot.manifest.quality_finding_count} governed quality findings
                </p>
                <small
                  >{snapshot.manifest.rows_hash.slice(0, 18)}… · expires <RelativeTime
                    value={snapshot.manifest.expires_at}
                  /></small
                >
                {#if snapshot.state === 'available' && roleAtLeast($user?.role, 'admin')}
                  <div class="row">
                    <button
                      class="text-btn"
                      disabled={!!busy}
                      onclick={() =>
                        void act('Verified JSON snapshot downloaded', () =>
                          saveSnapshot(
                            snapshot.manifest.snapshot_id,
                            snapshot.manifest.dataset_name,
                            'json'
                          )
                        )}>JSON</button
                    >
                    <button
                      class="text-btn"
                      disabled={!!busy}
                      onclick={() =>
                        void act('Verified CSV snapshot downloaded', () =>
                          saveSnapshot(
                            snapshot.manifest.snapshot_id,
                            snapshot.manifest.dataset_name,
                            'csv'
                          )
                        )}>CSV</button
                    >
                  </div>
                {/if}
              </article>
            {/each}
          </div>
        </div>
      </div>
      <details>
        <summary>Feature lineage & backfill</summary>
        {#if governedFeatures.length === 0}
          <EmptyState
            title="No governed features"
            hint="Define a Context feature to expose source, cost, materialization, and consumers."
          />
        {:else}
          <div class="table-wrap">
            <table>
              <thead
                ><tr
                  ><th>Feature</th><th>Source</th><th>Materialization</th><th>Rows / bytes</th><th
                    >Compute / cost</th
                  ><th>Consumers</th></tr
                ></thead
              >
              <tbody>
                {#each governedFeatures as feature (feature.definition.entity_type + feature.definition.name)}
                  <tr>
                    <td>
                      <strong>{feature.definition.name}</strong>
                      <small>v{feature.definition.version}</small>
                    </td>
                    <td>
                      {feature.source_ref.event_name} · schema v{feature.source_schema_version ??
                        '—'}
                      <small>{feature.freshness_seconds ?? 0}s freshness SLO</small>
                    </td>
                    <td>
                      <Badge
                        tone={feature.materialization_status === 'completed'
                          ? 'ok'
                          : feature.materialization_status === 'failed'
                            ? 'danger'
                            : 'neutral'}>{feature.materialization_status}</Badge
                      >
                      {#if feature.last_error}<small>{feature.last_error}</small>{/if}
                    </td>
                    <td>{feature.cardinality} / {feature.storage_bytes}</td>
                    <td>
                      {feature.compute_units} / ${feature.estimated_cost_usd.toFixed(6)}
                    </td>
                    <td>{feature.downstream_consumers.join(', ') || 'none'}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        {#if roleAtLeast($user?.role, 'editor')}
          <form
            class="inline-form"
            onsubmit={(event) => {
              event.preventDefault();
              void act('Backfill queued', () =>
                requestFeatureBackfill(key, {
                  entity_type: backfillEntity,
                  features: backfillFeatures
                    .split(',')
                    .map((item) => item.trim())
                    .filter(Boolean),
                  as_of: utc(backfillAsOf, 'As-of'),
                  knowledge_at: utc(backfillKnowledge, 'Knowledge cutoff'),
                  idempotency_key: `ui-backfill-${backfillEntity}-${backfillAsOf}`
                })
              );
            }}
          >
            <label>Entity type<input bind:value={backfillEntity} /></label><label
              >Features (comma separated)<input bind:value={backfillFeatures} /></label
            ><label>As-of<input type="datetime-local" bind:value={backfillAsOf} /></label><label
              >Knowledge cutoff<input type="datetime-local" bind:value={backfillKnowledge} /></label
            ><button disabled={!!busy}>Queue backfill</button>
          </form>
        {/if}
        <p class="muted">{materializations.length} verified materializations published.</p>
      </details>
    </section>

    <section class="panel" id="training">
      <div class="section-head">
        <div>
          <p class="eyebrow">Snapshot → fit → sign → validate</p>
          <h2>Training & independent evaluation</h2>
        </div>
        <Badge tone={runningJobs.length ? 'warn' : 'ok'}>{runningJobs.length} active jobs</Badge>
      </div>
      {#if roleAtLeast($user?.role, 'editor')}
        <div class="split">
          <form
            onsubmit={(event) => {
              event.preventDefault();
              void act('Training queued', () =>
                requestModelTraining(key, {
                  model_name: trainingModel,
                  snapshot_id: trainingSnapshot,
                  runtime: 'intraktible-logistic/v1',
                  code_revision: codeRevision,
                  parameters: { iterations: 200, learning_rate: 0.1, folds: 0 },
                  seed: 17,
                  idempotency_key: `ui-training-${trainingModel}-${trainingSnapshot}`
                })
              );
            }}
          >
            <h3>Reproducible training</h3>
            <label>Model name<input bind:value={trainingModel} /></label><label
              >Snapshot<select bind:value={trainingSnapshot}
                ><option value="">Choose…</option>{#each availableSnapshots as snapshot}<option
                    value={snapshot.manifest.snapshot_id}
                    >{snapshot.manifest.dataset_name} · {snapshot.manifest.snapshot_id.slice(
                      0,
                      8
                    )}</option
                  >{/each}</select
              ></label
            ><label
              >Code revision<input
                bind:value={codeRevision}
                placeholder="git:0123456789ab"
              /></label
            ><button disabled={!!busy || !trainingSnapshot}>Queue deterministic fit</button>
          </form>
          <form
            onsubmit={(event) => {
              event.preventDefault();
              void act('Independent evaluation queued', () =>
                requestModelEvaluation(key, {
                  artifact_id: evaluationArtifact,
                  snapshot_id: evaluationSnapshot,
                  purpose: 'independent-validation',
                  options: {},
                  idempotency_key: `ui-evaluation-${evaluationArtifact}-${evaluationSnapshot}`
                })
              );
            }}
          >
            <h3>Independent validation run</h3>
            <label
              >Signed artifact<select bind:value={evaluationArtifact}
                ><option value="">Choose…</option>{#each trainedArtifacts as artifact}<option
                    value={artifact.artifact_id}
                    >{artifact.model_name} · {artifact.artifact_hash.slice(0, 8)}</option
                  >{/each}</select
              ></label
            ><label
              >Evaluation snapshot<select bind:value={evaluationSnapshot}
                ><option value="">Choose…</option>{#each availableSnapshots as snapshot}<option
                    value={snapshot.manifest.snapshot_id}
                    >{snapshot.manifest.dataset_name} · {snapshot.manifest.snapshot_id.slice(
                      0,
                      8
                    )}</option
                  >{/each}</select
              ></label
            ><button disabled={!!busy || !evaluationArtifact || !evaluationSnapshot}
              >Queue independent evaluation</button
            >
          </form>
        </div>
      {:else}
        <p class="muted">
          Training and independent evaluation require the editor role. Signed artifacts and evidence
          remain available below for review.
        </p>
      {/if}

      <div class="table-wrap">
        <table>
          <thead
            ><tr
              ><th>Job</th><th>Kind</th><th>State</th><th>Progress</th><th>Attempt</th><th
                >Updated</th
              ><th></th></tr
            ></thead
          ><tbody>
            {#each jobs.slice(0, 12) as job (job.job_id)}
              <tr
                ><td><code>{job.job_id.slice(0, 12)}</code></td><td>{job.kind}</td><td
                  ><Badge
                    tone={job.state === 'completed'
                      ? 'ok'
                      : job.state === 'failed'
                        ? 'danger'
                        : 'warn'}>{job.state}</Badge
                  ></td
                ><td
                  ><strong>{Math.round(job.progress_percent ?? 0)}%</strong>
                  <small>{job.phase ?? 'queued'} · {job.compute_units ?? 0} compute units</small
                  ></td
                ><td>{job.attempt}</td><td><RelativeTime value={job.updated_at} /></td><td>
                  {#if roleAtLeast($user?.role, 'operator')}
                    {#if ['queued', 'running'].includes(job.state)}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Job pause requested', () =>
                            pauseModelingJob(
                              key,
                              job.job_id,
                              'Operator paused from modeling cockpit'
                            )
                          )}>Pause</button
                      >
                    {:else if job.state === 'paused'}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Job resumed', () =>
                            resumeModelingJob(
                              key,
                              job.job_id,
                              'Operator resumed from modeling cockpit'
                            )
                          )}>Resume</button
                      >
                    {:else if job.state === 'failed'}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Job retry queued', () =>
                            retryModelingJob(
                              key,
                              job.job_id,
                              'Operator reviewed failure and requested retry'
                            )
                          )}>Retry</button
                      >
                    {/if}
                    {#if ['queued', 'running', 'paused', 'failed'].includes(job.state)}
                      <button
                        class="text-btn"
                        disabled={!!busy}
                        onclick={() =>
                          void act('Job cancellation requested', () =>
                            cancelModelingJob(
                              key,
                              job.job_id,
                              'Operator cancelled from modeling cockpit'
                            )
                          )}>Cancel</button
                      >
                    {/if}
                  {/if}</td
                ></tr
              >
            {/each}
          </tbody>
        </table>
      </div>

      <div class="split lower">
        <div>
          <h3>Signed artifacts</h3>
          <div class="cards">
            {#each artifacts as artifact (artifact.artifact_id)}<article class="card">
                <div class="row">
                  <strong>{artifact.model_name}</strong><Badge
                    tone={artifact.stage === 'production'
                      ? 'ok'
                      : artifact.stage === 'archived'
                        ? 'danger'
                        : 'warn'}>{artifact.stage}</Badge
                  >
                </div>
                <p><code>{artifact.artifact_hash.slice(0, 22)}…</code></p>
                <small
                  >{artifact.origin === 'external' ? 'external' : 'platform trained'} ·
                  {artifact.format} · {artifact.runtime} · {artifact.size_bytes} bytes</small
                >
                <p>{artifact.explanation.limitations}</p>
                <button
                  class="text-btn"
                  disabled={!!busy}
                  onclick={() =>
                    void act('Artifact signature verified', () =>
                      verifyModelArtifact(key, artifact.artifact_id)
                    )}>Verify signature & provenance</button
                >
                {#if roleAtLeast($user?.role, 'approver') && artifact.stage === 'registered'}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() =>
                      void act('Artifact validated', () =>
                        changeModelArtifactStage(
                          key,
                          artifact.artifact_id,
                          'validated',
                          'Independent supply-chain evidence review passed'
                        )
                      )}>Validate</button
                  >
                {:else if roleAtLeast($user?.role, 'approver') && artifact.stage === 'validated'}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() =>
                      void act('Artifact promoted to production', () =>
                        changeModelArtifactStage(
                          key,
                          artifact.artifact_id,
                          'production',
                          'Production artifact gate passed'
                        )
                      )}>Promote</button
                  >
                {/if}
                {#if roleAtLeast($user?.role, 'approver') && artifact.stage !== 'archived'}
                  <button
                    class="text-btn danger-btn"
                    disabled={!!busy}
                    onclick={() =>
                      void act('Artifact archived', () =>
                        changeModelArtifactStage(
                          key,
                          artifact.artifact_id,
                          'archived',
                          'Artifact retired from active use'
                        )
                      )}>Archive</button
                  >
                {/if}
              </article>{/each}
          </div>
          {#if roleAtLeast($user?.role, 'editor')}
            <form
              onsubmit={(event) => {
                event.preventDefault();
                void act('External artifact registered', () =>
                  registerExternalArtifact(
                    key,
                    parseObject<ExternalArtifactRegistration>(
                      externalArtifactJSON,
                      'External artifact registration'
                    )
                  )
                );
              }}
            >
              <h3>Register signed external artifact</h3>
              <p class="muted">
                Supply-chain evidence is verified without fetching or deserializing model bytes.
              </p>
              <label
                >Registration JSON<textarea rows="14" bind:value={externalArtifactJSON}
                ></textarea></label
              ><button disabled={!!busy}>Verify & register</button>
            </form>
          {/if}
        </div>
        <div>
          <h3>Evaluation evidence</h3>
          <div class="cards">
            {#each evaluations as evaluation (evaluation.manifest.evaluation_id)}<article
                class="card"
              >
                <div class="row">
                  <strong>{evaluation.manifest.model_name}</strong><Badge
                    tone={evaluation.manifest.report.passed_leakage_checks ? 'ok' : 'danger'}
                    >AUC {evaluation.manifest.report.auc.toFixed(3)}</Badge
                  >
                </div>
                <p>
                  Accuracy {evaluation.manifest.report.accuracy.toFixed(3)} · Brier {evaluation.manifest.report.brier.toFixed(
                    3
                  )}
                </p>
                <small
                  >{evaluation.manifest.purpose} · <RelativeTime
                    value={evaluation.manifest.evaluated_at}
                  /></small
                >
              </article>{/each}
          </div>
        </div>
      </div>
    </section>

    <section class="panel" id="lineage">
      <div class="section-head">
        <div>
          <p class="eyebrow">Source → schema → feature → dataset → artifact → serving</p>
          <h2>Lineage & challenger evidence</h2>
        </div>
      </div>
      <div class="split">
        <form
          onsubmit={(event) => {
            event.preventDefault();
            busy = 'Loading lineage';
            void getModelLineage(key, lineageModel)
              .then(
                (result) => {
                  lineage = result;
                },
                (reason) => toast.error(message(reason))
              )
              .finally(() => {
                busy = '';
              });
          }}
        >
          <h3>Trace one production model</h3>
          <label>Model name<input bind:value={lineageModel} /></label><button
            disabled={!!busy || !lineageModel}>Load complete lineage</button
          >{#if lineage}<pre>{JSON.stringify(lineage, null, 2)}</pre>{/if}
        </form>
        <form
          onsubmit={(event) => {
            event.preventDefault();
            busy = 'Comparing models';
            void compareGovernedModels(key, champion, challenger)
              .then(
                (result) => {
                  comparison = result;
                },
                (reason) => toast.error(message(reason))
              )
              .finally(() => {
                busy = '';
              });
          }}
        >
          <h3>Champion / challenger</h3>
          <label>Champion<input bind:value={champion} /></label><label
            >Challenger<input bind:value={challenger} /></label
          ><button disabled={!!busy || !champion || !challenger}>Compare signed evidence</button
          >{#if comparison}<div class="comparison">
              <Badge tone={comparison.same_snapshot ? 'ok' : 'warn'}
                >{comparison.same_snapshot ? 'same snapshot' : 'different snapshots'}</Badge
              >
              <dl>
                <div>
                  <dt>Δ AUC</dt>
                  <dd>{comparison.auc_delta.toFixed(4)}</dd>
                </div>
                <div>
                  <dt>Δ accuracy</dt>
                  <dd>{comparison.accuracy_delta.toFixed(4)}</dd>
                </div>
                <div>
                  <dt>Δ Brier</dt>
                  <dd>{comparison.brier_delta.toFixed(4)}</dd>
                </div>
              </dl>
            </div>{/if}
        </form>
      </div>
    </section>
  {/if}
</main>

<style>
  .page-shell {
    max-width: 90rem;
    margin: 0 auto;
    padding: 2rem;
  }
  .page-head,
  .section-head,
  .row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .page-head {
    margin-bottom: 1.25rem;
  }
  .eyebrow {
    margin: 0 0 0.3rem;
    color: var(--accent-text);
    font: 600 0.72rem var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  h1 {
    margin: 0;
    font-size: clamp(2rem, 4vw, 3.4rem);
  }
  h2 {
    margin: 0.1rem 0;
    font-size: 1.45rem;
  }
  h3 {
    margin: 0.25rem 0 0.75rem;
    font-size: 1rem;
  }
  .lede,
  .muted,
  .card p {
    color: var(--fg-muted);
  }
  .lede {
    max-width: 55rem;
  }
  .kpis {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 0.75rem;
    margin-bottom: 1rem;
  }
  .kpis article,
  .card,
  .panel,
  form {
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: var(--radius);
    box-shadow: var(--shadow-sm);
  }
  .kpis article {
    padding: 1rem;
  }
  .kpis span {
    display: block;
    color: var(--fg-muted);
    font-size: 0.78rem;
  }
  .kpis strong {
    font-size: 1.8rem;
  }
  .kpis .danger {
    border-color: var(--danger);
  }
  .kpis .warn {
    border-color: var(--warning);
  }
  .panel {
    padding: 1.25rem;
    margin: 1rem 0;
  }
  .split {
    display: grid;
    grid-template-columns: 1.25fr 1fr;
    gap: 1rem;
    margin-top: 1rem;
  }
  .lower {
    padding-top: 1rem;
    border-top: 1px solid var(--border);
  }
  .cards {
    display: grid;
    gap: 0.6rem;
  }
  .card {
    padding: 0.85rem;
    box-shadow: none;
  }
  .card p {
    margin: 0.45rem 0;
  }
  .card small {
    color: var(--fg-subtle);
  }
  .danger-edge {
    border-left: 3px solid var(--danger);
  }
  form {
    padding: 1rem;
    display: grid;
    gap: 0.65rem;
    box-shadow: none;
  }
  label {
    display: grid;
    gap: 0.3rem;
    color: var(--fg-muted);
    font-size: 0.78rem;
  }
  input,
  select,
  textarea {
    width: 100%;
    box-sizing: border-box;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--fg);
    padding: 0.58rem;
    font: inherit;
  }
  textarea,
  code,
  pre {
    font-family: var(--font-mono);
  }
  button {
    border: 0;
    border-radius: var(--radius-sm);
    padding: 0.58rem 0.78rem;
    background: var(--accent);
    color: var(--accent-contrast);
    font-weight: 650;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .secondary,
  .text-btn {
    background: var(--surface-2);
    color: var(--fg);
    border: 1px solid var(--border);
  }
  .text-btn {
    padding: 0.35rem 0.55rem;
    margin-top: 0.5rem;
  }
  .danger-btn {
    background: var(--danger);
    color: white;
  }
  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .table-wrap {
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.84rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.65rem;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }
  th {
    color: var(--fg-subtle);
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .inline-form {
    grid-template-columns: repeat(5, minmax(0, 1fr));
    margin-top: 0.75rem;
  }
  details {
    margin-top: 1rem;
  }
  summary {
    cursor: pointer;
    font-weight: 650;
  }
  .error {
    padding: 0.75rem;
    border: 1px solid var(--danger);
    color: var(--danger);
    border-radius: var(--radius-sm);
  }
  pre {
    max-height: 24rem;
    overflow: auto;
    padding: 0.8rem;
    background: var(--surface-2);
    font-size: 0.72rem;
  }
  .comparison {
    margin-top: 0.8rem;
  }
  .comparison dl {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.5rem;
  }
  .comparison dl div {
    padding: 0.6rem;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  dt {
    color: var(--fg-muted);
    font-size: 0.72rem;
  }
  dd {
    margin: 0;
    font: 650 1.1rem var(--font-mono);
  }
  @media (max-width: 900px) {
    .kpis {
      grid-template-columns: repeat(2, 1fr);
    }
    .split {
      grid-template-columns: 1fr;
    }
    .inline-form {
      grid-template-columns: 1fr;
    }
    .page-shell {
      padding: 1rem;
    }
  }
</style>
