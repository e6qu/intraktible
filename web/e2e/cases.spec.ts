// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

function governedCaseType(key: string) {
  return {
    key,
    name: `Governed ${key}`,
    initial_state: 'intake',
    fields: [{ key: 'summary', label: 'Review summary', kind: 'string' }],
    transitions: [
      { from: 'intake', to: 'investigating', roles: ['operator', 'admin'] },
      { from: 'investigating', to: 'resolved', roles: ['operator', 'admin'] }
    ],
    dispositions: [
      {
        key: 'clear',
        label: 'Clear',
        reason_codes: ['verified'],
        terminal_state: 'resolved'
      },
      {
        key: 'reject',
        label: 'Reject',
        reason_codes: ['confirmed'],
        terminal_state: 'resolved'
      }
    ],
    priorities: ['normal', 'high'],
    service_calendar: {
      timezone: 'UTC',
      weekdays: [1, 2, 3, 4, 5],
      start_hour: 9,
      end_hour: 17,
      sla_hours: 16
    },
    evidence_requirements: [],
    layouts: [{ role: 'operator', sections: ['summary'], editable: ['summary'] }]
  };
}

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('admin publishes a case type, opens governed work, and sees analytics', async ({ page }) => {
  await page.goto('/cases');
  await expect(page.getByRole('heading', { name: 'Cases', exact: true })).toBeVisible();

  const typeKey = `ui_review_${Math.random().toString(36).slice(2, 8)}`;
  await page.getByText('Case operations configuration').click();
  await page.getByLabel('case type definition').fill(JSON.stringify(governedCaseType(typeKey)));
  await page.getByRole('button', { name: 'Publish next version' }).click();

  await page.getByLabel('company name').fill('Acme UI');
  await page.getByLabel('case type', { exact: true }).selectOption(typeKey);
  await page.getByLabel('sla days').fill('5');
  await page.getByRole('button', { name: 'Open case' }).click();

  // .first(): a reused dev server may carry "Acme UI" cases from prior runs.
  await expect(page.getByRole('link', { name: 'Acme UI' }).first()).toBeVisible();
  // The queue summary banner reflects the open case(s).
  const summary = page.getByLabel('queue summary');
  await expect(summary).toContainText('Total');
  await expect(summary).toContainText('Overdue');

  // The SLA sweep runs without error (a fresh 5-day case is not overdue).
  const sweep = page.getByRole('button', { name: 'Run SLA sweep' });
  const sweepResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === '/v1/cases/sla-sweep' &&
      response.request().method() === 'POST'
  );
  await sweep.click();
  expect((await sweepResponse).ok()).toBeTruthy();
  await expect(page.locator('p.err')).toHaveCount(0);
  await expect(page.getByTestId('case-analytics')).toContainText('Open');
});

test('routes, evidences, dispositions, and independently disputes a governed case', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const typeKey = `ops_${suffix}`;
  const queueKey = `ops_queue_${suffix}`;
  const qaActor = `qa-${suffix}`;
  const headers = { 'X-Api-Key': KEY };

  expect(
    (
      await request.post('/v1/case-types', {
        headers,
        data: governedCaseType(typeKey)
      })
    ).ok()
  ).toBeTruthy();
  expect(
    (
      await request.put(`/v1/case-queues/${queueKey}`, {
        headers,
        data: {
          key: queueKey,
          name: `Operations ${suffix}`,
          case_types: [typeKey],
          required_skills: ['ops'],
          capacity: 20
        }
      })
    ).ok()
  ).toBeTruthy();
  expect(
    (
      await request.put(`/v1/case-reviewers/dev`, {
        headers,
        data: {
          actor: 'dev',
          skills: ['ops'],
          jurisdictions: [],
          capacity: 20,
          active: true
        }
      })
    ).ok()
  ).toBeTruthy();
  const qaKeyResponse = await request.post('/v1/api-keys', {
    headers,
    data: { name: qaActor, actor: qaActor, role: 'operator', scope: '*' }
  });
  expect(qaKeyResponse.ok()).toBeTruthy();
  const { secret: qaKey } = (await qaKeyResponse.json()) as { secret: string };

  const created = await request.post('/v1/cases', {
    headers,
    data: {
      company_name: `Enterprise ${suffix}`,
      case_type: typeKey,
      priority: 'high',
      subject: `customer/${suffix}`,
      context: {}
    }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id: caseID } = (await created.json()) as { case_id: string };

  await page.goto(`/cases/${caseID}`);
  await page.getByRole('button', { name: 'Route now' }).click();
  await expect(page.getByText(queueKey, { exact: true })).toBeVisible();
  await page.getByLabel('set status').selectOption('investigating');
  await page.getByRole('button', { name: 'Set status' }).click();

  await page.getByLabel('evidence subject').fill(`decision-${suffix}`);
  await page.getByLabel('evidence label').fill('Recorded screening decision');
  await page.getByRole('button', { name: 'Link', exact: true }).click();
  await expect(
    page.getByTestId('case-evidence').getByText('Recorded screening decision')
  ).toBeVisible();

  await page.getByLabel('attachment name').fill('registry.pdf');
  await page
    .getByLabel('attachment hash')
    .fill('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
  await page.getByLabel('attachment storage').fill(`s3://evidence/${suffix}/registry.pdf`);
  await page.getByRole('button', { name: 'Register' }).click();
  await expect(page.getByTestId('case-evidence').getByText(/registry\.pdf/)).toBeVisible();
  await page.getByRole('button', { name: 'Reveal storage reference' }).click();
  await expect(
    page.getByRole('button', { name: /Copy registry\.pdf storage reference/ })
  ).toContainText(`s3://evidence/${suffix}/registry.pdf`);

  await page.getByLabel('disposition', { exact: true }).selectOption('clear');
  await page.getByLabel('reason code', { exact: true }).selectOption('verified');
  await page.getByRole('button', { name: 'Record outcome' }).click();
  await expect(page.getByTestId('case-status')).toHaveText('resolved');

  await page.getByLabel('QA sample id').fill(`sample-${suffix}`);
  await page.getByLabel('QA reviewer').fill(qaActor);
  await page.getByRole('button', { name: 'Select for QA' }).click();
  await expect(page.getByTestId('qa-state')).toContainText(qaActor);

  await page.context().request.post('/v1/login', { data: { api_key: qaKey } });
  await page.reload();
  await page.getByLabel('QA disposition').selectOption('reject');
  await page.getByLabel('QA reason code').fill('confirmed');
  await page.getByRole('button', { name: 'Complete QA' }).click();
  await expect(page.getByTestId('qa-state')).toContainText('disputed');

  await page.goto('/cases');
  await expect(page.getByTestId('case-analytics')).toContainText('QA disagreement');
});

test('bulk-assigns selected cases from the queue', async ({ page, request }) => {
  const tag = 'bulk-' + Math.random().toString(36).slice(2, 7);
  for (const n of [1, 2]) {
    await request.post('/v1/cases', {
      headers: { 'X-Api-Key': KEY },
      data: { company_name: `${tag}-${n}`, case_type: 'aml', sla_days: 5 }
    });
  }
  await page.goto('/cases');

  // Select the two cases this test created via their row checkboxes.
  for (const n of [1, 2]) {
    await page.getByRole('checkbox', { name: `select ${tag}-${n}` }).check();
  }
  const bar = page.getByTestId('bulk-bar');
  await expect(bar).toContainText('2 selected');
  await bar.getByLabel('bulk assignee').fill('reviewer@x');
  await bar.getByRole('button', { name: 'Assign' }).click();

  // Both rows now show the assignee; the bulk bar clears after the action.
  for (const n of [1, 2]) {
    const row = page.locator('tbody tr').filter({ hasText: `${tag}-${n}` });
    await expect(row).toContainText('reviewer@x');
  }
  await expect(page.getByTestId('bulk-bar')).toHaveCount(0);
});

test('saves a searchable queue view and surfaces duplicate candidates', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const subject = `customer/duplicate-${suffix}`;
  const caseIDs: string[] = [];
  for (const company of [`Duplicate ${suffix} A`, `Duplicate ${suffix} B`]) {
    const created = await request.post('/v1/cases', {
      headers: { 'X-Api-Key': KEY },
      data: { company_name: company, case_type: 'aml', subject, sla_days: 5 }
    });
    expect(created.ok()).toBeTruthy();
    caseIDs.push(((await created.json()) as { case_id: string }).case_id);
  }

  await page.goto('/cases');
  await page.getByLabel('search cases').fill(suffix);
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(page.getByRole('link', { name: `Duplicate ${suffix} A` })).toBeVisible();
  await page.getByLabel('saved view name').fill(`Duplicates ${suffix}`);
  await page.getByRole('button', { name: 'Save current filters' }).click();
  await expect(page.getByLabel('apply saved case view')).toContainText(`Duplicates ${suffix}`);

  await page.getByLabel('search cases').fill('');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await page.getByLabel('apply saved case view').selectOption({ label: `Duplicates ${suffix}` });
  await expect(page.getByRole('link', { name: `Duplicate ${suffix} B` })).toBeVisible();

  const duplicates = page.getByTestId('case-duplicates');
  await duplicates.locator('summary').click();
  for (const caseID of caseIDs) {
    await expect(duplicates.getByRole('link', { name: caseID })).toBeVisible();
  }
});

test('case detail shows computed days-left', async ({ page, request }) => {
  // A freshly opened 5-day case is on track with ~5 days left.
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Initech UI', case_type: 'aml', sla_days: 5 }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  const daysLeft = page.getByTestId('days-left');
  // The SLA state renders as a human label ("on track"), not the wire enum.
  await expect(daysLeft).toContainText('on track');
  await expect(daysLeft).not.toContainText('on_track');
  await expect(daysLeft).toContainText(/[45]/);
});

test('assigns, transitions, and notes a case', async ({ page, request }) => {
  // Seed a case through the API.
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Globex UI', case_type: 'kyb_kyc', sla_days: 3 }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);

  await page.getByLabel('assignee').fill('adam');
  // exact: the page now also has an "Assign to me" button.
  await page.getByRole('button', { name: 'Assign', exact: true }).click();
  await page.getByLabel('set status').selectOption('in_progress');
  await page.getByRole('button', { name: 'Set status' }).click();
  await page.getByLabel('note', { exact: true }).fill('reviewed the docs');
  await page.getByRole('button', { name: 'Add note' }).click();

  // The read model is eventually consistent; reload until it reflects all three
  // actions (audit: requested + assigned + status_changed + note_added = 4).
  await expect(async () => {
    await page.getByRole('button', { name: 'Reload' }).click();
    await expect(page.getByTestId('case-status')).toHaveText('in_progress');
    await expect(page.getByTestId('audit').locator('li')).toHaveCount(4);
  }).toPass({ timeout: 5000 });
});

test('resumes a suspended decision from its case and closes both', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const slug = `human-task-${suffix}`;
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: `Human task ${suffix}` }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id } = await created.json();
  const published = await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'review',
            type: 'manual_review',
            config: {
              company_name: "'Acme'",
              case_type: "'underwriting'",
              suspend: true,
              output_key: 'review'
            }
          },
          { id: 'out', type: 'output', config: { fields: ['review'] } }
        ],
        edges: [
          { from: 'in', to: 'review' },
          { from: 'review', to: 'out' }
        ]
      }
    }
  });
  expect(published.ok()).toBeTruthy();

  let decisionId = '';
  let caseId = '';
  await expect(async () => {
    const decided = await request.post(`/v1/flows/${slug}/sandbox/decide`, {
      headers: { 'X-Api-Key': KEY },
      data: { data: { applicant: `A-${suffix}` } }
    });
    const result = await decided.json();
    expect(result.status).toBe('suspended');
    decisionId = result.decision_id;
  }).toPass({ timeout: 5000 });
  await expect(async () => {
    const detail = await request.get(`/v1/decisions/${decisionId}`, {
      headers: { 'X-Api-Key': KEY }
    });
    const record = await detail.json();
    expect(record.status).toBe('suspended');
    expect(record.case_id).toBeTruthy();
    caseId = record.case_id;
  }).toPass({ timeout: 5000 });

  // The API refuses every parallel lifecycle mutation: this task owns a
  // still-suspended decision and the recorded human outcome is its sole state
  // transition authority.
  for (const status of ['in_progress', 'completed']) {
    const premature = await request.post(`/v1/cases/${caseId}/status`, {
      headers: { 'X-Api-Key': KEY },
      data: { status }
    });
    expect(premature.status()).toBe(400);
    expect(await premature.text()).toContain('record its review outcome');
  }

  await page.goto(`/cases/${caseId}`);
  await expect(page.getByTestId('case-status')).toHaveText('needs_review');
  await expect(page.getByLabel('set status')).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Set status' })).toBeDisabled();
  await expect(page.getByRole('heading', { name: 'Disposition' })).toHaveCount(0);
  const reviewDecision = page.getByTestId('case-resume-decision');
  await expect(reviewDecision).toBeVisible();
  await reviewDecision.click();
  await expect(page).toHaveURL((url) => url.pathname === `/decisions/${decisionId}`);
  const resume = page.getByTestId('resume-panel');
  await resume.getByRole('button', { name: 'Approve' }).click();
  await expect(resume).toBeHidden();
  await expect(page.getByRole('heading', { name: slug })).toContainText('completed');

  await page.goto(`/cases/${caseId}`);
  await expect(page.getByTestId('case-status')).toHaveText('completed');
  await expect(page.getByTestId('audit')).toContainText('decision_resumed');
  await expect(page.getByText('This case is resolved.')).toBeVisible();

  const inbox = await request.get('/v1/notifications', {
    headers: { 'X-Api-Key': KEY }
  });
  expect(inbox.ok()).toBeTruthy();
  const body = await inbox.json();
  expect(
    (body.notifications as { subject_type: string; subject_id: string }[]).some(
      (item) => item.subject_type === 'case' && item.subject_id === caseId
    )
  ).toBeFalsy();
});

test('case detail renders context in the active role layout', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const typeKey = `layout_${suffix}`;
  const definition = governedCaseType(typeKey);
  definition.fields = [
    { key: 'subject', label: 'Customer', kind: 'string' },
    { key: 'fico', label: 'Risk score', kind: 'number' }
  ];
  definition.layouts = [{ role: 'admin', sections: ['fico', 'subject'], editable: ['fico'] }];
  const published = await request.post('/v1/case-types', {
    headers: { 'X-Api-Key': KEY },
    data: definition
  });
  expect(published.ok()).toBeTruthy();

  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: {
      company_name: 'Context Co',
      case_type: typeKey,
      sla_days: 5,
      context: { subject: 'Acme Corp', fico: 700 }
    }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  const ctx = page.getByTestId('context');
  await expect(ctx).toContainText('Customer');
  await expect(ctx).toContainText('Acme Corp');
  await expect(ctx).toContainText('Risk score');
  await expect(ctx.locator('.fact-key')).toHaveText(['Risk score', 'Customer']);
  await page.getByLabel('edit Risk score').fill('720');
  await page.getByRole('button', { name: 'Save fields' }).click();
  await expect(ctx).toContainText('720');
  await page.reload();
  await expect(page.getByTestId('context')).toContainText('720');
});

// The persona lens presets the operator's queue to needs_review, but choosing
// "all" must actually widen the view — the lens default only applies to a
// pristine URL, never over an explicit selection.
test("operator persona: the 'all' status filter overrides the lens preset", async ({
  page,
  request
}) => {
  const tag = 'lens-' + Math.random().toString(36).slice(2, 7);
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: `${tag}-done`, case_type: 'aml', sla_days: 5 }
  });
  const { case_id } = (await created.json()) as { case_id: string };
  await request.post(`/v1/cases/${case_id}/status`, {
    headers: { 'X-Api-Key': KEY },
    data: { status: 'completed' }
  });

  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'operator'));
  await page.goto('/cases');
  const filter = page.getByLabel('status filter');
  await expect(filter).toHaveValue('needs_review');
  await expect(page.getByRole('link', { name: `${tag}-done` })).toHaveCount(0);

  await filter.selectOption('');
  await expect(filter).toHaveValue('');
  await expect(page.getByRole('link', { name: `${tag}-done` }).first()).toBeVisible();
});

test('posts a comment in the case discussion thread', async ({ page, request }) => {
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Discussed UI', case_type: 'aml', sla_days: 5 }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  // The discussion sits below the activity trail, distinct from Notes.
  await expect(page.getByRole('heading', { name: 'Discussion' })).toBeVisible();
  const thread = page.getByTestId('comment-thread');
  await thread.getByLabel('new comment').fill('Holding for the registry extract — thoughts?');
  await thread.getByTestId('post-comment').click();
  await expect(thread).toContainText('Holding for the registry extract — thoughts?');

  // The comment persists (it is event-sourced, not local state).
  await page.reload();
  await expect(page.getByTestId('comment-thread')).toContainText(
    'Holding for the registry extract — thoughts?'
  );
});
