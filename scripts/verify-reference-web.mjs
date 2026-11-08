// A protected, single-package QA adapter for the known Flutter/Go blueprint.
// Chrome at mobile width does not establish physical device or store acceptance.
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createWriteStream } from 'node:fs';
import { lstat, mkdir, mkdtemp, readFile, realpath, readdir, writeFile } from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(path.join(scripts, '../web/package.json'));
const { chromium, expect } = require('@playwright/test');
const { values } = parseArgs({ options: { application: { type: 'string' }, 'api-url': { type: 'string' }, output: { type: 'string' }, 'feedback-priority': { type: 'boolean', default: false } } });
for (const key of ['application', 'api-url', 'output']) assert(values[key], '--' + key + ' is required');
assert(process.env.IS_ARTIFACT_PATH, 'IS_ARTIFACT_PATH is required');
const application = await realpath(values.application);
const output = path.resolve(values.output);
assert(!(await lstat(output).catch(() => null)), 'report output already exists');
const api = new URL(values['api-url']);
assert(api.protocol === 'http:' && api.hostname === '127.0.0.1' && api.port && api.pathname === '/' && !api.username && !api.password && !api.search && !api.hash, 'explicit loopback diagnostic API required');
const root = await realpath(await mkdtemp(path.join(os.tmpdir(), 'is-reference-qa-')));
const diagnostics = path.join(root, 'report');
await mkdir(diagnostics);
const report = { schemaVersion: 1, kind: 'actual-single-flutter-web-and-source-go-api', startedAt: new Date().toISOString(),
  physicalDevice: false, storePublished: false, checks: [], externalRequests: [], pageErrors: [], cleanup: [] };
const resources = [];
let browser;
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
function command(argv, options = {}) {
  return execFileSync('rtk', ['proxy', ...argv], { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024, ...options });
}
async function sourceSnapshot(directory, relative = '') {
  const files = {};
  for (const entry of (await readdir(path.join(directory, relative), { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
    if (['.git', '.dart_tool', 'build', '.cache'].includes(entry.name) || entry.name.startsWith('.intelligence-')) continue;
    if (!relative && ['quality', 'artifacts'].includes(entry.name)) continue;
    const name = path.posix.join(relative, entry.name);
    // Native jobs use the workspace as HOME; RTK writes its own telemetry here.
    // These are tool runtime files, never inputs from the reference blueprint.
    if (name === 'Library/Application Support/rtk') continue;
    assert(!entry.isSymbolicLink(), 'linked source is unsupported');
    if (entry.isDirectory()) Object.assign(files, await sourceSnapshot(directory, name));
    else if (entry.isFile()) files[name] = hash(await readFile(path.join(directory, name)));
    else throw new Error('special source entry');
  }
  return files;
}
async function stop(child) {
  if (!child) return;
  try { process.kill(-child.pid, 'SIGTERM'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { try { process.kill(-child.pid, 'SIGKILL'); } catch {} reject(new Error('API shutdown timed out')); }, 5000);
    child.once('exit', () => { clearTimeout(timer); resolve(); });
  });
}
async function openApplication(mode) {
  const site = path.join(root, 'site');
  report.archive = JSON.parse(command(['python3', path.join(scripts, 'reference-web-archive.py'), '--artifact', process.env.IS_ARTIFACT_PATH, '--destination', site]));
  report.sourceFiles = await sourceSnapshot(application);
  report.sourceDigest = hash(JSON.stringify(report.sourceFiles));
  report.backendFiles = Object.fromEntries(Object.entries(report.sourceFiles).filter(([name]) => name.startsWith('backend/')));
  report.backendSourceDigest = hash(JSON.stringify(report.backendFiles));
  const mime = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.json': 'application/json', '.wasm': 'application/wasm',
    '.woff2': 'font/woff2', '.ttf': 'font/ttf', '.otf': 'font/otf', '.png': 'image/png', '.svg': 'image/svg+xml' };
  const server = http.createServer(async (request, response) => {
    try {
      const name = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
      const file = path.resolve(site, '.' + (name === '/' ? '/index.html' : name));
      if (!file.startsWith(site + path.sep) || !(await lstat(file)).isFile()) throw new Error('not found');
      response.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
      response.end(await readFile(file));
    } catch { response.writeHead(404); response.end(); }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  resources.push(['static-server', async () => { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }]);
  const origin = 'http://127.0.0.1:' + server.address().port;
  report.origins = [origin, api.origin];
  const binary = path.join(root, 'api');
  command(['go', 'build', '-trimpath', '-o', binary, '.'], { cwd: path.join(application, 'backend') });
  report.backendBinarySHA256 = hash(await readFile(binary));
  const state = path.join(root, 'state.json');
  let child;
  async function start() {
    // Refuse occupied endpoints. Never terminate or use an unrelated service.
    const probe = http.createServer();
    await new Promise((resolve, reject) => { probe.once('error', reject); probe.listen(Number(api.port), '127.0.0.1', resolve); });
    await new Promise(resolve => probe.close(resolve));
    const log = createWriteStream(path.join(diagnostics, 'api.log'), { flags: 'a' });
    child = spawn('rtk', ['proxy', binary], { env: { ...process.env, APP_MODE: mode, APP_STATE_FILE: state,
      APP_LISTEN: '127.0.0.1:' + api.port, APP_WEB_ORIGIN: origin,
      APP_SLOTS: JSON.stringify([{ id: 'first', startsAt: '2026-12-01T09:00:00+08:00', reserved: false }]) }, detached: true, stdio: ['ignore', 'ignore', 'pipe'] });
    child.stderr.pipe(log);
    child.once('exit', () => log.end());
    child.once('error', error => { report.backendSpawnError = String(error); });
    await expect.poll(async () => {
      if (child.exitCode !== null || child.signalCode !== null) throw new Error('owned backend exited during startup');
      try { return (await fetch(api.origin + '/api/health', { signal: AbortSignal.timeout(2000) })).status; } catch { return 0; }
    }, { timeout: 15000 }).toBe(200);
  }
  resources.push(['go-backend', async () => { await stop(child); }]);
  await start();
  return { origin, api: api.origin, state, restart: async () => { await stop(child); await start(); } };
}
async function navigate(page, origin) {
  page.on('pageerror', error => report.pageErrors.push(error.message));
  await page.goto(origin);
  const accessibility = page.locator('flt-semantics-placeholder');
  await accessibility.waitFor({ state: 'attached', timeout: 60000 });
  await accessibility.dispatchEvent('click');
  await expect(page.getByRole('button', { name: '刷新' })).toBeVisible();
  assert.equal(await page.getByRole('button', { name: '应用更新', exact: true }).count(), 0);
}
async function visibleText(page, text) {
  await expect.poll(async () => page.locator('flt-semantics').evaluateAll((nodes, expected) =>
    nodes.some(node => (node.getAttribute('aria-label') || '').includes(expected) || (node.textContent || '').includes(expected)), text), { timeout: 15000 }).toBe(true);
}
async function enterText(page, name, text) {
  const input = page.getByRole('textbox', { name, exact: true });
  await input.click();
  await expect(input).toBeFocused();
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText(text);
  await expect(input).toHaveValue(text);
  await page.keyboard.press('Tab');
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}
const check = name => report.checks.push({ name, passed: true });
// Worker cancellation must also stop the owned backend process group.
for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, async () => {
  try { if (browser) await browser.close(); } catch {}
  for (const [, close] of [...resources].reverse()) { try { await close(); } catch {} }
  process.exit(signal === 'SIGTERM' ? 143 : 130);
});
async function feedbackChecks(page, context, app) {
  await page.getByRole('button', { name: '提交反馈', exact: true }).click();
  await visibleText(page, '请输入反馈标题。');
  assert.deepEqual(await (await fetch(app.api + '/api/feedback')).json(), []);
  const payload = '<script>window.__reference_xss = true</script>';
  await enterText(page, '反馈标题', '真实浏览器反馈');
  await enterText(page, '详细说明', payload);
  await page.route(app.api + '/api/feedback', async route => {
    if (route.request().method() === 'POST') await wait(400);
    await route.continue();
  });
  await page.getByRole('button', { name: '提交反馈', exact: true }).dblclick();
  await visibleText(page, payload);
  const records = await (await fetch(app.api + '/api/feedback')).json();
  assert.equal(records.length, 1);
  assert.equal(records[0].title, '真实浏览器反馈');
  assert.equal(records[0].content, payload);
  assert.equal(await page.evaluate(() => window.__reference_xss), undefined);
  check('feedback-validation-single-write-and-plain-text');
  await offlineRecovery(page, context, app, '/api/feedback', payload);
  await app.restart();
  await refresh(page, app, '/api/feedback');
  await visibleText(page, payload);
  assert.equal((await (await fetch(app.api + '/api/feedback')).json()).length, 1);
  check('feedback-backend-restart-persistence');
}
async function feedbackPriorityChecks(page, app, viewport) {
  const endpoint = app.api + '/api/feedback';
  if (viewport === 'desktop') {
    const defaultResponse = await fetch(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: 'Priority-default-API', content: 'Omitted priority remains normal' }) });
    assert.equal(defaultResponse.status, 201);
    assert.equal((await defaultResponse.json()).priority, 'normal', 'omitted priority must have a persisted normal value');
    const before = await (await fetch(endpoint)).json();
    for (const priority of ['administrator', '', 'NORMAL', ' urgent', 'urgent ']) {
      const invalid = await fetch(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: 'Priority-invalid-API', content: 'Reject invalid priority', priority }) });
      assert.equal(invalid.status, 422, 'explicit invalid priority must return 422: ' + JSON.stringify(priority));
      assert.deepEqual(await (await fetch(endpoint)).json(), before, 'rejected priority must not create feedback');
    }
    check('feedback-priority-default-and-invalid-api');
  }
  await page.getByRole('button', { name: /优先级/ }).or(page.getByRole('combobox', { name: /优先级/ })).first().click();
  await page.getByLabel('紧急', { exact: true }).last().click();
  const urgentTitle = viewport + '-urgent-feedback';
  await enterText(page, '反馈标题', urgentTitle);
  await enterText(page, '详细说明', 'Actual ' + viewport + ' priority submission');
  const beforeFailure = await (await fetch(endpoint)).json();
  await page.context().setOffline(true);
  try {
    await page.getByRole('button', { name: '提交反馈', exact: true }).click();
    await visibleText(page, '提交结果尚未确认，请先刷新核对，避免重复提交。');
    await expect(page.getByRole('textbox', { name: '反馈标题', exact: true })).toHaveValue(urgentTitle);
    await expect(page.getByRole('textbox', { name: '详细说明', exact: true })).toHaveValue('Actual ' + viewport + ' priority submission');
    assert.deepEqual(await (await fetch(endpoint)).json(), beforeFailure, 'offline submission wrote feedback');
  } finally {
    await page.context().setOffline(false);
  }
  await page.getByRole('button', { name: '提交反馈', exact: true }).click();
  await visibleText(page, urgentTitle);
  await visibleText(page, '紧急');
  let records = await (await fetch(endpoint)).json();
  assert.equal(records.filter(item => item.title === urgentTitle).length, 1);
  assert.equal(records.find(item => item.title === urgentTitle).priority, 'urgent');
  check('feedback-priority-' + viewport + '-failed-submit-retains-input-and-selection');
  const normalTitle = viewport + '-normal-feedback';
  await enterText(page, '反馈标题', normalTitle);
  await enterText(page, '详细说明', 'Selection resets after successful submission');
  await page.getByRole('button', { name: '提交反馈', exact: true }).click();
  await visibleText(page, normalTitle);
  records = await (await fetch(endpoint)).json();
  assert.equal(records.filter(item => item.title === normalTitle).length, 1);
  assert.equal(records.find(item => item.title === normalTitle).priority, 'normal');
  await app.restart();
  assert.deepEqual(await (await fetch(endpoint)).json(), records, 'priority and feedback must survive backend restart');
  await refresh(page, app, '/api/feedback');
  await visibleText(page, urgentTitle);
  check('feedback-priority-' + viewport + '-real-ui-reset-and-restart');
}
async function refresh(page, app, route) {
  const read = page.waitForResponse(response => response.url() === app.api + route && response.status() === 200);
  await page.getByRole('button', { name: '刷新' }).click();
  await read;
}
async function offlineRecovery(page, context, app, route, text) {
  await context.setOffline(true);
  await page.getByRole('button', { name: '刷新' }).click();
  await visibleText(page, '连接失败，请检查网络后刷新。');
  await context.setOffline(false);
  await refresh(page, app, route);
  await visibleText(page, text);
  check('network-offline-and-recovery');
}
async function bookingChecks(page, context, app) {
  const other = await context.newPage();
  await navigate(other, app.origin);
  await page.getByRole('button', { name: '预约', exact: true }).click();
  await visibleText(page, '预约成功');
  await page.getByRole('button', { name: '已保存', exact: true }).click();
  await visibleText(page, '已预约');
  const credential = page.getByRole('textbox', { name: '取消凭据' });
  await credential.click();
  await expect(credential).toHaveValue(/^[a-f0-9]{48}$/);
  const token = await credential.inputValue();
  assert.match(token, /^[a-f0-9]{48}$/);
  assert(!(await readFile(app.state, 'utf8')).includes(token));
  await other.getByRole('button', { name: '预约', exact: true }).click();
  await visibleText(other, 'This slot is already reserved');
  await app.restart();
  assert.equal((await (await fetch(app.api + '/api/slots')).json())[0].reserved, true);
  check('booking-stale-client-conflict-opaque-token-and-restart');
  await enterText(page, '取消凭据', 'wrong-token');
  await page.getByRole('button', { name: '取消我的预约', exact: true }).click();
  await visibleText(page, 'Booking not found');
  assert.equal((await (await fetch(app.api + '/api/slots')).json())[0].reserved, true);
  await enterText(page, '取消凭据', token);
  await page.getByRole('button', { name: '取消我的预约', exact: true }).click();
  await visibleText(page, '可以预约');
  await app.restart();
  assert.equal((await (await fetch(app.api + '/api/slots')).json())[0].reserved, false);
  check('booking-cancellation-ownership-and-restart-persistence');
  await offlineRecovery(page, context, app, '/api/slots', '可以预约');
}

try {
  const metadata = JSON.parse(await readFile(path.join(application, 'application.json'), 'utf8'));
  assert(['feedback', 'booking'].includes(metadata.mode), 'known reference mode required');
  assert(!values['feedback-priority'] || metadata.mode === 'feedback', 'priority verification requires a feedback application');
  report.feedbackPriorityRequired = values['feedback-priority'];
  report.application = metadata;
  const app = await openApplication(metadata.mode);
  browser = await chromium.launch({ executablePath: process.env.IS_BROWSER_EXECUTABLE || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  report.browser = { name: 'Chrome', version: browser.version() };
  async function contextFor(options) {
    const context = await browser.newContext({ serviceWorkers: 'block', ...options });
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (report.origins.includes(url.origin)) return route.continue();
      report.externalRequests.push(url.href);
      await route.abort();
    });
    return context;
  }
  const context = await contextFor({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  await navigate(page, app.origin);
  if (metadata.mode === 'feedback') await feedbackChecks(page, context, app);
  else await bookingChecks(page, context, app);
  if (values['feedback-priority']) await feedbackPriorityChecks(page, app, 'desktop');
  await page.screenshot({ path: path.join(diagnostics, 'desktop.png'), fullPage: true });
  const mobile = await contextFor({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1 });
  const mobilePage = await mobile.newPage();
  await navigate(mobilePage, app.origin);
  await visibleText(mobilePage, metadata.mode === 'feedback' ? values['feedback-priority'] ? 'desktop-normal-feedback' : '真实浏览器反馈' : '可以预约');
  if (values['feedback-priority']) await feedbackPriorityChecks(mobilePage, app, 'mobile');
  assert(await mobilePage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  await mobilePage.screenshot({ path: path.join(diagnostics, 'mobile.png'), fullPage: true });
  check('chrome-390px-layout-and-no-desktop-updater');
  assert.deepEqual(report.externalRequests, [], 'undeclared client network request');
  assert.deepEqual(report.pageErrors, [], 'browser runtime error');
  check('all-browser-resources-and-fonts-use-declared-local-origins');
  report.passed = true;
} catch (error) {
  report.passed = false;
  report.error = error.stack || String(error);
  if (browser) {
    let index = 0;
    for (const context of browser.contexts()) for (const page of context.pages()) {
      try { await page.screenshot({ path: path.join(diagnostics, 'failure-' + index++ + '.png'), fullPage: true }); } catch { /* keep original error */ }
    }
  }
  process.exitCode = 1;
} finally {
  if (browser) {
    try { await browser.close(); report.cleanup.push({ resource: 'browser', stopped: true }); }
    catch (error) { report.cleanup.push({ resource: 'browser', error: String(error) }); process.exitCode = 1; report.passed = false; }
  }
  for (const [name, close] of resources.reverse()) {
    try { await close(); report.cleanup.push({ resource: name, stopped: true }); }
    catch (error) { report.cleanup.push({ resource: name, error: String(error) }); process.exitCode = 1; report.passed = false; }
  }
  report.finishedAt = new Date().toISOString();
  await writeFile(path.join(diagnostics, 'result.json'), JSON.stringify(report, null, 2) + '\n');
  await mkdir(path.dirname(output), { recursive: true });
  command(['python3', '-c', 'import os,pathlib,sys,tarfile,tempfile\nr=pathlib.Path(sys.argv[1]);o=pathlib.Path(sys.argv[2]);fd,n=tempfile.mkstemp(dir=o.parent,prefix=".quality-report-");os.close(fd)\ntry:\n with tarfile.open(n,"w:gz") as t:\n  for f in sorted(r.iterdir()):\n   if f.is_symlink() or not f.is_file(): raise ValueError("unsafe report entry")\n   t.add(f,arcname=f.name,recursive=False)\n os.link(n,o)\nfinally: os.unlink(n)', diagnostics, output]);
  console.log(JSON.stringify({ passed: report.passed, mode: report.application?.mode, artifactSHA256: report.archive?.sha256, report: output, diagnostics, checks: report.checks, error: report.error }));
}
