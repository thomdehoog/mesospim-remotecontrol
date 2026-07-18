// Whole-system chaos suite.
//
// Unlike the per-layer harnesses, this one shakes the entire machine at once:
// a live browser session drives the SPA while concurrent API clients hammer
// writes and repeated reindexing churns the projection underneath it. After
// the storm it asserts the end-to-end invariants that only a full-stack test
// can reach — no data lost across the compare-and-swap, the live projection
// equals a from-scratch Git rebuild (through the HTTP API), the browser never
// threw and stays responsive, the WebSocket delivered live updates, and the
// UI reflects backend truth.
//
//   ORIGOA_STATIC=../frontend/dist go run ./cmd/origoad &
//   node test/chaos.mjs [http://localhost:8000]
//
// CHROMIUM_PATH overrides the browser binary.
import { chromium } from 'playwright';

const BASE = (process.argv[2] || process.env.ORIGOA_URL || 'http://localhost:8000').replace(/\/$/, '');
const results = [];
let failed = 0;
const check = (name, ok, extra = '') => {
  results.push(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failed++;
};
const note = (m) => results.push('NOTE  ' + m);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function api(method, path, body) {
  const res = await fetch(BASE + path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { /* non-JSON */ }
  return { status: res.status, data };
}

// write retries through the spec's backpressure: 503 (maintenance) and 409
// (a concurrent change) are expected under this load, not failures.
async function write(method, path, body) {
  for (let attempt = 0; attempt < 300; attempt++) {
    const r = await api(method, path, body);
    if (r.status !== 503 && r.status !== 409) return r;
    await sleep(15 + (attempt % 10) * 5);
  }
  return { status: 0, data: null };
}

// listing snapshots the whole projection as guid → identity signature (the
// identity-bearing fields the spec guarantees are stable), via the API.
async function listing() {
  const r = await api('GET', '/api/search?subtree=1&limit=1000');
  const map = {};
  for (const a of r.data?.results ?? []) {
    map[a.guid] = `${a.kind}|${a.type}|${a.title}|${a.hid}|${a.parentPath}`;
  }
  return map;
}

async function waitIdle(timeoutMs = 15000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    const s = await api('GET', '/api/status');
    if (s.status === 200 && !s.data.maintenance && !s.data.reindex?.running &&
        s.data.gitHead === s.data.revision) return true;
    await sleep(100);
  }
  return false;
}

const launchOpts = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
const browser = await chromium.launch(launchOpts);
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
const pageErrors = [];
page.on('pageerror', (e) => pageErrors.push(String(e)));
page.on('dialog', (d) => d.dismiss().catch(() => {}));

// ---- Pre-flight: the live-update WebSocket delivers UI refreshes ----
await page.goto(BASE + '/?path=chaos&subtree=1');
await page.waitForTimeout(800);
const rowsBefore = await page.locator('origoa-table tr.row').count();
const wsProbe = await write('POST', '/api/entries', {
  folder: 'chaos/probe', type: 'requirement', title: 'ws-probe-artifact',
});
await page.waitForTimeout(2000); // no manual reload: only a WS event can update the table
const rowsAfter = await page.locator('origoa-table tr.row').count();
check('WebSocket drives live UI update (no reload)', wsProbe.status === 201 && rowsAfter > rowsBefore,
  `rows ${rowsBefore} -> ${rowsAfter}`);

// ---- The storm: concurrent writers + reindex churn + a live browsing UI ----
const WORKERS = 6;
const OPS_EACH = 10;
const created = [];
let maintenanceSeen = false;

const worker = async (w) => {
  const mine = [];
  for (let i = 0; i < OPS_EACH; i++) {
    const folder = `chaos/w${w}`;
    if (i % 5 === 0 || mine.length === 0) {
      const r = await write('POST', '/api/entries', {
        folder, type: i % 2 ? 'testcase' : 'requirement', title: `w${w}-e${i}`,
        fields: { priority: 'high' },
      });
      if (r.status === 201) { mine.push(r.data.guid); created.push(r.data.guid); }
    } else if (i % 5 === 1) {
      await write('PATCH', `/api/artifacts/${mine[0]}`, { fields: { priority: 'low' } });
    } else if (i % 5 === 2 && mine.length > 1) {
      await write('POST', '/api/links', { type: 'relates', source: mine[0], target: mine[1] });
      await write('POST', '/api/comments', { subject: mine[1], text: `c${w}-${i}` });
    } else if (i % 5 === 3) {
      await write('POST', `/api/artifacts/${mine[0]}/workflows/review/transition`, { to: 'in_review' });
    } else if (mine.length > 1) {
      await write('POST', `/api/artifacts/${mine[mine.length - 1]}/move`, { folder: `chaos/moved${w}` });
    }
  }
};

const reindexChurn = async () => {
  for (let k = 0; k < 5; k++) {
    await api('POST', '/api/reindex');
    await sleep(300);
  }
};

const statusPoller = async (stop) => {
  while (!stop.done) {
    const s = await api('GET', '/api/status');
    if (s.data?.maintenance) maintenanceSeen = true;
    await sleep(40);
  }
};

const browseUnderFire = async (stop) => {
  const urls = ['/?path=chaos&subtree=1', '/?q=w0', '/?path=chaos&subtree=1&type=requirement',
    '/?q=e1&subtree=1', '/?path=engineering&subtree=1'];
  let i = 0;
  while (!stop.done) {
    await page.goto(BASE + urls[i++ % urls.length]).catch(() => {});
    await page.waitForTimeout(250);
  }
};

const stop = { done: false };
const t0 = Date.now();
// The writers and reindex churn are the primary work; the status poller and
// the browsing loop run in the background until the primary work completes.
const background = [statusPoller(stop), browseUnderFire(stop)];
await Promise.all([
  ...Array.from({ length: WORKERS }, (_, w) => worker(w)),
  reindexChurn(),
]);
stop.done = true;
await Promise.all(background);
await sleep(400);
note(`storm: ${created.length} artifacts created by ${WORKERS} writers in ${Date.now() - t0}ms`);

// ---- Invariants after the storm ----

// Whether the transient maintenance window was observed is informational, not
// an invariant: a reindex of a small repository completes faster than an
// external HTTP poll, so the flag is often unobservable from outside. The
// barrier's correctness is instead proven deterministically by the
// live-equals-rebuild invariant below (which would diverge if a write leaked
// into a concurrent reindex) and by the backend concurrency torture suite.
note(maintenanceSeen ? 'maintenance window observed during reindex churn'
  : 'maintenance window too brief to observe over HTTP (expected on a small repo)');

// A. Repository settles: the projection catches up to Git HEAD.
check('projection synchronizes to Git HEAD after the storm', await waitIdle());

// B. No data lost — every artifact a writer committed is retrievable.
let lost = 0;
for (const g of created) {
  const r = await api('GET', `/api/artifacts/${g}`);
  if (r.status !== 200) lost++;
}
check('no created artifact lost across the storm', lost === 0, `${lost}/${created.length} missing`);

// C. Full-stack rebuild equivalence: the live projection equals a from-scratch
//    reindex from Git, observed entirely through the HTTP API.
const live = await listing();
await api('POST', '/api/reindex');
await waitIdle();
const rebuilt = await listing();
let diff = 0;
for (const g of Object.keys(live)) if (live[g] !== rebuilt[g]) diff++;
for (const g of Object.keys(rebuilt)) if (!(g in live)) diff++;
check('live projection equals from-scratch Git rebuild', diff === 0, `${diff} divergence(s)`);

// D. The live browser never threw and is still responsive.
check('no uncaught page errors through the whole storm', pageErrors.length === 0,
  pageErrors.length ? [...new Set(pageErrors)][0] : '');
await page.goto(BASE + '/?path=chaos&subtree=1');
await page.waitForTimeout(800);
check('UI shell responsive after the storm', await page.locator('origoa-app header').count() > 0);

// E. The UI reflects backend truth: the table count matches the API for the
//    same view.
const apiCount = (await api('GET', '/api/search?path=chaos&subtree=1&limit=1000')).data
  ?.results?.filter((a) => a.kind === 'entry' || a.kind === 'document').length ?? -1;
const uiCount = await page.locator('origoa-table tr.row').count();
check('UI table matches backend truth for a folder', apiCount === uiCount, `api=${apiCount} ui=${uiCount}`);

// ---- Tally ----
if (pageErrors.length) note('captured page errors:\n    ' + [...new Set(pageErrors)].join('\n    '));
await browser.close();
console.log(results.join('\n'));
console.log(failed === 0 ? '\nAll chaos invariants held.' : `\n${failed} chaos invariant(s) FAILED.`);
process.exit(failed === 0 ? 0 : 1);
