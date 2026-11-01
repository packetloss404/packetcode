import { copyFile, mkdir, readdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { dirname, extname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = join(root, 'dist', 'site');
const prefix = '/packetcode/';
const argumentsSet = new Set(process.argv.slice(2));

async function copyDirectory(source, destination) {
  await mkdir(destination, { recursive: true });
  for (const entry of await readdir(source, { withFileTypes: true })) {
    if (entry.name === '.DS_Store') continue;
    const input = join(source, entry.name);
    const target = join(destination, entry.name);
    if (entry.isSymbolicLink()) throw new Error(`Static site contains a symbolic link: ${input}`);
    if (entry.isDirectory()) await copyDirectory(input, target);
    else if (entry.isFile()) await copyFile(input, target);
  }
}

async function build() {
  // Delete only this project's generated website, never the release binaries
  // or other artifacts that may already be in dist/.
  await rm(output, { recursive: true, force: true });
  await copyDirectory(join(root, 'site'), output);
  await copyDirectory(join(root, 'docs'), join(output, 'docs'));
  await copyDirectory(join(root, 'docs', 'images'), join(output, 'assets', 'screenshots'));
  // The offline manual and reference Markdown retain their repository-relative
  // links. Publish their root document targets beside docs/ as well.
  for (const file of ['README.md', 'CHANGELOG.md', 'LICENSE', 'HANDOFF.md', 'BACKLOG.md', 'PACKETCOMPUTERS.md', 'workbook.md']) {
    await copyFile(join(root, file), join(output, file));
  }
  await writeFile(join(output, '.nojekyll'), '');
  await stat(join(output, 'index.html'));
  await stat(join(output, 'guide', 'index.html'));
  console.log(`Built PacketCode website: ${relative(root, output)}`);
}

const contentTypes = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.md': 'text/plain; charset=utf-8',
  '.png': 'image/png',
  '.svg': 'image/svg+xml',
  '.txt': 'text/plain; charset=utf-8',
  '.ttf': 'font/ttf',
  '.webp': 'image/webp',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.xml': 'application/xml; charset=utf-8',
};

async function serve() {
  await stat(join(output, 'index.html'));
  const port = Number(process.env.SITE_PORT || 4177);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('SITE_PORT must be a valid port number.');
  const server = createServer(async (request, response) => {
    if (!['GET', 'HEAD'].includes(request.method)) {
      response.writeHead(405, { Allow: 'GET, HEAD' }).end();
      return;
    }
    try {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname === '/' || url.pathname === prefix.slice(0, -1)) {
        response.writeHead(302, { Location: prefix }).end();
        return;
      }
      if (!url.pathname.startsWith(prefix)) {
        response.writeHead(404).end('Not found');
        return;
      }
      let target = resolve(output, decodeURIComponent(url.pathname.slice(prefix.length)));
      if (target !== output && !target.startsWith(output + sep)) {
        response.writeHead(404).end('Not found');
        return;
      }
      const info = await stat(target);
      if (info.isDirectory()) {
        if (!url.pathname.endsWith('/')) {
          response.writeHead(302, { Location: url.pathname + '/' + url.search }).end();
          return;
        }
        target = join(target, 'index.html');
      }
      const body = await readFile(target);
      response.writeHead(200, {
        'Content-Type': contentTypes[extname(target)] || 'application/octet-stream',
        'Content-Length': body.length,
        'Cache-Control': 'no-store',
      });
      response.end(request.method === 'HEAD' ? undefined : body);
    } catch (error) {
      if (error.code === 'ENOENT' || error.code === 'ENOTDIR') {
        try {
          const body = await readFile(join(output, '404.html'));
          response.writeHead(404, { 'Content-Type': 'text/html; charset=utf-8' });
          response.end(request.method === 'HEAD' ? undefined : body);
        } catch {
          response.writeHead(404).end('Not found');
        }
      } else {
        response.writeHead(400).end('Bad request');
      }
    }
  });
  await new Promise((fulfill, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', fulfill);
  });
  console.log(`PacketCode preview: http://127.0.0.1:${port}${prefix}`);
}

try {
  if (!argumentsSet.has('--skip-build')) await build();
  if (argumentsSet.has('--serve')) await serve();
} catch (error) {
  console.error(`Website: ${error.message}`);
  process.exitCode = 1;
}
