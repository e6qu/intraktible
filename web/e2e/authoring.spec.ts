// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect, type APIRequestContext, type Browser, type Page } from '@playwright/test';

const ADMIN_KEY = 'dev-sandbox-key';

const graph = {
  nodes: [
    { id: 'input', type: 'input' },
    { id: 'output', type: 'output' }
  ],
  edges: [{ from: 'input', to: 'output' }]
};

async function createKey(
  request: APIRequestContext,
  actor: string,
  role: 'editor' | 'approver'
): Promise<string> {
  const response = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': ADMIN_KEY },
    data: { name: actor, actor, role, scope: '*' }
  });
  expect(response.ok()).toBeTruthy();
  return (await response.json()).secret as string;
}

async function signedPage(browser: Browser, baseURL: string, apiKey: string): Promise<Page> {
  const context = await browser.newContext({ baseURL });
  const login = await context.request.post('/v1/login', { data: { api_key: apiKey } });
  expect(login.ok()).toBeTruthy();
  return context.newPage();
}

async function createVersionedFlow(
  request: APIRequestContext,
  apiKey: string,
  suffix: string
): Promise<string> {
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': apiKey },
    data: { slug: `collaboration-${suffix}`, name: `Collaboration ${suffix}` }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id: flowId } = await created.json();
  const published = await request.post(`/v1/flows/${flowId}/versions`, {
    headers: { 'X-Api-Key': apiKey },
    data: { graph }
  });
  expect(published.ok()).toBeTruthy();
  return flowId as string;
}

async function publishGovernedConsumer(
  request: APIRequestContext,
  makerKey: string,
  reviewerKey: string,
  suffix: string,
  componentId: string
): Promise<string> {
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': makerKey },
    data: { slug: `component-consumer-${suffix}`, name: `Component consumer ${suffix}` }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id: flowId } = await created.json();
  const source = {
    nodes: [
      { id: 'input', type: 'input' },
      {
        id: 'shared',
        type: 'subflow',
        config: { component_id: componentId, version: 1 }
      },
      { id: 'output', type: 'output' }
    ],
    edges: [
      { from: 'input', to: 'shared' },
      { from: 'shared', to: 'output' }
    ]
  };
  const draftResponse = await request.post('/v1/authoring/drafts', {
    headers: {
      'X-Api-Key': makerKey,
      'Idempotency-Key': `consumer-draft-${suffix}`
    },
    data: {
      flow_id: flowId,
      base_version: 0,
      title: `Adopt shared component ${suffix}`,
      graph: source
    }
  });
  expect(draftResponse.ok()).toBeTruthy();
  const { draft_id: draftId } = await draftResponse.json();
  const changeResponse = await request.post('/v1/authoring/changesets', {
    headers: {
      'X-Api-Key': makerKey,
      'Idempotency-Key': `consumer-changeset-${suffix}`
    },
    data: {
      draft_id: draftId,
      draft_revision: 1,
      title: `Adopt shared component ${suffix}`,
      required_checks: ['flow-validation']
    }
  });
  expect(changeResponse.ok()).toBeTruthy();
  const { changeset_id: changeSetId } = await changeResponse.json();
  const mutation = async (path: string, apiKey: string, data: object = {}) => {
    const response = await request.post(path, {
      headers: { 'X-Api-Key': apiKey },
      data
    });
    expect(response.ok(), `${path}: ${await response.text()}`).toBeTruthy();
  };
  await mutation(`/v1/authoring/changesets/${changeSetId}/checks`, makerKey, {
    name: 'flow-validation',
    status: 'passed'
  });
  await mutation(`/v1/authoring/changesets/${changeSetId}/submit`, makerKey);
  await mutation(`/v1/authoring/changesets/${changeSetId}/review`, reviewerKey, {
    decision: 'approve',
    reason: 'Reusable contract verified'
  });
  await expect
    .poll(async () => {
      const response = await request.get(`/v1/authoring/changesets/${changeSetId}`, {
        headers: { 'X-Api-Key': reviewerKey }
      });
      return response.ok() ? (await response.json()).state : 'unavailable';
    })
    .toBe('approved');
  await mutation(`/v1/authoring/changesets/${changeSetId}/publish`, reviewerKey);
  return flowId as string;
}

test('two browsers preserve competing edits and recover the accepted revision', async ({
  browser,
  request
}, testInfo) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const firstActor = `builder-a-${suffix}`;
  const secondActor = `builder-b-${suffix}`;
  const firstKey = await createKey(request, firstActor, 'editor');
  const secondKey = await createKey(request, secondActor, 'editor');
  const flowId = await createVersionedFlow(request, firstKey, suffix);
  const baseURL = testInfo.project.use.baseURL as string;
  const first = await signedPage(browser, baseURL, firstKey);
  const second = await signedPage(browser, baseURL, secondKey);

  await first.goto(`/engine/${flowId}`);
  await expect(first.getByTestId('draft-save-state')).toHaveText('Saved · r1');
  // Open the second browser only after the first has durably created the shared
  // draft, avoiding a test-only race over which draft the team selected.
  await second.goto(`/engine/${flowId}`);
  await expect(second.getByTestId('draft-save-state')).toHaveText('Saved · r1');
  await expect(second.getByTestId('draft-presence')).toContainText(firstActor);
  await expect(second.getByTestId('draft-presence')).toContainText(secondActor);

  const firstTitle = `First browser ${suffix}`;
  const secondTitle = `Second browser ${suffix}`;
  await Promise.all([
    first.getByLabel('Review title').fill(firstTitle),
    second.getByLabel('Review title').fill(secondTitle)
  ]);
  await expect
    .poll(
      async () =>
        (await first.getByTestId('draft-conflict').count()) +
        (await second.getByTestId('draft-conflict').count()),
      { timeout: 10_000 }
    )
    .toBe(1);

  const firstLost = (await first.getByTestId('draft-conflict').count()) === 1;
  const loser = firstLost ? first : second;
  const winner = firstLost ? second : first;
  const acceptedLocalTitle = firstLost ? firstTitle : secondTitle;
  await expect(winner.getByTestId('draft-save-state')).toHaveText('Saved · r2');
  const comparison = loser.getByTestId('draft-conflict-comparison');
  await expect(comparison).toContainText('Common base · r1');
  await expect(comparison).toContainText(firstTitle);
  await expect(comparison).toContainText(secondTitle);

  await loser.getByRole('button', { name: 'Keep my canvas as next revision' }).click();
  await expect(loser.getByTestId('draft-save-state')).toHaveText('Saved · r3');

  await Promise.all([first.reload(), second.reload()]);
  await expect(first.getByLabel('Review title')).toHaveValue(acceptedLocalTitle);
  await expect(second.getByLabel('Review title')).toHaveValue(acceptedLocalTitle);
  await expect(first.getByTestId('draft-save-state')).toHaveText('Saved · r3');
  await expect(second.getByTestId('draft-save-state')).toHaveText('Saved · r3');

  await first.context().close();
  await second.context().close();
});

test('maker discussion, independent review, notification, and publication stay on one changeset', async ({
  browser,
  request
}, testInfo) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const makerActor = `maker-${suffix}`;
  const reviewerActor = `reviewer-${suffix}`;
  const makerKey = await createKey(request, makerActor, 'editor');
  const reviewerKey = await createKey(request, reviewerActor, 'approver');
  const flowId = await createVersionedFlow(request, makerKey, `review-${suffix}`);
  const baseURL = testInfo.project.use.baseURL as string;
  const maker = await signedPage(browser, baseURL, makerKey);
  const reviewer = await signedPage(browser, baseURL, reviewerKey);

  await maker.goto(`/engine/${flowId}`);
  await expect(maker.getByTestId('draft-save-state')).toHaveText('Saved · r1');
  await maker.getByLabel('Review title').focus();
  await maker.keyboard.press('ControlOrMeta+a');
  await maker.keyboard.type(`Exact reviewed change ${suffix}`);
  await maker.getByLabel('Assigned reviewers').fill(reviewerActor);
  await maker
    .getByLabel('Required evidence')
    .fill(`assertion-suite = assertions-${suffix}: 24/24 passed`);
  await expect(
    maker.getByText('Do not paste customer data, credentials, or raw sensitive fixtures')
  ).toBeVisible();
  await expect(maker.getByTestId('draft-save-state')).toHaveText('Saved · r2', {
    timeout: 15_000
  });
  await maker.getByRole('button', { name: 'Request review' }).focus();
  await maker.keyboard.press('Enter');
  await expect(maker.getByTestId('changeset-in_review')).toBeVisible();
  await expect(maker.getByTestId('changeset-in_review')).toContainText('flow-validation: passed');
  await expect(maker.getByTestId('changeset-in_review')).toContainText(
    `assertions-${suffix}: 24/24 passed`
  );

  await reviewer.goto(`/engine/${flowId}#changeset-review`);
  const reviewCard = reviewer.getByTestId('changeset-in_review');
  await expect(reviewCard).toContainText(`Exact reviewed change ${suffix}`);
  const impact = reviewCard.getByTestId('changeset-environment-impact');
  await expect(impact).toContainText('sandbox — will resolve v2 when this review is published');
  await expect(impact).toContainText('staging — remains undeployed');
  await expect(impact).toContainText('production — remains undeployed');
  await expect(reviewCard).toContainText(`Assigned reviewer: ${reviewerActor}`);
  const thread = reviewCard.getByTestId('comment-thread');
  const discussion = `@${makerActor} pinned revision and validation evidence verified`;
  await thread.getByLabel('new comment').focus();
  await reviewer.keyboard.type(discussion);
  await thread.getByTestId('post-comment').focus();
  await reviewer.keyboard.press('Enter');
  await expect(thread).toContainText(discussion);
  await thread.getByRole('button', { name: `Resolve comment by ${reviewerActor}` }).focus();
  await reviewer.keyboard.press('Enter');
  await expect(thread).toContainText(`Resolved by ${reviewerActor}`);
  await reviewCard.getByRole('button', { name: 'Approve' }).focus();
  await reviewer.keyboard.press('Enter');
  const approved = reviewer.getByTestId('changeset-approved');
  await expect(approved).toBeVisible();
  await approved.getByRole('button', { name: 'Publish reviewed version' }).focus();
  await reviewer.keyboard.press('Enter');
  await expect(reviewer.getByTestId('changeset-published')).toBeVisible();

  const bell = maker.getByTestId('notifications-bell');
  await bell.locator('summary').click();
  const notification = bell
    .locator('button.item')
    .filter({ hasText: `Approved and ready to publish: Exact reviewed change ${suffix}` });
  await expect(notification).toBeVisible();
  await notification.click();
  await expect(maker).toHaveURL(
    (url) => url.pathname === `/engine/${flowId}` && url.hash === '#changeset-review'
  );
  await expect(maker.getByTestId('changeset-published')).toContainText('published');

  const flow = await request.get(`/v1/flows/${flowId}`, {
    headers: { 'X-Api-Key': reviewerKey }
  });
  expect(flow.ok()).toBeTruthy();
  expect((await flow.json()).latest).toBe(2);

  await maker.context().close();
  await reviewer.context().close();
});

test('revision history restores checkpoints and explicit archive starts fresh', async ({
  browser,
  request
}, testInfo) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const actor = `history-editor-${suffix}`;
  const editorKey = await createKey(request, actor, 'editor');
  const flowSuffix = `history-${suffix}`;
  const flowId = await createVersionedFlow(request, editorKey, flowSuffix);
  const editor = await signedPage(browser, testInfo.project.use.baseURL as string, editorKey);

  await editor.goto(`/engine/${flowId}`);
  await expect(editor.getByTestId('draft-save-state')).toHaveText('Saved · r1');
  const initialTitle = `Collaboration ${flowSuffix} working draft`;
  const changedTitle = `Checkpoint to undo ${suffix}`;
  await editor.getByLabel('Review title').fill(changedTitle);
  await expect(editor.getByTestId('draft-save-state')).toHaveText('Saved · r2');

  const history = editor.getByTestId('draft-revision-history');
  await history.locator('summary').click();
  await expect(history).toContainText('r1');
  await expect(history).toContainText('r2');
  await history.getByRole('button', { name: 'Restore draft revision 1 as a new revision' }).click();
  await expect(editor.getByTestId('draft-save-state')).toHaveText('Saved · r3');
  await expect(editor.getByLabel('Review title')).toHaveValue(initialTitle);

  await editor.getByRole('button', { name: 'Archive draft' }).click();
  const confirm = editor.getByRole('group', { name: 'Confirm archive draft' });
  await expect(confirm).toBeVisible();
  await confirm.getByRole('button', { name: 'Confirm archive' }).click();
  await expect(editor.getByTestId('draft-save-state')).toHaveText('Saved · r1');

  await expect
    .poll(async () => {
      const response = await request.get(`/v1/authoring/drafts?flow_id=${flowId}`, {
        headers: { 'X-Api-Key': editorKey }
      });
      if (!response.ok()) return [];
      return (
        (await response.json()) as {
          drafts?: Array<{ state: string }>;
        }
      ).drafts?.map((draft) => draft.state);
    })
    .toEqual(expect.arrayContaining(['active', 'archived']));

  await editor.context().close();
});

test('component owner sees impact and creates a governed compatible upgrade draft', async ({
  browser,
  request
}, testInfo) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const ownerKey = await createKey(request, `component-owner-${suffix}`, 'editor');
  const reviewerKey = await createKey(request, `component-reviewer-${suffix}`, 'approver');
  const componentName = `Affordability gate ${suffix}`;
  const componentResponse = await request.post('/v1/authoring/components', {
    headers: {
      'X-Api-Key': ownerKey,
      'Idempotency-Key': `component-${suffix}`
    },
    data: {
      slug: `affordability-${suffix}`,
      name: componentName,
      description: 'Shared exact-version affordability logic'
    }
  });
  expect(componentResponse.ok()).toBeTruthy();
  const { component_id: componentId } = await componentResponse.json();
  const componentGraph = {
    nodes: [
      { id: 'input', type: 'input' },
      { id: 'rule', type: 'rule', config: { rules: [] } },
      { id: 'output', type: 'output' }
    ],
    edges: [
      { from: 'input', to: 'rule' },
      { from: 'rule', to: 'output' }
    ]
  };
  const publishComponent = async (version: number, outputProperties: object) => {
    const response = await request.post(`/v1/authoring/components/${componentId}/versions`, {
      headers: {
        'X-Api-Key': ownerKey,
        'Idempotency-Key': `component-${suffix}-v${version}`
      },
      data: {
        graph: componentGraph,
        input_schema: {
          type: 'object',
          properties: { income: { type: 'number' } },
          required: ['income']
        },
        output_schema: {
          type: 'object',
          properties: outputProperties,
          required: ['eligible']
        }
      }
    });
    expect(response.ok()).toBeTruthy();
  };
  await publishComponent(1, { eligible: { type: 'boolean' } });
  await publishComponent(2, {
    eligible: { type: 'boolean' },
    affordability_band: { type: 'string' }
  });
  const flowId = await publishGovernedConsumer(request, ownerKey, reviewerKey, suffix, componentId);

  const owner = await signedPage(browser, testInfo.project.use.baseURL as string, ownerKey);
  await owner.goto(`/engine/${flowId}`);
  await owner.locator('.svelte-flow__node', { hasText: 'shared' }).click();
  const pinnedContents = owner.getByTestId('subflow-runtime-contents');
  await pinnedContents.locator('summary').click();
  await expect(pinnedContents).toContainText('Pinned runtime contents · 3 nodes');
  await expect(pinnedContents).toContainText('rule');

  await owner.goto('/components');
  await owner.getByRole('button').filter({ hasText: componentName }).click();
  await expect(owner.getByText('compatible', { exact: true })).toBeVisible();
  await owner.getByRole('button', { name: 'Retire' }).click();
  const retireConfirmation = owner.getByRole('group', { name: `Retire ${componentName}` });
  await expect(retireConfirmation).toContainText('Existing exact pins keep working');
  await retireConfirmation.getByRole('button', { name: 'Keep active' }).click();
  await expect(retireConfirmation).not.toBeVisible();
  const consumer = owner.locator('label.consumer-select').filter({ hasText: flowId });
  await expect(consumer).toBeVisible();
  await consumer.getByRole('checkbox').check();
  await owner.getByRole('button', { name: 'Create selected upgrade drafts to v2' }).click();
  await expect(owner.getByText('Created 1 governed upgrade draft')).toBeVisible();

  await expect
    .poll(async () => {
      const response = await request.get(`/v1/authoring/drafts?flow_id=${flowId}`, {
        headers: { 'X-Api-Key': ownerKey }
      });
      if (!response.ok()) return 0;
      const body = await response.json();
      const upgrade = body.drafts?.find(
        (draft: { graph?: { nodes?: Array<{ id: string; config?: { version?: number } }> } }) =>
          draft.graph?.nodes?.find((node) => node.id === 'shared')?.config?.version === 2
      );
      return upgrade ? 1 : 0;
    })
    .toBe(1);

  await owner.context().close();
});
