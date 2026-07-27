// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('the notifications bell mounts and opens the inbox', async ({ page }) => {
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await expect(bell).toBeVisible();
  await bell.locator('summary').click();
  // The inbox panel opens. We don't assert it's empty: case events (human-review tasks)
  // and @-mentions both feed it, so depending on what else this run created it may hold
  // items or show the caught-up state — either is valid here. The per-source behaviour is
  // covered by the Go unit tests (notifications.TestTaskNotificationsFromCaseLifecycle).
  await expect(bell.getByText('Notifications')).toBeVisible();
});

test('Escape and outside clicks dismiss the dropdown', async ({ page }) => {
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  const panel = bell.locator('.panel');
  await bell.locator('summary').click();
  await expect(panel).toBeVisible();

  // Escape closes while focus is still on the summary (the click leaves it there).
  await page.keyboard.press('Escape');
  await expect(panel).toBeHidden();

  // Reopen, then a click outside the dropdown dismisses it.
  await bell.locator('summary').click();
  await expect(panel).toBeVisible();
  await page.locator('body').click({ position: { x: 5, y: 400 } });
  await expect(panel).toBeHidden();
});

test('a policy mention opens the exact policy discussion', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const headers = { 'X-Api-Key': KEY };
  const flowResponse = await request.post('/v1/flows', {
    headers,
    data: { slug: `mention-flow-${suffix}`, name: `Mention flow ${suffix}` }
  });
  expect(flowResponse.ok()).toBeTruthy();
  const policyResponse = await request.post('/v1/policies', {
    headers,
    data: { name: `Mention policy ${suffix}`, flow_slug: `mention-flow-${suffix}` }
  });
  expect(policyResponse.ok()).toBeTruthy();
  const { policy_id } = await policyResponse.json();

  // Use a distinct author so @dev produces a real personal notification for the
  // browser's dev identity rather than being discarded as a self-mention.
  const authorResponse = await request.post('/v1/api-keys', {
    headers,
    data: {
      name: `mention-author-${suffix}`,
      actor: `author-${suffix}`,
      role: 'editor',
      scope: '*'
    }
  });
  expect(authorResponse.ok()).toBeTruthy();
  const { secret } = await authorResponse.json();
  const body = `@dev please review policy ${suffix}`;
  const commentResponse = await request.post(`/v1/comments/policy/${policy_id}`, {
    headers: { 'X-Api-Key': secret },
    data: { body }
  });
  expect(commentResponse.ok()).toBeTruthy();

  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await bell.locator('summary').click();
  const mention = bell.locator('button.item').filter({ hasText: body });
  await expect(mention).toBeVisible();
  await mention.click();

  await expect(page).toHaveURL(
    (url) =>
      url.pathname === '/policies' &&
      url.searchParams.get('policy') === policy_id &&
      url.hash === '#policy-discussion'
  );
  await expect(page.getByTestId('band-editor')).toBeVisible();
  await expect(page.getByTestId('comment-thread')).toContainText(body);
});

test('a model mention opens the exact model discussion', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const headers = { 'X-Api-Key': KEY };
  const model = `mention-model-${suffix}`;
  const modelResponse = await request.post('/v1/models', {
    headers,
    data: {
      name: model,
      spec: { kind: 'logistic', intercept: -3, coefficients: { fico: 0.005 } }
    }
  });
  expect(modelResponse.ok()).toBeTruthy();
  const authorResponse = await request.post('/v1/api-keys', {
    headers,
    data: { name: `model-author-${suffix}`, actor: `author-${suffix}`, role: 'editor', scope: '*' }
  });
  expect(authorResponse.ok()).toBeTruthy();
  const { secret } = await authorResponse.json();
  const body = `@dev please review model ${suffix}`;
  const commentResponse = await request.post(`/v1/comments/model/${model}`, {
    headers: { 'X-Api-Key': secret },
    data: { body }
  });
  expect(commentResponse.ok()).toBeTruthy();

  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await bell.locator('summary').click();
  const mention = bell.locator('button.item').filter({ hasText: body });
  await expect(mention).toBeVisible();
  await mention.click();

  await expect(page).toHaveURL(
    (url) =>
      url.pathname === '/models' &&
      url.searchParams.get('discussion') === model &&
      url.hash === '#model-discussion'
  );
  await expect(page.getByTestId('model-discussion')).toContainText(body);
});
