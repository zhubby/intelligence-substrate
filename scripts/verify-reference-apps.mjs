// Actual compiled Flutter clients against isolated real Go backends.
// This verifies Chrome, not a physical mobile device or a public release.
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createWriteStream } from 'node:fs';
import { mkdir, mkdtemp, readFile, writeFile, stat } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(repo, 'web/package.json'));
const { chromium, expect } = require('@playwright/test');
const { values } = parseArgs({ options: {
  feedback: { type: 'string' }, booking: { type: 'string' },
  'feedback-manifest': { type: 'string' }, 'booking-manifest': { type: 'string' },
} });
for (const key of ['feedback', 'booking', 'feedback-manifest', 'booking-manifest']) {
  assert(values[key], '--' + key + ' is required');
}
const root = await mkdtemp(path.join(repo, 'work/reference-browser-'));
const report = { kind: 'actual-flutter-web-and-go-api', startedAt: new Date().toISOString(),
  physicalDevice: false, storePublished: false, checks: [], artifacts: {}, externalRequests: [] };
const resources = [];
let browser;
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { child.kill('SIGKILL'); reject(new Error('API shutdown timed out')); }, 5000);
    child.once('exit', () => { clearTimeout(timer); resolve(); });
    child.kill('SIGTERM');
  });
}

async function openApplication(mode) {
  const application = path.resolve(values[mode]);
  const manifestPath = path.resolve(values[mode + '-manifest']);
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  assert.equal(manifest.target, 'web');
  assert.equal(manifest.mode, mode);
  const api = new URL(manifest.apiBaseURL);
  assert.equal(api.protocol, 'http:');
  assert.equal(api.hostname, '127.0.0.1');
  assert(api.port && api.pathname === '/' && !api.search && !api.hash);
  const artifact = path.join(path.dirname(manifestPath), manifest.artifact.file);
  assert.equal(hash(await readFile(artifact)), manifest.artifact.sha256);
  report.artifacts[mode] = { sha256: manifest.artifact.sha256, sourceDigest: manifest.sourceDigest, manifest: manifestPath };
  const directory = path.join(root, mode);
  const site = path.join(directory, 'site');
  await mkdir(site, { recursive: true });
  execFileSync('tar', ['-xzf', artifact, '-C', site]);
  const mime = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.json': 'application/json',
    '.wasm': 'application/wasm', '.woff2': 'font/woff2', '.ttf': 'font/ttf', '.otf': 'font/otf', '.png': 'image/png', '.svg': 'image/svg+xml' };
  const server = http.createServer(async (request, response) => {
    try {
      const name = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
      const file = path.resolve(site, '.' + (name === '/' ? '/index.html' : name));
      if (!file.startsWith(site + path.sep) || !(await stat(file)).isFile()) throw new Error('not found');
      response.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
      response.end(await readFile(file));
    } catch { response.writeHead(404); response.end(); }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  resources.push(() => new Promise((resolve) => server.close(resolve)));
  const origin = 'http://127.0.0.1:' + server.address().port;
  const binary = path.join(directory, 'api');
  execFileSync('go', ['build', '-trimpath', '-o', binary, '.'], { cwd: path.join(application, 'backend') });
  const state = path.join(directory, 'state.json');
  let child;
  async function start() {
    // Refuse to use an already running service on the requested API endpoint.
    const probe = http.createServer();
    await new Promise((resolve, reject) => { probe.once('error', reject); probe.listen(Number(api.port), '127.0.0.1', resolve); });
    await new Promise((resolve) => probe.close(resolve));
    const log = createWriteStream(path.join(directory, 'api.log'), { flags: 'a' });
    child = spawn(binary, [], { env: { ...process.env, APP_MODE: mode, APP_STATE_FILE: state,
      APP_LISTEN: '127.0.0.1:' + api.port, APP_WEB_ORIGIN: origin,
      APP_SLOTS: JSON.stringify([{ id: 'first', startsAt: '2026-12-01T09:00:00+08:00', reserved: false }]) },
      stdio: ['ignore', 'ignore', 'pipe'] });
    child.stderr.pipe(log);
    child.once('exit', () => log.end());
    await expect.poll(async () => {
      try { return (await fetch(api.origin + '/api/health')).status; } catch { return 0; }
    }, { timeout: 10000 }).toBe(200);
  }
  resources.push(async () => { if (child) await stop(child); });
  await start();
  return { origin, api: api.origin, state, restart: async () => { await stop(child); await start(); } };
}

async function navigate(page, origin) {
  page.on('pageerror', (error) => report.checks.push({ kind: 'page-error', error: error.message }));
  await page.goto(origin);
  const accessibility = page.locator('flt-semantics-placeholder');
  await accessibility.waitFor({ state: 'attached', timeout: 60000 });
  // Flutter deliberately places this AT-only button outside the viewport.
  // Its documented engine listener accepts an accessibility click directly;
  // keyboard activation can lose focus during the first rendered frame.
  await accessibility.dispatchEvent('click');
  await expect(page.getByRole('button', { name: '刷新' })).toBeVisible();
  // The native-process updater must remain absent from browser builds.
  const updateEntries = await page.getByRole('button', { name: '应用更新', exact: true }).count();
  assert.equal(updateEntries, 0);
  (report.webUpdateEntries ??= []).push({ origin, count: updateEntries });
}

async function visibleText(page, text) {
  await expect.poll(async () => page.locator('flt-semantics').evaluateAll(
    (nodes, expected) => nodes.some((node) =>
      (node.getAttribute('aria-label') || '').includes(expected) || (node.textContent || '').includes(expected)), text,
  ), { timeout: 15000 }).toBe(true);
}

async function enterText(page, name, text) {
  const input = page.getByRole('textbox', { name, exact: true });
  await input.click();
  await expect(input).toBeFocused();
  // DOM focus precedes Flutter's next-frame activation of the text-editing
  // strategy. Typing before that activation is overwritten by its old value.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(input).toBeFocused();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText(text);
  await expect(input).toHaveValue(text);
  await page.keyboard.press('Tab');
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

try {
  const feedback = await openApplication('feedback');
  const booking = await openApplication('booking');
  browser = await chromium.launch({ executablePath: process.env.IS_BROWSER_EXECUTABLE || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  const allowedOrigins = new Set([feedback.origin, booking.origin, feedback.api, booking.api]);
  async function constrainNetwork(context) {
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url());
      if (allowedOrigins.has(url.origin)) return route.continue();
      report.externalRequests.push(url.href);
      await route.abort();
    });
  }
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await constrainNetwork(context);
  const page = await context.newPage();
  await navigate(page, feedback.origin);
  await page.getByRole('button', { name: '提交反馈', exact: true }).click();
  await visibleText(page, '请输入反馈标题。');
  assert.deepEqual(await (await fetch(feedback.api + '/api/feedback')).json(), []);
  const payload = '<script>window.__reference_xss = true</script>';
  await enterText(page, '反馈标题', '真实浏览器反馈');
  await enterText(page, '详细说明', payload);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  report.inputBeforeSubmit = await page.locator('input,textarea').evaluateAll((nodes) => nodes.map((node) => ({ label: node.getAttribute('aria-label'), value: node.value, active: node === document.activeElement })));
  await page.screenshot({ path: path.join(root, 'feedback-before-submit.png'), fullPage: true });
  page.on('request', (request) => { if (request.method() === 'POST') report.checks.push({ kind: 'observed-write', body: request.postData() }); });
  await page.route(feedback.api + '/api/feedback', async (route) => {
    if (route.request().method() === 'POST') await wait(400);
    await route.continue();
  });
  await page.getByRole('button', { name: '提交反馈', exact: true }).dblclick();
  await visibleText(page, payload);
  const records = await (await fetch(feedback.api + '/api/feedback')).json();
  assert.equal(records.length, 1);
  assert.equal(records[0].title, '真实浏览器反馈');
  assert.equal(records[0].content, payload);
  assert.equal(await page.evaluate(() => window.__reference_xss), undefined);
  report.checks.push({ name: 'feedback-validation-submit-single-write-plain-text', passed: true });
  await context.setOffline(true);
  await page.getByRole('button', { name: '刷新' }).click();
  await visibleText(page, '连接失败，请检查网络后刷新。');
  await context.setOffline(false);
  const recoveredRead = page.waitForResponse((response) => response.url() === feedback.api + '/api/feedback' && response.status() === 200);
  await page.getByRole('button', { name: '刷新' }).click();
  await recoveredRead;
  await visibleText(page, payload);
  await feedback.restart();
  const restartedRead = page.waitForResponse((response) => response.url() === feedback.api + '/api/feedback' && response.status() === 200);
  await page.getByRole('button', { name: '刷新' }).click();
  await restartedRead;
  await visibleText(page, payload);
  assert.equal((await (await fetch(feedback.api + '/api/feedback')).json()).length, 1);
  report.checks.push({ name: 'feedback-network-recovery-and-process-restart-persistence', passed: true });
  await page.screenshot({ path: path.join(root, 'feedback-desktop.png'), fullPage: true });

  const reservation = await context.newPage();
  const other = await context.newPage();
  await navigate(reservation, booking.origin);
  await navigate(other, booking.origin);
  await reservation.getByRole('button', { name: '预约', exact: true }).click();
  await visibleText(reservation, '预约成功');
  await reservation.getByRole('button', { name: '已保存', exact: true }).click();
  await visibleText(reservation, '已预约');
  const credential = reservation.getByRole('textbox', { name: '取消凭据' });
  await credential.click();
  await expect(credential).toBeFocused();
  await expect(credential).toHaveValue(/^[a-f0-9]{48}$/);
  const token = await credential.inputValue();
  assert.match(token, /^[a-f0-9]{48}$/);
  assert(!(await readFile(booking.state, 'utf8')).includes(token));
  await other.getByRole('button', { name: '预约', exact: true }).click();
  await visibleText(other, 'This slot is already reserved');
  await booking.restart();
  assert.equal((await (await fetch(booking.api + '/api/slots')).json())[0].reserved, true);
  report.checks.push({ name: 'booking-stale-client-conflict-opaque-token-and-restart', passed: true });
  await enterText(reservation, '取消凭据', 'wrong-token');
  await reservation.getByRole('button', { name: '取消我的预约', exact: true }).click();
  await visibleText(reservation, 'Booking not found');
  assert.equal((await (await fetch(booking.api + '/api/slots')).json())[0].reserved, true);
  await enterText(reservation, '取消凭据', token);
  await reservation.getByRole('button', { name: '取消我的预约', exact: true }).click();
  await visibleText(reservation, '可以预约');
  await booking.restart();
  assert.equal((await (await fetch(booking.api + '/api/slots')).json())[0].reserved, false);
  report.checks.push({ name: 'booking-cancellation-ownership-and-restart-persistence', passed: true });
  await reservation.screenshot({ path: path.join(root, 'booking-desktop.png'), fullPage: true });
  const mobile = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1 });
  await constrainNetwork(mobile);
  for (const [mode, app] of [['feedback', feedback], ['booking', booking]]) {
    const mobilePage = await mobile.newPage();
    await navigate(mobilePage, app.origin);
    await visibleText(mobilePage, mode === 'feedback' ? '真实浏览器反馈' : '可以预约');
    assert(await mobilePage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
    await mobilePage.screenshot({ path: path.join(root, mode + '-mobile-width.png'), fullPage: true });
  }
  report.checks.push({ name: 'chrome-mobile-viewport-both-applications', passed: true });
  assert.deepEqual(report.externalRequests, [], 'client requested an undeclared external resource');
  report.checks.push({ name: 'all-browser-resources-and-fonts-from-declared-local-origins', passed: true });
  assert(!report.checks.some((check) => check.kind === 'page-error'), JSON.stringify(report.checks));
  report.passed = true;
} catch (error) {
  report.passed = false;
  report.error = error.stack || String(error);
  if (browser) {
    let index = 0;
    for (const context of browser.contexts()) for (const page of context.pages()) {
      try {
        await page.screenshot({ path: path.join(root, 'failure-' + index + '.png'), fullPage: true });
        await writeFile(path.join(root, 'failure-' + index + '.html'), await page.content());
        await writeFile(path.join(root, 'failure-' + index + '-inputs.json'), JSON.stringify(await page.locator('input,textarea').evaluateAll((nodes) => nodes.map((node) => ({ label: node.getAttribute('aria-label'), value: node.value, active: node === document.activeElement }))), null, 2));
        index++;
      } catch { /* retain the original failure if diagnostics cannot be captured */ }
    }
  }
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  for (const close of resources.reverse()) {
    try { await close(); } catch (error) { report.cleanupError = String(error); process.exitCode = 1; }
  }
  report.finishedAt = new Date().toISOString();
  await writeFile(path.join(root, 'result.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({ root, ...report }, null, 2));
}
