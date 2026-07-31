// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from 'vitest';
import {
  acknowledgeQualityIncident,
  defineSourceSchema,
  downloadDatasetSnapshot,
  getMaterialization,
  getModelArtifact,
  getModelEvaluation,
  getSourceSchema,
  listMaterializations,
  listGovernedFeatures,
  listModelArtifacts,
  listModelEvaluations,
  listModelingJobs,
  modelApproved,
  pauseModelingJob,
  requestModelEvaluation,
  requestSourceSchemaApproval,
  resumeModelingJob,
  retryModelingJob,
  resolveQualityIncident,
  retireModel,
  type Model,
  type SchemaRef
} from './api';

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

describe('modeling API contract', () => {
  it('keeps an event schema query after every versioned resource suffix', async () => {
    const ref: SchemaRef = {
      kind: 'event',
      entity_type: 'applicant',
      event_name: 'risk/signal'
    };
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ request_id: 'review-1' })
    );

    await requestSourceSchemaApproval('key', ref, 3, fetcher);
    expect(fetcher).toHaveBeenCalledWith(
      '/v1/modeling/schemas/event/applicant/versions/3/approval-request?event_name=risk%2Fsignal',
      expect.objectContaining({ method: 'POST' })
    );

    const read = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ ref, versions: [] })
    );
    await getSourceSchema('key', ref, read);
    expect(read.mock.calls[0][0]).toBe(
      '/v1/modeling/schemas/event/applicant?event_name=risk%2Fsignal'
    );
  });

  it('sends governed schema and independent evaluation bodies unchanged', async () => {
    const schemaFetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ event_id: 'schema', seq: 1 }, 202)
    );
    await defineSourceSchema(
      'key',
      {
        ref: { kind: 'entity', entity_type: 'applicant' },
        description: 'Applicant contract',
        owner_team: 'data-platform',
        purposes: ['underwriting'],
        compatibility: 'backward',
        additional_properties: false,
        fields: [
          { name: 'income', type: 'number', required: true, classification: 'confidential' }
        ],
        quality: { action: 'block', completeness_min: 1 }
      },
      schemaFetcher
    );
    expect(JSON.parse(schemaFetcher.mock.calls[0][1]?.body as string)).toMatchObject({
      ref: { kind: 'entity', entity_type: 'applicant' },
      quality: { action: 'block' }
    });

    const evaluationFetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ event_id: 'evaluation', seq: 2, job_id: 'job-1', evaluation_id: 'eval-1' }, 202)
    );
    await requestModelEvaluation(
      'key',
      {
        artifact_id: 'artifact/1',
        snapshot_id: 'snapshot/1',
        purpose: 'independent-validation',
        options: { threshold: 0.42 },
        idempotency_key: 'evaluation-1'
      },
      evaluationFetcher
    );
    expect(evaluationFetcher.mock.calls[0][0]).toBe('/v1/modeling/evaluation-jobs');
    expect(JSON.parse(evaluationFetcher.mock.calls[0][1]?.body as string)).toEqual({
      artifact_id: 'artifact/1',
      snapshot_id: 'snapshot/1',
      purpose: 'independent-validation',
      options: { threshold: 0.42 },
      idempotency_key: 'evaluation-1'
    });
  });

  it('normalizes omitted lists and exposes every immutable detail read', async () => {
    const empty = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> => response({})
    );
    await expect(listModelingJobs('key', empty)).resolves.toEqual([]);
    await expect(listModelArtifacts('key', empty)).resolves.toEqual([]);
    await expect(listModelEvaluations('key', empty)).resolves.toEqual([]);
    await expect(listMaterializations('key', empty)).resolves.toEqual([]);
    await expect(listGovernedFeatures('key', empty)).resolves.toEqual([]);

    const detail = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> => response({})
    );
    await getModelArtifact('key', 'artifact/1', detail);
    await getModelEvaluation('key', 'evaluation/1', detail);
    await getMaterialization('key', 'backfill/1', detail);
    expect(detail.mock.calls.map((call) => call[0])).toEqual([
      '/v1/modeling/artifacts/artifact%2F1',
      '/v1/modeling/evaluations/evaluation%2F1',
      '/v1/modeling/materializations/backfill%2F1'
    ]);
  });

  it('treats retirement as a serving gate and sends its evidence', async () => {
    const current: Model = {
      name: 'risk',
      kind: 'logistic',
      spec: {},
      updated_at: '2026-07-31T00:00:00Z',
      version: 2,
      approved_version: 2
    };
    expect(modelApproved(current)).toBe(true);
    expect(modelApproved({ ...current, retired_version: 2 })).toBe(false);

    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ event_id: 'retired', seq: 3 }, 202)
    );
    await retireModel('key', 'risk/model', 'superseded by v3', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/models/risk%2Fmodel/retire');
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'superseded by v3'
    });
  });

  it('requires distinct acknowledgement and resolution evidence for quality incidents', async () => {
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ event_id: 'quality-command', seq: 4 }, 202)
    );
    await acknowledgeQualityIncident('key', 'incident/1', 'Data Ops owns remediation', fetcher);
    await resolveQualityIncident(
      'key',
      'incident/1',
      'Corrected source and replayed rows',
      fetcher
    );
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      '/v1/modeling/quality/incidents/incident%2F1/acknowledge',
      '/v1/modeling/quality/incidents/incident%2F1/resolve'
    ]);
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      note: 'Data Ops owns remediation'
    });
    expect(JSON.parse(fetcher.mock.calls[1][1]?.body as string)).toEqual({
      reason: 'Corrected source and replayed rows'
    });
  });

  it('exposes explicit pause, resume, and retry job controls', async () => {
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        response({ event_id: 'job-command', seq: 5 }, 202)
    );
    await pauseModelingJob('key', 'job/1', 'maintenance', fetcher);
    await resumeModelingJob('key', 'job/1', 'maintenance complete', fetcher);
    await retryModelingJob('key', 'job/1', 'failure reviewed', fetcher);
    expect(fetcher.mock.calls.map((call) => call[0])).toEqual([
      '/v1/modeling/jobs/job%2F1/pause',
      '/v1/modeling/jobs/job%2F1/resume',
      '/v1/modeling/jobs/job%2F1/retry'
    ]);
  });

  it('downloads reproducible snapshot bytes in the requested format', async () => {
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
        new Response('entity_id,feature:risk\\na-1,0.4\\n', {
          status: 200,
          headers: { 'Content-Type': 'text/csv' }
        })
    );
    const blob = await downloadDatasetSnapshot('key', 'snapshot/1', 'csv', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/modeling/snapshots/snapshot%2F1/export?format=csv');
    expect(await blob.text()).toContain('entity_id');
  });
});
