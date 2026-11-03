// Render recorded output; every pixel of the application comes from its PTY.
import { chromium } from '@playwright/test';
import { readFile, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const output = resolve(process.argv[2]);
const require = createRequire(import.meta.url);
const xterm = dirname(dirname(require.resolve('@xterm/xterm')));
const states = ['chat', 'approval', 'agents', 'models'];
const files = new Map([
  ['/', join(root, 'tools/screenshots/terminal.html')],
  ['/vendor/xterm.js', join(xterm, 'lib/xterm.js')],
  ['/vendor/xterm.css', join(xterm, 'css/xterm.css')],
  ...states.map(state => [`/${state}.ansi`, join(output, `${state}.ansi`)]),
]);
const server = createServer(async (request, response) => {
  const path = new URL(request.url, 'http://localhost').pathname;
  if (path === '/favicon.ico') { response.writeHead(204).end(); return; }
  const file = files.get(path);
  if (!file) { response.writeHead(404).end(); return; }
  try {
    const data = await readFile(file);
    response.writeHead(200, { 'Content-Type': path.endsWith('.js') ? 'text/javascript' : path.endsWith('.css') ? 'text/css' : path === '/' ? 'text/html' : 'application/octet-stream' });
    response.end(data);
  } catch { response.writeHead(500).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
try {
  browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: 1, reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const result = {};
  for (const state of states) {
    await page.goto(`http://127.0.0.1:${server.address().port}/?state=${state}`);
    await page.waitForFunction(() => document.body.dataset.captureReady === 'true');
    const rendered = await page.evaluate(() => window.captureResult);
    if (!rendered.text.includes('Local demo') && state !== 'agents') throw new Error(`${state}: local demo identity missing`);
    if (errors.length) throw new Error(errors.join('\n'));
    await page.screenshot({ path: join(output, `packetcode-${state}.jpg`), type: 'jpeg', quality: 92 });
    result[state] = rendered.crop;
  }
  await writeFile(join(output, 'crops.json'), JSON.stringify(result, null, 2) + '\n');
} finally {
  await browser?.close();
  await new Promise(resolve => server.close(resolve));
}
