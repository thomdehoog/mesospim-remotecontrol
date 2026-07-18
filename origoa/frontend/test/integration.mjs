// Frontend integration (end-to-end) suite.
//
// Drives the real SPA against a running backend seeded with the demo dataset
// (cmd/origoa-seed), exercising the full happy path each MVP success criterion
// requires: schema-driven navigation and detail views, workflow transitions,
// field editing with optimistic concurrency, overlay resolution, the document
// editor with entry references, search, and creating an artifact through the
// UI with no direct Git interaction. Every step crosses the whole stack —
// browser → REST/WebSocket → repository services → Git + PostgreSQL.
//
//   go run ./cmd/origoa-seed
//   ORIGOA_STATIC=../frontend/dist go run ./cmd/origoad &
//   node test/integration.mjs [http://localhost:8000]
//
// CHROMIUM_PATH overrides the browser binary; run against a freshly seeded
// repository (the assertions expect the demo dataset's exact shape).
import { chromium } from 'playwright';

const BASE = (process.argv[2] || process.env.ORIGOA_URL || 'http://localhost:8000').replace(/\/$/, '');
const results = [];
let failed = 0;
const check = (name, ok, extra = '') => {
  results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failed++;
};

const launchOpts = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
const browser = await chromium.launch(launchOpts);
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.on('pageerror', (e) => check('no uncaught page errors', false, String(e)));

// 1. Root view: sidebar, artifact table, folders, type-based navigation.
await page.goto(BASE + '/?subtree=1');
await page.waitForTimeout(1200);
const sidebar = page.locator('origoa-sidebar');
check('sidebar renders folders', await sidebar.locator('.node', { hasText: 'engineering' }).count() > 0);
check('type navigation shows Requirement', await sidebar.locator('.type-item', { hasText: 'Requirement' }).count() > 0);
const table = page.locator('origoa-table');
check('artifact table lists subtree artifacts', await table.locator('tr.row').count() === 8);

// 2. Schema-driven entry detail view.
await table.locator('tr.row', { hasText: 'Controller boots' }).click();
await page.waitForTimeout(1200);
const detail = page.locator('origoa-detail');
check('detail shows generated HID', (await detail.locator('.head .hid').textContent())?.trim() === 'REQ-1');
check('schema-driven fields rendered', await detail.locator('origoa-field').count() >= 4);
const wfPanel = detail.locator('origoa-workflow-panel');
check('workflow state shown', (await wfPanel.locator('.state').first().textContent())?.includes('In Review'));
check('relationships listed', await detail.locator('origoa-links-panel .item').count() === 2);
check('comment thread rendered', await detail.locator('origoa-comments-panel .comment').count() === 2);
check('history shows structured commit messages',
  (await detail.locator('origoa-history-panel .item').first().textContent())?.length > 0);
check('deep-link URL reflects the selection', page.url().includes('sel='));
await wfPanel.locator('button[title="Show workflow diagram"]').click();
await page.waitForTimeout(300);
check('workflow diagram rendered', await wfPanel.locator('svg rect').count() === 4);

// 3. Workflow transition through the UI.
await wfPanel.locator('select').selectOption({ index: 0 });
await wfPanel.locator('button.primary', { hasText: 'Apply' }).click();
await page.waitForTimeout(1200);
check('workflow transition applied via UI',
  (await wfPanel.locator('.state').first().textContent())?.includes('Approved'));

// 4. Field edit + save (optimistic-concurrency happy path).
await detail.locator('origoa-field').filter({ hasText: 'Priority' }).locator('select').selectOption('medium');
await detail.locator('button.primary', { hasText: 'Save changes' }).click();
await page.waitForTimeout(1200);
check('field edit saved', (await detail.locator('button.primary').first().textContent())?.includes('Saved'));

// 5. Overlay resolution on the product variant.
await page.goto(BASE + '/?path=catalog');
await page.waitForTimeout(800);
await page.locator('origoa-table tr.row', { hasText: 'rugged variant' }).click();
await page.waitForTimeout(1200);
check('overlay composition visualized',
  await page.locator('origoa-detail origoa-overlay-panel .level').count() === 2);
check('inherited fields marked',
  await page.locator('origoa-detail origoa-field .inherited').count() >= 1);

// 6. Document editor with entry references and a sidebar.
await page.goto(BASE + '/?path=engineering');
await page.waitForTimeout(800);
await page.locator('origoa-table tr.row', { hasText: 'System Specification' }).click();
await page.waitForTimeout(1500);
const docEd = page.locator('origoa-document-editor');
check('document sections rendered', await docEd.locator('.section').count() >= 4);
check('entry references resolved to titles',
  (await docEd.locator('.entry-ref').first().textContent())?.includes('REQ-1'));
await docEd.locator('.toolbar button', { hasText: 'comments' }).click();
await page.waitForTimeout(600);
check('document comment sidebar', await docEd.locator('aside .comment').count() === 1);

// 7. Full-text search.
await page.goto(BASE + '/?q=firmware');
await page.waitForTimeout(1000);
check('full-text search finds firmware artifacts',
  await page.locator('origoa-table tr.row').count() === 2);

// 8. Create an artifact through the UI (no direct Git interaction).
await page.goto(BASE + '/?path=engineering%2Frequirements');
await page.waitForTimeout(800);
await page.locator('origoa-app header button', { hasText: '+ Artifact' }).click();
await page.waitForTimeout(600);
const dialog = page.locator('origoa-create-dialog');
await dialog.locator('#type').selectOption('requirement');
await dialog.locator('#title').fill('Created through the browser');
await dialog.locator('button.primary', { hasText: 'Create' }).click();
await page.waitForTimeout(1500);
check('entry created via UI with an assigned HID',
  (await page.locator('origoa-detail .head .hid').textContent())?.startsWith('REQ-'));

await browser.close();
console.log(results.join('\n'));
console.log(failed === 0 ? '\nAll integration checks passed.' : `\n${failed} integration check(s) FAILED.`);
process.exit(failed === 0 ? 0 : 1);
