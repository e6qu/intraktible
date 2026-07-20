// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

const shauthURL = process.env.SHAUTH_URL;
const intraktibleURL = process.env.INTRAKTIBLE_URL;
const password = process.env.SHAUTH_BOOTSTRAP_ADMIN_PASSWORD;
assert.ok(shauthURL, 'SHAUTH_URL is required');
assert.ok(intraktibleURL, 'INTRAKTIBLE_URL is required');
assert.ok(password, 'SHAUTH_BOOTSTRAP_ADMIN_PASSWORD is required');

const browser = await chromium.launch({
  headless: true,
  ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH }
    : {})
});
try {
  const context = await browser.newContext();
  const page = await context.newPage();

  async function completeShauthLogin() {
    await page.waitForURL(`${shauthURL}/login**`);
    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Sign in with password' }).click();
    await page.waitForURL(`${intraktibleURL}/**`);
    await page.getByTestId('user-identity').waitFor();
    assert.equal(
      (await page.getByTestId('user-identity').textContent())?.trim(),
      'admin@localhost.test'
    );
  }

  async function assertGlobalLogout(surface) {
    if (surface === 'account') {
      await page.goto(`${intraktibleURL}/me`);
      await page
        .getByLabel('Current account details')
        .getByRole('button', { name: 'Sign out' })
        .click();
    } else if (surface === 'palette') {
      await page.keyboard.press('Control+k');
      await page.getByRole('option', { name: /Sign out/ }).click();
    } else {
      await page.getByTestId('persona-switch').locator('summary').click();
      await page.getByTestId('persona-switch').getByRole('button', { name: 'Sign out' }).click();
    }

    await page.waitForURL(`${intraktibleURL}/v1/auth/signed-out`);
    await page.getByRole('heading', { name: 'You are signed out' }).waitFor();
    await page.reload();
    assert.equal(page.url(), `${intraktibleURL}/v1/auth/signed-out`);
    const meStatus = await page.evaluate(async () => (await fetch('/v1/me')).status);
    assert.equal(meStatus, 401, `${surface} logout retained the Intraktible session`);

    await page.goto(`${intraktibleURL}/engine`);
    await page.waitForURL(`${shauthURL}/login**`);
    await page.getByRole('heading', { name: 'Sign in to Shauth' }).waitFor();
    assert.equal(
      await page.getByLabel('API key').count(),
      0,
      `${surface} logout exposed Intraktible's API-key prompt`
    );
  }

  // Direct protected entry uses the real Authorization Code + PKCE flow with a
  // confidential client authenticated through client_secret_post.
  await page.goto(`${intraktibleURL}/engine`);
  await completeShauthLogin();
  assert.ok(page.url().startsWith(`${intraktibleURL}/engine`));

  // Remove only Intraktible's application cookie while preserving Shauth's
  // browser session, then enter through the real Shauth catalog. This proves the
  // catalog launch performs SSO without another credential prompt.
  await context.clearCookies({ name: 'session', domain: 'localhost' });
  await page.goto(`${shauthURL}/apps`);
  await page.getByRole('heading', { name: 'Apps' }).waitFor();
  await page.getByRole('link', { name: 'Open Intraktible' }).click();
  await page.waitForURL(`${intraktibleURL}/**`);
  await page.getByTestId('user-identity').waitFor();

  for (const surface of ['header', 'account', 'palette']) {
    await assertGlobalLogout(surface);
    if (surface !== 'palette') await completeShauthLogin();
  }
} finally {
  await browser.close();
}
