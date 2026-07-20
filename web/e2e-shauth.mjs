// SPDX-License-Identifier: AGPL-3.0-or-later

import assert from 'node:assert/strict';
import { chromium } from 'playwright';

const shauthURL = process.env.SHAUTH_URL;
const intraktibleURL = process.env.INTRAKTIBLE_URL;
const validationUsername = process.env.SHAUTH_VALIDATION_USERNAME;
const validationPassword = process.env.SHAUTH_VALIDATION_PASSWORD;
const rejectionSentinel = process.env.INTRAKTIBLE_REJECTION_SENTINEL;
assert.ok(shauthURL, 'SHAUTH_URL is required');
assert.ok(intraktibleURL, 'INTRAKTIBLE_URL is required');
assert.ok(validationUsername, 'SHAUTH_VALIDATION_USERNAME is required');
assert.ok(validationPassword, 'SHAUTH_VALIDATION_PASSWORD is required');
assert.ok(rejectionSentinel, 'INTRAKTIBLE_REJECTION_SENTINEL is required');
assert.notEqual(
  rejectionSentinel,
  validationPassword,
  'the relying-party rejection sentinel must not be a real Shauth credential'
);
const validationURL = `${intraktibleURL}/me`;
const signedOutURL = `${intraktibleURL}/v1/auth/signed-out`;
const shauthOrigin = new URL(shauthURL).origin;
const exactShauthLoginURL = new URL('/login', shauthOrigin).href;

function safeShauthReturn(value) {
  if (!value) return true;
  let destination;
  try {
    destination = new URL(value, shauthOrigin);
  } catch {
    return false;
  }
  return (
    destination.origin === shauthOrigin &&
    destination.username === '' &&
    destination.password === ''
  );
}

function classifyCredentialRequest({ url, method, headers = {}, postData = '' }) {
  const passwordEncodings = new Set([
    validationPassword,
    encodeURIComponent(validationPassword),
    Buffer.from(validationPassword).toString('base64'),
    Buffer.from(`${validationUsername}:${validationPassword}`).toString('base64')
  ]);
  const includesCredential = (value) =>
    [...passwordEncodings].some((encoding) => String(value).includes(encoding));
  const credentialInURL = includesCredential(url);
  const credentialHeaders = Object.entries(headers).filter(([, value]) =>
    includesCredential(value)
  );
  const credentialInBody = includesCredential(postData);
  const containsCredential = credentialInURL || credentialHeaders.length > 0 || credentialInBody;
  if (!containsCredential) return { containsCredential: false, allowed: true, reason: '' };

  let target;
  try {
    target = new URL(url);
  } catch {
    return { containsCredential: true, allowed: false, reason: 'invalid target URL' };
  }
  if (method.toUpperCase() !== 'POST') {
    return { containsCredential: true, allowed: false, reason: 'non-POST credential request' };
  }
  if (target.href !== exactShauthLoginURL) {
    return { containsCredential: true, allowed: false, reason: 'non-Shauth login target' };
  }
  if (credentialInURL || credentialHeaders.length > 0) {
    return { containsCredential: true, allowed: false, reason: 'credential outside form body' };
  }

  const contentType = Object.entries(headers).find(
    ([name]) => name.toLowerCase() === 'content-type'
  )?.[1];
  if (!contentType?.toLowerCase().startsWith('application/x-www-form-urlencoded')) {
    return { containsCredential: true, allowed: false, reason: 'non-form credential body' };
  }
  const form = new URLSearchParams(postData);
  if (
    form.getAll('username').length !== 1 ||
    form.get('username') !== validationUsername ||
    form.getAll('password').length !== 1 ||
    form.get('password') !== validationPassword
  ) {
    return { containsCredential: true, allowed: false, reason: 'invalid Shauth login form' };
  }
  for (const [name, value] of form) {
    if (name !== 'password' && includesCredential(value)) {
      return {
        containsCredential: true,
        allowed: false,
        reason: 'credential in another form field'
      };
    }
    if (
      /^(next|redirect_uri|return_to|post_logout_redirect_uri)$/i.test(name) &&
      !safeShauthReturn(value)
    ) {
      return { containsCredential: true, allowed: false, reason: 'cross-origin login return' };
    }
  }
  return { containsCredential: true, allowed: true, reason: '' };
}

function assertCredentialBoundaryPolicy() {
  const headers = { 'content-type': 'application/x-www-form-urlencoded' };
  const allowedBody = new URLSearchParams({
    username: validationUsername,
    password: validationPassword,
    next: '/oauth/login?login_challenge=challenge'
  }).toString();
  assert.equal(
    classifyCredentialRequest({
      url: exactShauthLoginURL,
      method: 'POST',
      headers,
      postData: allowedBody
    }).allowed,
    true,
    'the exact Shauth password-login request was blocked'
  );

  const hostileOrigin = new URL(shauthOrigin);
  hostileOrigin.hostname = `${hostileOrigin.hostname}.invalid`;
  const mutations = [
    { url: `${intraktibleURL}/v1/login`, method: 'POST', headers, postData: allowedBody },
    { url: new URL('/login', hostileOrigin).href, method: 'POST', headers, postData: allowedBody },
    { url: `${exactShauthLoginURL}/`, method: 'POST', headers, postData: allowedBody },
    { url: exactShauthLoginURL, method: 'GET', headers, postData: allowedBody },
    {
      url: `${exactShauthLoginURL}?password=${encodeURIComponent(validationPassword)}`,
      method: 'POST',
      headers,
      postData: allowedBody
    },
    {
      url: exactShauthLoginURL,
      method: 'POST',
      headers,
      postData: new URLSearchParams({
        username: validationUsername,
        password: validationPassword,
        next: `${intraktibleURL}/credential-capture`
      }).toString()
    },
    {
      url: exactShauthLoginURL,
      method: 'POST',
      headers: { ...headers, Authorization: `Bearer ${validationPassword}` },
      postData: allowedBody
    }
  ];
  for (const mutation of mutations) {
    assert.equal(
      classifyCredentialRequest(mutation).allowed,
      false,
      `credential boundary accepted mutated request ${mutation.method} ${new URL(mutation.url).origin}${new URL(mutation.url).pathname}`
    );
  }
}

assertCredentialBoundaryPolicy();

const browser = await chromium.launch({
  headless: true,
  ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH }
    : {})
});
try {
  const context = await browser.newContext();
  const credentialBoundaryViolations = [];
  let allowedCredentialSubmissions = 0;
  await context.route('**/*', async (route) => {
    const request = route.request();
    const decision = classifyCredentialRequest({
      url: request.url(),
      method: request.method(),
      headers: await request.allHeaders(),
      postData: request.postData() ?? ''
    });
    if (!decision.containsCredential) {
      await route.continue();
      return;
    }
    if (decision.allowed) {
      allowedCredentialSubmissions += 1;
      await route.continue();
      return;
    }
    const target = new URL(request.url());
    credentialBoundaryViolations.push(
      `${request.method()} ${target.origin}${target.pathname}: ${decision.reason}`
    );
    await route.abort('blockedbyclient');
  });
  const page = await context.newPage();

  async function completeShauthLogin() {
    await page.waitForURL(`${shauthURL}/login**`);
    const loginPage = new URL(page.url());
    assert.equal(loginPage.origin, shauthOrigin, 'the credential page was not served by Shauth');
    assert.equal(loginPage.pathname, '/login', 'Shauth credentials were requested on another path');
    const submit = page.getByRole('button', { name: 'Sign in with password' });
    const form = submit.locator('xpath=ancestor::form[1]');
    const formAction = new URL((await form.getAttribute('action')) ?? page.url(), page.url());
    assert.equal(
      formAction.href,
      exactShauthLoginURL,
      'the Shauth login form targeted another URL'
    );
    assert.equal(
      ((await form.getAttribute('method')) ?? 'get').toUpperCase(),
      'POST',
      'the Shauth login form did not use POST'
    );
    const submissionsBeforeLogin = allowedCredentialSubmissions;
    await page.getByLabel('Username').fill(validationUsername);
    await page.getByLabel('Password').fill(validationPassword);
    await submit.click();
    await page.waitForURL(`${intraktibleURL}/**`);
    assert.equal(
      allowedCredentialSubmissions,
      submissionsBeforeLogin + 1,
      'Shauth login did not make exactly one boundary-approved credential request'
    );
    assert.deepEqual(
      credentialBoundaryViolations,
      [],
      'the browser attempted to send a Shauth credential outside the identity service'
    );
    await page.getByTestId('user-identity').waitFor();
    assert.equal(
      (await page.getByTestId('user-identity').textContent())?.trim(),
      'admin@localhost.test'
    );
  }

  async function currentAppSessionCookie() {
    const session = (await context.cookies(intraktibleURL)).find(
      (cookie) => cookie.name === 'session'
    );
    assert.ok(session, 'Intraktible did not issue its application session cookie');
    return session;
  }

  async function assertProtectedAPIsRejectStaleCookie(label, staleCookie) {
    await context.addCookies([staleCookie]);
    for (const path of ['/v1/me', '/v1/flows']) {
      const response = await context.request.get(`${intraktibleURL}${path}`);
      assert.equal(response.status(), 401, `${label} left ${path} accessible with a stale cookie`);
    }
    await context.clearCookies({ name: 'session', domain: 'localhost' });
  }

  async function assertAppSignedOutLanding() {
    await page.waitForURL(signedOutURL);
    await page.getByRole('heading', { name: 'You are signed out' }).waitFor();
    const signIn = page.getByRole('link', { name: 'Sign in with Shauth', exact: true });
    assert.equal(await signIn.getAttribute('href'), '/v1/auth/oidc/shauth/login?return_to=%2F');
    await page.reload();
    assert.equal(page.url(), signedOutURL);
    await page.getByRole('heading', { name: 'You are signed out' }).waitFor();
  }

  async function recoverFromAppSignedOut() {
    await page.getByRole('link', { name: 'Sign in with Shauth', exact: true }).click();
    await completeShauthLogin();
  }

  async function assertRelyingPartyLogout(surface) {
    const staleSession = await currentAppSessionCookie();
    if (surface === 'account') {
      await page.goto(validationURL);
      const signOut = page
        .getByLabel('Current account details')
        .getByRole('button', { name: 'Sign out', exact: true });
      await signOut.waitFor();
      await signOut.click();
    } else if (surface === 'palette') {
      await page.keyboard.press('Control+k');
      await page.getByRole('option', { name: 'Sign out', exact: true }).click();
    } else {
      await page.getByTestId('persona-switch').locator('summary').click();
      await page
        .getByTestId('persona-switch')
        .getByRole('button', { name: 'Sign out', exact: true })
        .click();
    }

    await assertAppSignedOutLanding();
    await assertProtectedAPIsRejectStaleCookie(`${surface} relying-party logout`, staleSession);
    await page.goto(signedOutURL);
    await recoverFromAppSignedOut();
    assert.equal(
      await page.getByLabel('API key').count(),
      0,
      `${surface} logout exposed Intraktible's API-key prompt`
    );
  }

  async function assertRelyingPartyCredentialSurfacesRejectSentinel() {
    const apiKeyLogin = await context.request.post(`${intraktibleURL}/v1/login`, {
      data: { api_key: rejectionSentinel }
    });
    assert.equal(
      apiKeyLogin.status(),
      401,
      'a non-authentic sentinel authenticated through Intraktible local login'
    );

    const passwordLogin = await context.request.post(`${intraktibleURL}/v1/login`, {
      data: { username: 'not-a-shauth-user', password: rejectionSentinel }
    });
    assert.equal(
      passwordLogin.status(),
      400,
      'Intraktible accepted a username/password body on its API-key-only local login'
    );

    const authHeaders = [
      ['X-Api-Key', { 'X-Api-Key': rejectionSentinel }],
      [
        'HTTP Basic',
        {
          Authorization: `Basic ${Buffer.from(`not-a-shauth-user:${rejectionSentinel}`).toString('base64')}`
        }
      ],
      ['Bearer', { Authorization: `Bearer ${rejectionSentinel}` }]
    ];
    for (const [surface, headers] of authHeaders) {
      const response = await context.request.get(`${intraktibleURL}/v1/me`, { headers });
      assert.equal(
        response.status(),
        401,
        `a non-authentic sentinel authenticated through Intraktible ${surface}`
      );
    }

    const session = (await context.cookies(intraktibleURL)).find(
      (cookie) => cookie.name === 'session'
    );
    assert.equal(session, undefined, 'a rejected relying-party sentinel created a session');
  }

  const versionResponse = await context.request.get(`${intraktibleURL}/version`);
  assert.equal(versionResponse.status(), 200, 'Intraktible build metadata is unavailable');
  assert.equal(versionResponse.headers()['cache-control'], 'no-store');
  const version = await versionResponse.json();
  assert.equal(version.service, 'intraktible');
  assert.match(version.revision, /^[0-9a-f]{40}$/);
  assert.ok(version.go, 'Intraktible build metadata omitted the Go toolchain');

  await assertRelyingPartyCredentialSurfacesRejectSentinel();

  // Direct protected entry uses the real Authorization Code + PKCE flow with a
  // confidential client authenticated through client_secret_post.
  await page.goto(`${intraktibleURL}/engine`);
  await completeShauthLogin();
  assert.ok(page.url().startsWith(`${intraktibleURL}/engine`));

  // Remove only Intraktible's application cookie while preserving Shauth's
  // browser session, then enter directly again. The protected app route must
  // complete silent SSO without showing either credential form.
  await context.clearCookies({ name: 'session', domain: 'localhost' });
  await page.goto(`${intraktibleURL}/engine`);
  await page.waitForURL(`${intraktibleURL}/engine`);
  await page.getByTestId('user-identity').waitFor();

  // Repeat the same cookie-loss recovery through Shauth's real app catalog.
  await context.clearCookies({ name: 'session', domain: 'localhost' });
  await page.goto(`${shauthURL}/apps`);
  await page.getByRole('heading', { name: 'Apps' }).waitFor();
  await page.getByRole('link', { name: 'Open Intraktible' }).click();
  await page.waitForURL(`${intraktibleURL}/**`);
  await page.getByTestId('user-identity').waitFor();

  for (const surface of ['header', 'account', 'palette']) {
    await assertRelyingPartyLogout(surface);
  }

  // Provider-global logout starts at Shauth, not Intraktible. Its signed
  // Back-Channel Logout token must invalidate the still-present app cookie and
  // every protected API before the browser returns to Intraktible.
  const providerStaleSession = await currentAppSessionCookie();
  await page.goto(`${shauthURL}/logout`);
  await page.getByRole('button', { name: 'Sign out everywhere', exact: true }).click();
  await page.waitForURL(`${shauthURL}/signed-out`);
  await page.getByRole('heading', { name: 'You are signed out' }).waitFor();
  await assertProtectedAPIsRejectStaleCookie('Shauth provider-global logout', providerStaleSession);
  await page.goto(signedOutURL);
  await assertAppSignedOutLanding();
  await recoverFromAppSignedOut();
  assert.ok(allowedCredentialSubmissions > 0, 'the browser did not exercise Shauth password login');
  assert.deepEqual(
    credentialBoundaryViolations,
    [],
    'the browser attempted to send a Shauth credential outside the identity service'
  );
} finally {
  await browser.close();
}
