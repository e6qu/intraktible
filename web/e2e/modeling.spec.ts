// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

// waitForJob polls the durable modeling job until it reaches a terminal state.
// Snapshot/training/evaluation jobs run over tiny fixtures, so 30s is ample.
async function waitForJob(
  request: import('@playwright/test').APIRequestContext,
  jobID: string,
  timeoutMs = 30_000
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const res = await request.get(`/v1/modeling/jobs/${jobID}`, { headers: { 'X-Api-Key': KEY } });
    expect(res.ok()).toBeTruthy();
    const job = (await res.json()) as { state: string; error?: string };
    if (job.state === 'completed') return;
    if (job.state === 'failed' || job.state === 'cancelled') {
      throw new Error(`modeling job ${jobID} ended ${job.state}: ${job.error ?? ''}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`modeling job ${jobID} did not complete within ${timeoutMs}ms`);
}

// secondActor mints an independent API key so maker-checker is exercised by two
// real identities, not the sandbox admin approving its own work.
async function secondActor(
  request: import('@playwright/test').APIRequestContext,
  role: string,
  suffix: string
): Promise<{ headers: Record<string, string>; actor: string }> {
  const actor = `${role}-${suffix}`;
  const res = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': KEY },
    data: { name: `modeling-${actor}`, actor, role, scope: '*' }
  });
  expect(res.ok()).toBeTruthy();
  const { secret } = (await res.json()) as { secret: string };
  return { headers: { 'X-Api-Key': secret }, actor };
}

const ENTITY_TYPE = 'modeling_applicant';

test('a data owner versions a schema through independent approval and governs ingestion', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const entityType = `e2e_entity_${suffix}`;
  const spec = {
    ref: { kind: 'entity', entity_type: entityType },
    description: 'E2E governed entity contract.',
    owner_team: 'risk-data-science',
    purposes: ['model_development'],
    compatibility: 'backward',
    additional_properties: false,
    fields: [
      {
        name: 'cohort_key',
        type: 'string',
        required: true,
        identifier: true,
        classification: 'confidential',
        pattern: '^E2E-[0-9]{3}$',
        min_length: 7,
        max_length: 7
      },
      { name: 'amount', type: 'number', classification: 'confidential' }
    ],
    quality: { action: 'block', completeness_min: 1, unique_fields: ['cohort_key'] }
  };

  // Define v1 as the data owner (editor).
  const owner = await secondActor(request, 'editor', suffix);
  const defined = await request.post('/v1/modeling/schemas', {
    headers: owner.headers,
    data: spec
  });
  expect(defined.ok()).toBeTruthy();

  // v1 is not active until an independent checker approves it.
  const requested = await request.post(
    `/v1/modeling/schemas/entity/${entityType}/versions/1/approval-request`,
    { headers: owner.headers, data: {} }
  );
  expect(requested.ok()).toBeTruthy();
  const { request_id } = (await requested.json()) as { request_id: string };

  // The owner cannot approve their own request (four-eyes).
  const selfApprove = await request.post(`/v1/modeling/schema-approval/${request_id}/decision`, {
    headers: owner.headers,
    data: { ref: spec.ref, approve: true, reason: 'self approval must be refused' }
  });
  expect(selfApprove.ok()).toBeFalsy();

  const checker = await secondActor(request, 'approver', suffix);
  const approved = await request.post(`/v1/modeling/schema-approval/${request_id}/decision`, {
    headers: checker.headers,
    data: { ref: spec.ref, approve: true, reason: 'Contract and quality policy reviewed.' }
  });
  expect(approved.ok()).toBeTruthy();

  // The cockpit reflects the active contract and rejects structurally invalid data.
  await page.goto('/modeling');
  await expect(page.getByRole('heading', { name: 'Modeling cockpit' })).toBeVisible();
  const schemas = page.getByRole('region', { name: 'Sources & quality' });
  const card = schemas.locator('article.card', { hasText: entityType }).first();
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card.getByText('active v1')).toBeVisible();

  // A record violating the block policy (missing required identifier) is refused.
  const invalid = await request.post('/v1/context/entities', {
    headers: owner.headers,
    data: { entity_type: entityType, entity_id: 'E2E-001', schema_version: 1, attributes: {} }
  });
  expect(invalid.ok()).toBeFalsy();

  // A valid record is admitted.
  const valid = await request.post('/v1/context/entities', {
    headers: owner.headers,
    data: {
      entity_type: entityType,
      entity_id: 'E2E-001',
      schema_version: 1,
      attributes: { cohort_key: 'E2E-001', amount: 5 }
    }
  });
  expect(valid.ok()).toBeTruthy();

  // A duplicate identifier violates uniqueness and is refused under the block policy.
  const duplicate = await request.post('/v1/context/entities', {
    headers: owner.headers,
    data: {
      entity_type: entityType,
      entity_id: 'E2E-002',
      schema_version: 1,
      attributes: { cohort_key: 'E2E-001', amount: 9 }
    }
  });
  expect(duplicate.ok()).toBeFalsy();
});

test('a refer-policy violation opens an operator quality incident with acknowledgement', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const eventName = `e2e_refer_${suffix}`;
  const owner = await secondActor(request, 'editor', suffix);
  // A refer policy: the record is admitted but opens an actionable incident.
  const spec = {
    ref: { kind: 'event', entity_type: ENTITY_TYPE, event_name: eventName },
    description: 'E2E refer-policy event contract.',
    owner_team: 'risk-data-science',
    purposes: ['model_development'],
    compatibility: 'backward',
    additional_properties: false,
    fields: [{ name: 'amount', type: 'number', classification: 'confidential' }],
    quality: { action: 'refer', completeness_min: 1 }
  };
  const defined = await request.post('/v1/modeling/schemas', {
    headers: owner.headers,
    data: spec
  });
  expect(defined.ok()).toBeTruthy();
  const requested = await request.post(
    `/v1/modeling/schemas/event/${ENTITY_TYPE}/versions/1/approval-request?event_name=${eventName}`,
    { headers: owner.headers, data: {} }
  );
  expect(requested.ok()).toBeTruthy();
  const { request_id } = (await requested.json()) as { request_id: string };
  const checker = await secondActor(request, 'approver', suffix);
  const approved = await request.post(`/v1/modeling/schema-approval/${request_id}/decision`, {
    headers: checker.headers,
    data: { ref: spec.ref, approve: true, reason: 'Refer policy reviewed.' }
  });
  expect(approved.ok()).toBeTruthy();

  // First create the entity so the event has a subject, then record an incomplete
  // event: admitted under refer policy, but it opens an incident.
  const entity = await request.post('/v1/context/entities', {
    headers: owner.headers,
    data: {
      entity_type: ENTITY_TYPE,
      entity_id: `E2E-${suffix}`,
      attributes: { cohort_key: `E2E-${suffix}`.padEnd(7, '0').slice(0, 7) }
    }
  });
  expect(entity.ok()).toBeTruthy();
  const event = await request.post('/v1/context/events', {
    headers: owner.headers,
    data: {
      entity_type: ENTITY_TYPE,
      entity_id: `E2E-${suffix}`,
      event_name: eventName,
      schema_version: 1,
      event_id: `refer-${suffix}`,
      data: {}
    }
  });
  expect(event.ok()).toBeTruthy();

  await page.goto('/modeling');
  const incidents = page.getByRole('region', { name: 'Sources & quality' });
  // The event schema and its incident share the event name; the incident card is
  // distinguished by its owner/lineage line, which the schema card does not carry.
  const incidentCard = incidents
    .locator('article.card', { hasText: `schema:event/${ENTITY_TYPE}/${eventName}` })
    .first();
  await expect(incidentCard).toBeVisible({ timeout: 15_000 });

  // Manual resolution is impossible before an operator acknowledges ownership: the
  // acknowledge control is the only action available while the incident is open.
  await expect(incidentCard.getByRole('button', { name: 'Acknowledge ownership' })).toBeVisible();
  await incidentCard.getByRole('button', { name: 'Acknowledge ownership' }).click();
  await expect(incidentCard.getByRole('button', { name: 'Resolve with evidence' })).toBeVisible();
  await incidentCard.getByRole('button', { name: 'Resolve with evidence' }).click();
  await expect(incidentCard).toHaveCount(0);
});

test('a modeler builds a point-in-time snapshot and a validator approves the trained model', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const owner = await secondActor(request, 'editor', suffix);
  const checker = await secondActor(request, 'approver', suffix);
  const entityType = `e2e_cohort_${suffix}`;
  const datasetName = `e2e_dataset_${suffix}`;
  const modelName = `e2e_model_${suffix}`;

  // Governed entity + risk_signal + outcome contracts.
  for (const spec of [
    {
      ref: { kind: 'entity', entity_type: entityType },
      description: 'cohort entity',
      owner_team: 'risk-data-science',
      purposes: ['model_development'],
      compatibility: 'backward',
      additional_properties: false,
      fields: [{ name: 'segment', type: 'string', required: true, classification: 'internal' }],
      quality: { action: 'block', completeness_min: 1 }
    },
    {
      ref: { kind: 'event', entity_type: entityType, event_name: 'risk_signal' },
      description: 'risk signal',
      owner_team: 'risk-data-science',
      purposes: ['model_development'],
      compatibility: 'backward',
      additional_properties: false,
      fields: [{ name: 'amount', type: 'number', classification: 'confidential' }],
      quality: { action: 'block', completeness_min: 1 }
    },
    {
      ref: { kind: 'event', entity_type: entityType, event_name: 'outcome' },
      description: 'realized outcome',
      owner_team: 'credit-operations',
      purposes: ['model_development', 'model_validation'],
      compatibility: 'backward',
      additional_properties: false,
      fields: [
        { name: 'defaulted', type: 'boolean', required: true, classification: 'confidential' }
      ],
      quality: { action: 'block', completeness_min: 1 }
    }
  ]) {
    const defined = await request.post('/v1/modeling/schemas', {
      headers: owner.headers,
      data: spec
    });
    expect(defined.ok()).toBeTruthy();
    const ref = spec.ref as { kind: string; entity_type: string; event_name?: string };
    let path = `/v1/modeling/schemas/${ref.kind}/${ref.entity_type}/versions/1/approval-request`;
    if (ref.event_name) path += `?event_name=${ref.event_name}`;
    const requested = await request.post(path, { headers: owner.headers, data: {} });
    expect(requested.ok()).toBeTruthy();
    const { request_id } = (await requested.json()) as { request_id: string };
    const approved = await request.post(`/v1/modeling/schema-approval/${request_id}/decision`, {
      headers: checker.headers,
      data: { ref, approve: true, reason: 'Contract reviewed.' }
    });
    expect(approved.ok()).toBeTruthy();
  }

  // Define the feature and dataset, then seed a small labelled cohort.
  await request.post('/v1/context/features', {
    headers: owner.headers,
    data: {
      name: 'signal_sum_30d',
      entity_type: entityType,
      event_name: 'risk_signal',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 720
    }
  });
  const dataset = await request.post('/v1/modeling/datasets', {
    headers: owner.headers,
    data: {
      name: datasetName,
      description: 'E2E point-in-time cohort.',
      owner_team: 'risk-data-science',
      entity_type: entityType,
      features: ['signal_sum_30d'],
      label: {
        event_name: 'outcome',
        field: 'defaulted',
        kind: 'binary',
        positive_value: 'true',
        horizon_hours: 48
      },
      segment_fields: ['segment'],
      purpose: 'model_development',
      consent_requirement: { mode: 'not_required' },
      retention_days: 365,
      partitions: { train_bps: 7000, validation_bps: 1500, test_bps: 1500 }
    }
  });
  expect(dataset.ok()).toBeTruthy();

  const signalAt = new Date(Date.now() - 48 * 3600_000).toISOString();
  const outcomeAt = new Date(Date.now() - 12 * 3600_000).toISOString();
  const observationAt = new Date(Date.now() - 24 * 3600_000).toISOString();
  for (let i = 0; i < 10; i++) {
    const id = `E2E-${String(i).padStart(3, '0')}`;
    await request.post('/v1/context/entities', {
      headers: owner.headers,
      data: {
        entity_type: entityType,
        entity_id: id,
        schema_version: 1,
        attributes: { segment: i % 2 === 0 ? 'thin' : 'established' }
      }
    });
    await request.post('/v1/context/events', {
      headers: owner.headers,
      data: {
        entity_type: entityType,
        entity_id: id,
        event_name: 'risk_signal',
        schema_version: 1,
        event_id: `sig-${suffix}-${i}`,
        data: { amount: 20 + i * 7 },
        occurred_at: signalAt
      }
    });
    await request.post('/v1/context/events', {
      headers: owner.headers,
      data: {
        entity_type: entityType,
        entity_id: id,
        event_name: 'outcome',
        schema_version: 1,
        event_id: `out-${suffix}-${i}`,
        data: { defaulted: i % 2 === 0 },
        occurred_at: outcomeAt
      }
    });
  }

  // Build the immutable snapshot and wait for the durable worker to publish it.
  // knowledge_at is captured after the cohort is written so the knowledge-time
  // population includes every entity just created.
  const snapshotRes = await request.post(
    `/v1/modeling/datasets/${datasetName}/versions/1/snapshots`,
    {
      headers: owner.headers,
      data: {
        observation_at: observationAt,
        knowledge_at: new Date().toISOString(),
        idempotency_key: `e2e-snap-${suffix}`
      }
    }
  );
  expect(snapshotRes.ok()).toBeTruthy();
  const snapshot = (await snapshotRes.json()) as { job_id: string; snapshot_id: string };
  await waitForJob(request, snapshot.job_id);

  // Train a deterministic logistic model over the snapshot.
  const trainingRes = await request.post('/v1/modeling/training-jobs', {
    headers: owner.headers,
    data: {
      model_name: modelName,
      snapshot_id: snapshot.snapshot_id,
      runtime: 'intraktible-logistic/v1',
      code_revision: `e2e-notebook@${suffix}`,
      parameters: { iterations: 200, learning_rate: 0.1, folds: 0 },
      seed: 17,
      idempotency_key: `e2e-train-${suffix}`
    }
  });
  expect(trainingRes.ok()).toBeTruthy();
  const training = (await trainingRes.json()) as { job_id: string; artifact_id: string };
  await waitForJob(request, training.job_id);

  // Independent evaluation over the same snapshot.
  const evaluationRes = await request.post('/v1/modeling/evaluation-jobs', {
    headers: owner.headers,
    data: {
      artifact_id: training.artifact_id,
      snapshot_id: snapshot.snapshot_id,
      purpose: 'independent_model_validation',
      options: {},
      idempotency_key: `e2e-eval-${suffix}`
    }
  });
  expect(evaluationRes.ok()).toBeTruthy();
  const evaluation = (await evaluationRes.json()) as { job_id: string; evaluation_id: string };
  await waitForJob(request, evaluation.job_id);

  const evaluationView = await request.get(`/v1/modeling/evaluations/${evaluation.evaluation_id}`, {
    headers: owner.headers
  });
  const evaluationBody = (await evaluationView.json()) as {
    manifest: { report_hash: string; report: { auc: number; brier: number; accuracy: number } };
  };

  // The validator clears the artifact supply-chain gate, records validation, and
  // promotes the artifact to production before approving the model.
  for (const stage of ['validated', 'production']) {
    const stageRes = await request.post(`/v1/modeling/artifacts/${training.artifact_id}/stage`, {
      headers: checker.headers,
      data: { stage, reason: `Independent ${stage} gate passed.` }
    });
    expect(stageRes.ok()).toBeTruthy();
  }
  const validationRes = await request.post(`/v1/models/${modelName}/validation`, {
    headers: checker.headers,
    data: {
      dataset: datasetName,
      metrics: {
        auc: evaluationBody.manifest.report.auc,
        brier: evaluationBody.manifest.report.brier,
        accuracy: evaluationBody.manifest.report.accuracy
      },
      notes: 'Independent validator reproduced the signed holdout report.',
      passed: true,
      artifact_id: training.artifact_id,
      snapshot_id: snapshot.snapshot_id,
      evaluation_hash: evaluationBody.manifest.report_hash,
      leakage_passed: true,
      calibration_reviewed: true,
      fairness_reviewed: true,
      threshold_reviewed: true
    }
  });
  expect(validationRes.ok()).toBeTruthy();

  const approvalRequested = await request.post(`/v1/models/${modelName}/approval-request`, {
    headers: owner.headers
  });
  expect(approvalRequested.ok()).toBeTruthy();
  const { request_id } = (await approvalRequested.json()) as { request_id: string };
  const approved = await request.post(`/v1/models/${modelName}/approve`, {
    headers: checker.headers,
    data: {
      request_id,
      reason: 'Signed artifact, independent evaluation, and attestations verified.'
    }
  });
  expect(approved.ok()).toBeTruthy();

  // The cockpit surfaces the signed artifact, evaluation evidence, and full lineage.
  await page.goto('/modeling');
  const trainingPanel = page.getByRole('region', { name: 'Training & independent evaluation' });
  const artifactCard = trainingPanel.locator('article.card', { hasText: modelName }).first();
  await expect(artifactCard).toBeVisible({ timeout: 15_000 });
  await expect(artifactCard.getByText('production')).toBeVisible();
  await expect(trainingPanel.getByText(/AUC/)).toBeVisible();

  const lineage = page.getByRole('region', { name: 'Lineage & challenger evidence' });
  await lineage.getByLabel('Model name').fill(modelName);
  await lineage.getByRole('button', { name: 'Load complete lineage' }).click();
  const lineagePre = lineage.locator('pre');
  await expect(lineagePre).toContainText(snapshot.snapshot_id);
  await expect(lineagePre).toContainText(training.artifact_id);
});
