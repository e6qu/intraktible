// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

async function configureShauthOnly(page: import('@playwright/test').Page): Promise<void> {
  await page.route('**/v1/auth/oidc/providers', (route) =>
    route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ providers: ['shauth'] })
    })
  );
  await page.route('**/v1/auth/saml/providers', (route) =>
    route.fulfill({ contentType: 'application/json', body: JSON.stringify({ providers: [] }) })
  );
}

test('direct entry delegates an unauthenticated Shauth deployment to Shauth', async ({ page }) => {
  await configureShauthOnly(page);
  const signIn = page.waitForRequest(
    (request) => new URL(request.url()).pathname === '/v1/auth/oidc/shauth/login'
  );

  await page.goto('/');
  await signIn;
});

test('a direct protected deep link delegates to Shauth without rendering an API-key prompt', async ({
  page
}) => {
  await configureShauthOnly(page);
  const signIn = page.waitForRequest(
    (request) => new URL(request.url()).pathname === '/v1/auth/oidc/shauth/login'
  );

  await page.goto('/engine');
  await expect(page.getByLabel('API key')).toHaveCount(0);
  const request = await signIn;
  expect(new URL(request.url()).searchParams.get('return_to')).toBe('/engine');
});

test('the sign-in route delegates a Shauth deployment to Shauth', async ({ page }) => {
  await configureShauthOnly(page);
  const signIn = page.waitForRequest(
    (request) => new URL(request.url()).pathname === '/v1/auth/oidc/shauth/login'
  );

  await page.goto('/login');
  await expect(page.getByLabel('API key')).toHaveCount(0);
  await signIn;
});

test('sign in with an API key, then sign out', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: /Sign in/i })).toBeVisible();
  // Minimal chrome on the sign-in screen: no primary nav or account control.
  await expect(page.getByRole('navigation', { name: 'Primary' })).toHaveCount(0);
  await expect(page.getByTestId('persona-switch')).toHaveCount(0);

  await page.getByLabel('API key').fill('dev-sandbox-key');
  await page.getByRole('button', { name: 'Sign in' }).click();

  // Redirected home; the full chrome returns and identity + sign-out live in the
  // account & view menu.
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible();
  await expect(page.getByTestId('user-identity')).toHaveText('dev');
  await expect(page.getByTestId('user-avatar')).toHaveText('D');
  await page.getByTestId('persona-switch').locator('summary').click();
  const status = page.getByTestId('auth-status');
  await expect(status).toContainText('Signed in as');
  await expect(status).toContainText('dev');

  await page.getByRole('link', { name: 'My account' }).click();
  await expect(page.getByRole('heading', { name: 'Signed in as dev' })).toBeVisible();
  await expect(page.getByText('Organization')).toBeVisible();
  await expect(page.getByText('Workspace')).toBeVisible();

  // Signing out returns to the sign-in screen (and drops the full chrome), so the
  // signed-out state is unambiguous rather than a stripped-down dashboard.
  await page
    .getByLabel('Current account details')
    .getByRole('button', { name: 'Sign out' })
    .click();
  await expect(page.getByRole('heading', { name: /Sign in/i })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Primary' })).toHaveCount(0);
});

test('a bad API key surfaces an error and does not sign in', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('API key').fill('not-a-real-key');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByTestId('login-error')).toContainText('invalid api key');
});

test('provider discovery failures fail closed without exposing API-key login', async ({ page }) => {
  await page.route('**/v1/auth/oidc/providers', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'identity service unavailable' })
    })
  );

  await page.goto('/engine');
  await expect(page.getByRole('heading', { name: 'Unable to verify your session' })).toBeVisible();
  await expect(page.getByText('identity service unavailable')).toBeVisible();
  await expect(page.getByLabel('API key')).toHaveCount(0);

  await page.goto('/login');
  await expect(
    page.getByRole('heading', { name: 'Sign-in methods are unavailable' })
  ).toBeVisible();
  await expect(page.getByLabel('API key')).toHaveCount(0);
});

test('every sign-out surface retains the identity and reports server revocation failure', async ({
  page
}) => {
  await page.goto('/login');
  await page.getByLabel('API key').fill('dev-sandbox-key');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByTestId('user-identity')).toHaveText('dev');
  await page.route('**/v1/logout', (route) =>
    route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'session store unavailable' })
    })
  );

  await page.goto('/me');
  await page
    .getByLabel('Current account details')
    .getByRole('button', { name: 'Sign out' })
    .click();
  await expect(page.getByRole('heading', { name: 'Signed in as dev' })).toBeVisible();
  await expect(
    page.getByRole('alert').filter({ hasText: 'session store unavailable' })
  ).toBeVisible();

  await page.getByTestId('persona-switch').locator('summary').click();
  await page.getByTestId('persona-switch').getByRole('button', { name: 'Sign out' }).click();
  await expect(page.getByTestId('user-identity')).toHaveText('dev');

  await page.keyboard.press('Control+k');
  await page.getByRole('option', { name: /Sign out/ }).click();
  await expect(page.getByTestId('user-identity')).toHaveText('dev');
});
