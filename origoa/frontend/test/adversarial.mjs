// Frontend adversarial suite.
//
// Drives the running Origoa SPA through hostile conditions the happy-path
// end-to-end test does not exercise: stored-XSS payloads, malformed deep
// links, hostile field input, optimistic-concurrency conflicts, missing
// artifacts, reindex-while-browsing and rapid navigation. Every scenario
// asserts the application neither executes injected script nor throws an
// uncaught error, and degrades gracefully.
//
//   ORIGOA_STATIC=../frontend/dist go run ./cmd/origoad &   # serve the SPA + API
//   node test/adversarial.mjs [http://localhost:8000]
//
// The browser binary may be overridden with CHROMIUM_PATH (defaults to the
// Playwright-managed Chromium).
import { chromium } from 'playwright';

const BASE = (process.argv[2] || process.env.ORIGOA_URL || 'http://localhost:8000').replace(/\/$/, '');
const results = [];
let failed = 0;
const check = (name, ok, extra = '') => {
  results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failed++;
};

const api = async (method, path, body) => {
  const res = await fetch(BASE + path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON */ }
  return { status: res.status, data };
};

const launchOpts = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
const browser = await chromium.launch(launchOpts);
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });

// A global XSS beacon: any executed injected payload sets window.__xss.
await ctx.addInitScript(() => { window.__xss = false; });

const page = await ctx.newPage();
const pageErrors = [];
let dialogFired = false;
page.on('pageerror', (e) => pageErrors.push(String(e)));
page.on('dialog', (d) => { dialogFired = true; d.dismiss().catch(() => {}); });

// True if an injected payload executed, via the window beacon or a dialog.
const xssFired = async () => dialogFired || (await page.evaluate(() => window.__xss === true));

// ---- 1. Malformed deep links must never crash the app ----
const hostileURLs = [
  '/?path=' + encodeURIComponent('../../etc/passwd'),
  '/?path=' + encodeURIComponent('<script>window.__xss=1</script>'),
  '/?sel=not-a-guid',
  '/?sel=' + '11111111-2222-3333-4444-555555555555', // well-formed but absent
  '/?q=' + encodeURIComponent('"><img src=x onerror="window.__xss=1">'),
  '/?path=' + 'a/'.repeat(400),
  '/?subtree=1&type=' + encodeURIComponent('‮righttoleft'),
  '/?unknownparam=1&another=' + encodeURIComponent('{}[]<>'),
];
let urlErrors = 0;
for (const u of hostileURLs) {
  const before = pageErrors.length;
  await page.goto(BASE + u);
  await page.waitForTimeout(400);
  if (pageErrors.length > before) urlErrors++;
}
check('malformed deep links never throw', urlErrors === 0, `${urlErrors} url(s) errored`);
check('app shell survives hostile URLs', await page.locator('origoa-app header').count() > 0);
check('no script executed from URL payloads', !(await xssFired()));

// ---- 2. Missing artifact deep link degrades gracefully ----
await page.goto(BASE + '/?sel=11111111-2222-3333-4444-555555555555');
await page.waitForTimeout(600);
check('absent artifact shows no detail and no crash',
  await page.locator('origoa-detail').count() === 0 && pageErrors.length === 0);

// ---- 3. Stored XSS: safe surfaces must escape, rich text must be sanitized ----
// Find a folder with a schema so we can create through the API.
const XSS = '<img src=x onerror="window.__xss=true">';
const SCRIPT = '<script>window.__xss=true<\/script>';

// Title and single-line text fields are text-bound: must render inert.
const created = await api('POST', '/api/entries', {
  folder: 'engineering/requirements', type: 'requirement',
  title: XSS + ' boom',
  fields: { description: XSS + SCRIPT + ' rich', priority: 'high' },
});
check('create with XSS payload accepted', created.status === 201, `status ${created.status}`);
const xssGuid = created.data?.guid;
if (xssGuid) {
  await page.goto(BASE + '/?sel=' + xssGuid);
  await page.waitForTimeout(900);
  // Title renders as literal text (escaped), so the payload string is visible.
  const titleVal = await page.locator('origoa-detail .head input.title').inputValue().catch(() => '');
  check('XSS title rendered as inert text', titleVal.includes('<img'));
  // The rich-text field must not retain the event handler after sanitization.
  const richHTML = await page.locator('origoa-detail origoa-field .rich').first().innerHTML().catch(() => '');
  check('rich-text onerror handler stripped', !/onerror/i.test(richHTML), richHTML.slice(0, 80));
  check('rich-text <script> stripped', !/<script/i.test(richHTML));
  await page.waitForTimeout(300);
  check('no injected script executed in detail view', !(await xssFired()));
}

// XSS in a comment (text-bound) must not execute.
if (xssGuid) {
  await api('POST', '/api/comments', { subject: xssGuid, author: 'attacker', text: XSS + ' comment' });
  await page.goto(BASE + '/?sel=' + xssGuid);
  await page.waitForTimeout(700);
  const commentText = await page.locator('origoa-detail origoa-comments-panel .comment').first().textContent().catch(() => '');
  check('XSS comment rendered as inert text', commentText.includes('<img'));
  check('no script executed from comment', !(await xssFired()));
}

// ---- 4. Document text block with XSS is sanitized on render ----
const docCreate = await api('POST', '/api/documents', {
  folder: 'engineering', type: 'spec', title: 'Adversarial Doc',
});
if (docCreate.status === 201) {
  const dGuid = docCreate.data.guid;
  await api('PATCH', '/api/artifacts/' + dGuid, {
    sections: [{ id: 's1', heading: 'x', blocks: [{ type: 'text', text: XSS + SCRIPT + ' body' }] }],
  });
  await page.goto(BASE + '/?path=engineering&sel=' + dGuid);
  await page.waitForTimeout(1000);
  const blockHTML = await page.locator('origoa-document-editor .text').first().innerHTML().catch(() => '');
  check('document block onerror stripped', !/onerror/i.test(blockHTML), blockHTML.slice(0, 80));
  check('no script executed in document view', !(await xssFired()));
}

// ---- 5. Hostile field input saved through the UI degrades gracefully ----
if (xssGuid) {
  await page.goto(BASE + '/?sel=' + xssGuid);
  await page.waitForTimeout(700);
  const before = pageErrors.length;
  // Type a huge value plus control characters into the priority-adjacent text.
  const desc = page.locator('origoa-detail origoa-field').filter({ hasText: 'Description' }).locator('.rich');
  if (await desc.count()) {
    await desc.first().evaluate((el, big) => { el.innerHTML = big; el.dispatchEvent(new Event('blur')); },
      'X'.repeat(200000) + ' ⓤⓝⓘⓒⓞⓓⓔ 🔥 ‮ rtl');
    await page.waitForTimeout(300);
    const saveBtn = page.locator('origoa-detail button.primary').first();
    await saveBtn.click().catch(() => {});
    await page.waitForTimeout(1500);
  }
  check('hostile field input did not crash the UI', pageErrors.length === before);
  check('app still responsive after huge input', await page.locator('origoa-app header').count() > 0);
}

// ---- 6. Optimistic-concurrency conflict is surfaced, not swallowed ----
// Uses a page with the live-update WebSocket suppressed, so the client keeps
// the stale revision it loaded (as it would across a dropped connection) and
// the save genuinely collides with the concurrent change.
{
  const r = await api('POST', '/api/entries', {
    folder: 'engineering/requirements', type: 'requirement', title: 'Conflict target',
  });
  if (r.status === 201) {
    const g = r.data.guid;
    const offline = await ctx.newPage();
    const offlineErrors = [];
    offline.on('pageerror', (e) => offlineErrors.push(String(e)));
    // Suppress the live-update WebSocket so the page keeps the revision it
    // loaded, reproducing a save against state changed by another client.
    await offline.routeWebSocket(/\/api\/ws/, (ws) => ws.close());
    await offline.goto(BASE + '/?sel=' + g);
    await offline.waitForTimeout(900);
    // Another client modifies the artifact after this page loaded it.
    await api('PATCH', '/api/artifacts/' + g, { title: 'Changed by someone else' });
    // Make a real schema-field edit so the form is dirty, then save against
    // the now-stale revision.
    await offline.locator('origoa-detail origoa-field').filter({ hasText: 'Priority' })
      .locator('select').selectOption('medium');
    await offline.waitForTimeout(200);
    await offline.locator('origoa-detail button.primary', { hasText: 'Save changes' }).click();
    await offline.waitForTimeout(1200);
    const banner = await offline.locator('origoa-app .banner.error').textContent().catch(() => '');
    check('stale save surfaces a conflict banner', /concurrent|modified|reload/i.test(banner), banner.slice(0, 80));
    check('no crash on concurrency conflict', offlineErrors.length === 0);
    await offline.close();
  }
}

// ---- 7. Reindex while browsing: maintenance is signalled, search degrades cleanly ----
{
  await page.goto(BASE + '/?subtree=1');
  await page.waitForTimeout(500);
  const before = pageErrors.length;
  await api('POST', '/api/reindex');
  // Hammer search/navigation during the maintenance window.
  for (let i = 0; i < 5; i++) {
    await page.goto(BASE + '/?q=firmware&subtree=1');
    await page.waitForTimeout(150);
  }
  await page.waitForTimeout(1500);
  check('reindex-while-browsing throws no page errors', pageErrors.length === before);
  const status = await api('GET', '/api/status');
  check('status endpoint reachable after reindex', status.status === 200);
}

// ---- 8. Rapid back/forward navigation stress ----
{
  const before = pageErrors.length;
  await page.goto(BASE + '/');
  const paths = ['/?path=engineering', '/?path=catalog', '/?q=boot', '/?subtree=1&type=requirement', '/?path=engineering/tests'];
  for (const p of paths) { await page.goto(BASE + p); await page.waitForTimeout(120); }
  for (let i = 0; i < 8; i++) { await page.goBack().catch(() => {}); await page.waitForTimeout(80); }
  for (let i = 0; i < 8; i++) { await page.goForward().catch(() => {}); await page.waitForTimeout(80); }
  check('rapid history navigation throws no errors', pageErrors.length === before);
  check('app shell intact after navigation stress', await page.locator('origoa-app header').count() > 0);
}

// ---- Final tally ----
if (pageErrors.length) {
  results.push('NOTE  captured page errors:\n    ' + [...new Set(pageErrors)].join('\n    '));
}
await browser.close();
console.log(results.join('\n'));
console.log(failed === 0 ? '\nAll adversarial checks passed.' : `\n${failed} adversarial check(s) FAILED.`);
process.exit(failed === 0 ? 0 : 1);
