import assert from 'node:assert/strict';
import { access, readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const docsRoot = fileURLToPath(new URL('..', import.meta.url));
const dist = path.join(docsRoot, 'dist');
const base = '/workitem';

const routes = [
  '',
  'getting-started/installation',
  'getting-started/quick-start',
  'concepts/work-items',
  'concepts/lifecycle',
  'concepts/workspaces',
  'concepts/agents',
  'concepts/attention',
  'guides/everyday-workflow',
  'guides/delegation',
  'guides/review-and-follow-up',
  'guides/tmux',
  'guides/merging',
  'reference/commands',
  'reference/configuration',
  'reference/data-and-diagnostics',
  'reference/troubleshooting',
  'philosophy/philosophy',
];

async function htmlFiles(directory = dist) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nested = await Promise.all(entries.map((entry) => {
    const target = path.join(directory, entry.name);
    return entry.isDirectory() ? htmlFiles(target) : [target];
  }));
  return nested.flat().filter((file) => file.endsWith('.html'));
}

test('all documented routes are emitted', async () => {
  for (const route of routes) {
    await access(path.join(dist, route, 'index.html'));
  }
});

test('public metadata is emitted', async () => {
  await access(path.join(dist, 'robots.txt'));
  await access(path.join(dist, 'llms.txt'));
  await access(path.join(dist, 'sitemap-index.xml'));
});

test('canonical URLs and local links include the GitHub Pages base', async () => {
  for (const file of await htmlFiles()) {
    const html = await readFile(file, 'utf8');
    assert.match(html, /https:\/\/regb\.github\.io\/workitem\//, file);

    for (const match of html.matchAll(/(?:href|src)="(\/[^"#?]*)/g)) {
      const reference = match[1];
      assert.ok(
        reference === base || reference.startsWith(`${base}/`),
        `${file} contains an unbased local reference: ${reference}`,
      );
    }
  }
});

test('internal page links and fragments resolve', async () => {
  const files = await htmlFiles();

  for (const file of files) {
    const html = await readFile(file, 'utf8');
    const relative = path.relative(dist, file).replaceAll(path.sep, '/');
    const route = relative === 'index.html'
      ? `${base}/`
      : `${base}/${relative.replace(/index\.html$/, '')}`;

    for (const match of html.matchAll(/href="([^"?]+)"/g)) {
      const href = match[1].replaceAll('&amp;', '&');
      const url = new URL(href, `https://regb.github.io${route}`);
      if (url.origin !== 'https://regb.github.io' || !url.pathname.startsWith(`${base}/`)) continue;

      const emittedPath = decodeURIComponent(url.pathname.slice(base.length + 1));
      const target = emittedPath === '404/'
        ? path.join(dist, '404.html')
        : emittedPath.endsWith('/')
          ? path.join(dist, emittedPath, 'index.html')
          : path.join(dist, emittedPath);
      await access(target);

      if (url.hash && target.endsWith('.html')) {
        const targetHtml = await readFile(target, 'utf8');
        const id = decodeURIComponent(url.hash.slice(1));
        assert.ok(
          [...targetHtml.matchAll(/\bid="([^"]+)"/g)].some((candidate) => candidate[1] === id),
          `${file} links to missing fragment ${url.pathname}${url.hash}`,
        );
      }
    }
  }
});
