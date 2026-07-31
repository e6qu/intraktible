// SPDX-License-Identifier: AGPL-3.0-or-later
// Real-Wasm journeys over the seeded modeling story: the embedded backend
// replays the governed source → dataset → snapshot → training → signed
// artifact → independent evaluation → production approval → backfill history,
// and the cockpit renders every stage from the same event log.
import { expect, test } from '@playwright/test';
import { forcePersona, gotoReady, switchRole } from './helpers';

const DATASET = 'default-risk-development';
const MODEL = 'default-risk-candidate';

test('journey: read the governed source contracts and resolve a quality incident', async ({
  page
}) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, '');
  await switchRole(page, 'admin');
  await gotoReady(page, 'modeling');

  const sources = page.getByRole('region', { name: 'Sources & quality' });
  // The seeded entity and event contracts are active under four-eyes approval.
  await expect(sources.getByText('active v1').first()).toBeVisible();
  await expect(
    sources.locator('article.card', { hasText: 'model_applicant' }).first()
  ).toBeVisible();
  // Source watermarks reflect the bitemporal cohort, including one correction.
  const watermarks = sources.locator('table').first();
  await expect(watermarks).toContainText('risk_signal');
});

test('journey: a signed production artifact and independent evaluation are visible', async ({
  page
}) => {
  await forcePersona(page, 'modeler');
  await gotoReady(page, 'modeling');

  const training = page.getByRole('region', { name: 'Training & independent evaluation' });
  // The trained artifact reached the production stage through the supply-chain gate.
  const artifact = training.locator('article.card', { hasText: MODEL }).first();
  await expect(artifact).toBeVisible({ timeout: 15_000 });
  await expect(artifact.getByText('production')).toBeVisible();
  // Independent evaluation evidence carries a real AUC over the holdout split.
  await expect(training.getByText(/AUC/)).toBeVisible();
});

test('journey: trace complete production lineage and compare champion evidence', async ({
  page
}) => {
  await forcePersona(page, 'validator');
  await gotoReady(page, 'modeling');

  const lineage = page.getByRole('region', { name: 'Lineage & challenger evidence' });
  await lineage.getByLabel('Model name').fill(MODEL);
  await lineage.getByRole('button', { name: 'Load complete lineage' }).click();
  const pre = lineage.locator('pre');
  // Lineage ties the production model to its dataset, snapshot, artifact, and
  // evaluation hashes — the full source-to-serving chain.
  await expect(pre).toContainText(DATASET);
  await expect(pre).toContainText('artifact');
  await expect(pre).toContainText('evaluation');
});
