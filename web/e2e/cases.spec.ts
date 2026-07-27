// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('opens a case from the queue and shows SLA + summary', async ({ page }) => {
  await page.goto('/cases');
  await expect(page.getByRole('heading', { name: 'Cases', exact: true })).toBeVisible();

  await page.getByLabel('company name').fill('Acme UI');
  await page.getByLabel('case type').fill('aml');
  await page.getByLabel('sla days').fill('5');
  await page.getByRole('button', { name: 'Open case' }).click();

  // .first(): a reused dev server may carry "Acme UI" cases from prior runs.
  await expect(page.getByRole('link', { name: 'Acme UI' }).first()).toBeVisible();
  // The queue summary banner reflects the open case(s).
  const summary = page.getByLabel('queue summary');
  await expect(summary).toContainText('Total');
  await expect(summary).toContainText('Overdue');

  // The SLA sweep runs without error (a fresh 5-day case is not overdue).
  await page.getByRole('button', { name: 'Run SLA sweep' }).click();
  await expect(page.locator('p.err')).toHaveCount(0);
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

  // The API also refuses the misleading terminal action: this task owns a
  // still-suspended decision and needs an actual review outcome.
  const premature = await request.post(`/v1/cases/${caseId}/status`, {
    headers: { 'X-Api-Key': KEY },
    data: { status: 'completed' }
  });
  expect(premature.status()).toBe(400);
  expect(await premature.text()).toContain('record its review outcome');

  await page.goto(`/cases/${caseId}`);
  await expect(page.getByTestId('case-status')).toHaveText('needs_review');
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

test('case detail renders the context as a key-value view', async ({ page, request }) => {
  // Seed a case with context (as a decision/agent escalation would carry).
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: {
      company_name: 'Context Co',
      case_type: 'aml',
      sla_days: 5,
      context: { subject: 'Acme Corp', fico: 700 }
    }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  const ctx = page.getByTestId('context');
  await expect(ctx).toContainText('subject');
  await expect(ctx).toContainText('Acme Corp');
  await expect(ctx).toContainText('fico');
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
