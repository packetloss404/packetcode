const { test, expect } = require('@playwright/test');
const { resolve } = require('node:path');
const { pathToFileURL } = require('node:url');
const { access } = require('node:fs/promises');

const manualURL = pathToFileURL(resolve(__dirname, '../../docs/packetcode-manual.html')).href;

test.beforeEach(async ({ context, page }) => {
  // The advertised offline manual must render without fetching network assets.
  await context.setOffline(true);
  const errors = [];
  test.info().onPageErrors = errors;
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error') errors.push(message.text());
  });
  await page.goto(manualURL);
  await expect(page.getByRole('heading', { name: 'packetcode manual', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Print cheat sheet' })).toBeVisible();
});

test.afterEach(async () => {
  if (test.info().onPageErrors) expect(test.info().onPageErrors).toEqual([]);
});

test('offline content has no network dependencies, duplicate ids, or broken local links', async ({ page }) => {
  const documentState = await page.evaluate(() => {
    const ids = Array.from(document.querySelectorAll('[id]'), element => element.id);
    return {
      duplicates: ids.filter((id, index) => ids.indexOf(id) !== index),
      links: Array.from(document.querySelectorAll('a[href]'), link => ({
        href: link.getAttribute('href'),
        resolved: link.href,
        exists: !link.hash || Boolean(document.getElementById(decodeURIComponent(link.hash.slice(1)))),
      })),
      networkAssets: performance.getEntriesByType('resource').filter(entry => /^https?:/.test(entry.name)).map(entry => entry.name),
    };
  });
  expect(documentState.duplicates).toEqual([]);
  expect(documentState.networkAssets).toEqual([]);
  expect(documentState.links.length).toBeGreaterThan(20);
  for (const link of documentState.links) {
    if (link.href.startsWith('#')) expect(link.exists, link.href).toBe(true);
    else if (link.resolved.startsWith('file:')) await access(new URL(link.resolved));
  }
});

test('hero actions and every contents entry navigate to their section', async ({ page }) => {
  test.setTimeout(60_000);
  await page.getByRole('link', { name: 'Start here', exact: true }).click();
  await expect(page).toHaveURL(/#quick-start$/);
  await expect(page.locator('#quick-start-title')).toBeInViewport();
  await page.goto(manualURL);
  await page.getByRole('link', { name: 'Jump to cheat sheet' }).click();
  await expect(page).toHaveURL(/#quick-reference$/);
  await expect(page.locator('#quick-reference-title')).toBeInViewport();

  const contents = page.getByRole('navigation', { name: 'Table of contents' });
  const links = await contents.getByRole('link').evaluateAll(elements => elements.map(element => ({
    text: element.textContent,
    href: element.getAttribute('href'),
  })));
  for (const link of links) {
    await contents.getByRole('link', { name: link.text, exact: true }).click();
    await expect(page).toHaveURL(`${manualURL}${link.href}`);
    await expect(page.locator(link.href).getByRole('heading').first()).toBeInViewport();
  }
});

test('keyboard users can skip to content and toggle the contents', async ({ page }) => {
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to manual' })).toBeFocused();
  await expect(page.getByRole('link', { name: 'Skip to manual' })).toBeInViewport();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#main-content$/);
  await expect(page.getByRole('heading', { name: 'At a glance' })).toBeInViewport();
  const contents = page.getByRole('navigation', { name: 'Table of contents' });
  await contents.getByText('Contents', { exact: true }).click();
  await expect(contents.getByRole('link').first()).toBeHidden();
  await contents.getByText('Contents', { exact: true }).click();
  await expect(contents.getByRole('link').first()).toBeVisible();
});

test('screen layouts keep long commands and tables within the viewport', async ({ page }) => {
  for (const width of [320, 375, 768, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    const overflow = await page.evaluate(() => ({
      width: window.innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      containers: Array.from(document.querySelectorAll('pre, .table-wrap')).filter(element => {
        const bounds = element.getBoundingClientRect();
        return bounds.left < -1 || bounds.right > window.innerWidth + 1;
      }).map(element => element.textContent.slice(0, 80)),
    }));
    expect(overflow.documentWidth, `document overflow at ${width}px`).toBeLessThanOrEqual(overflow.width + 1);
    expect(overflow.containers, `container overflow at ${width}px`).toEqual([]);
  }
});

test('print button selects only cheat sheet and afterprint restores full manual', async ({ page }) => {
  // Browser automation cannot operate the native print dialog. Capture the mode
  // passed to window.print, then exercise real Chromium print CSS and PDF output.
  await page.evaluate(() => {
    window.print = () => { window.printRequested = document.body.classList.contains('print-cheat'); };
  });
  await page.getByRole('button', { name: 'Print cheat sheet' }).click();
  expect(await page.evaluate(() => window.printRequested)).toBe(true);
  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('#quick-reference')).toBeVisible();
  await expect(page.locator('#quick-start')).toBeHidden();
  await expect(page.locator('.quick-banner')).toBeHidden();
  const pdf = await page.pdf({ format: 'A4' });
  expect(pdf.subarray(0, 5).toString()).toBe('%PDF-');
  await page.evaluate(() => window.dispatchEvent(new Event('afterprint')));
  await expect(page.locator('body')).not.toHaveClass(/print-cheat/);
  await expect(page.locator('#quick-start')).toBeVisible();
  await page.emulateMedia({ media: 'screen' });
  await expect(page.getByRole('button', { name: 'Print cheat sheet' })).toBeVisible();
});

test('full print includes all sections and wraps long command examples', async ({ page }) => {
  await page.setViewportSize({ width: 700, height: 900 });
  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('.hero')).toBeVisible();
  await expect(page.locator('aside')).toBeHidden();
  const sections = page.locator('main > section');
  for (const section of await sections.all()) await expect(section).toBeVisible();
  const clipped = await page.locator('pre').evaluateAll(elements => elements.filter(element => element.scrollWidth > element.clientWidth + 1).map(element => element.textContent.slice(0, 80)));
  expect(clipped, 'printed command examples must wrap instead of being clipped').toEqual([]);
  const pdf = await page.pdf({ format: 'A4' });
  expect(pdf.subarray(0, 5).toString()).toBe('%PDF-');
});
