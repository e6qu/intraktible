// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('a platform admin creates an organization, then an org admin manages workspaces', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const orgKey = `e2e-org-${suffix}`;

  // Create the organization through the real platform API; the response carries
  // the one-time org-admin secret.
  const created = await request.post('/v1/platform/orgs', {
    headers: { 'X-Api-Key': KEY },
    data: {
      key: orgKey,
      display: `E2E Org ${suffix}`,
      config: { plan: 'enterprise', max_workspaces: 5 },
      admin_actor: `admin-${suffix}`
    }
  });
  expect(created.ok()).toBeTruthy();
  const createdBody = (await created.json()) as {
    org_key: string;
    admin_key_secret: string;
  };
  expect(createdBody.org_key).toBe(orgKey);
  expect(createdBody.admin_key_secret).toBeTruthy();

  // The tenancy page shows the new organization.
  await page.goto('/tenancy');
  await expect(page.getByRole('heading', { name: 'Tenant administration' })).toBeVisible();
  const orgsPanel = page.getByRole('region', { name: 'Organizations' });
  await expect(orgsPanel.getByText(`E2E Org ${suffix}`)).toBeVisible({ timeout: 15_000 });

  // The org's own admin key can create a workspace through the real API.
  const workspaceRes = await request.post(`/v1/orgs/${orgKey}/workspaces`, {
    headers: { 'X-Api-Key': createdBody.admin_key_secret },
    data: { key: 'west', display: 'West', config: { retention_days: 90 } }
  });
  expect(workspaceRes.ok()).toBeTruthy();

  // Membership: grant an editor, then the last-admin guard refuses to remove the
  // org's only admin.
  const grantRes = await request.post(`/v1/orgs/${orgKey}/workspaces/main/memberships`, {
    headers: { 'X-Api-Key': createdBody.admin_key_secret },
    data: { actor: `editor-${suffix}`, role: 'editor' }
  });
  expect(grantRes.ok()).toBeTruthy();
  const lastAdmin = await request.post(
    `/v1/orgs/${orgKey}/workspaces/main/memberships/admin-${suffix}/revoke`,
    { headers: { 'X-Api-Key': createdBody.admin_key_secret }, data: { reason: 'offboard' } }
  );
  expect(lastAdmin.ok()).toBeFalsy();

  // The workspaces panel reflects the org-scoped administration when the org is selected.
  await orgsPanel.getByText(`E2E Org ${suffix}`).click();
  const wsPanel = page.getByRole('region', { name: 'Workspaces' });
  await expect(wsPanel.locator('li.card', { hasText: 'West' }).first()).toBeVisible();
});

test('a non-platform principal cannot create organizations', async ({ request }) => {
  const forbidden = await request.post('/v1/platform/orgs', {
    headers: { 'X-Api-Key': 'not-a-real-key' },
    data: { key: 'nope', display: 'Nope', admin_actor: 'x' }
  });
  expect(forbidden.ok()).toBeFalsy();
});
