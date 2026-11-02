const { test, expect } = require('@playwright/test');

const releaseBase = 'https://github.com/packetloss404/packetcode/releases/latest/download/';
const downloads = [
  'packetcode-darwin-arm64.tar.gz',
  'packetcode-darwin-amd64.tar.gz',
  'packetcode-linux-amd64.tar.gz',
  'packetcode-linux-arm64.tar.gz',
  'packetcode-windows-amd64.zip',
  'packetcode-windows-arm64.zip',
].map(name => releaseBase + name);

test.beforeEach(async ({ page }) => {
  const errors = [];
  test.info().siteErrors = errors;
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'error') errors.push(message.text());
  });
  await page.goto('./');
});

test.afterEach(async () => {
  expect(test.info().siteErrors).toEqual([]);
});

test('product page loads real screenshots and all six native download targets', async ({ page }) => {
  await expect(page).toHaveTitle(/packetcode/i);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expect(page.locator('main')).toHaveCount(1);
  const targets = await page.locator('a[href*="/releases/latest/download/packetcode-"]')
    .evaluateAll(links => [...new Set(links.map(link => link.href))].sort());
  expect(targets).toEqual([...downloads].sort());

  const images = page.locator('img[src*="screenshots/"]');
  expect(await images.count()).toBeGreaterThanOrEqual(3);
  for (const image of await images.all()) {
    await expect(image).toHaveAttribute('alt', /\S/);
    // Inactive tab images can be hidden or lazy, so load each through its URL
    // as well as checking the initially visible screenshot in the browser.
    const response = await page.request.get(await image.getAttribute('src'));
    expect(response.ok(), response.url()).toBe(true);
  }
  const visibleImage = page.locator('[role="tabpanel"]:visible img').first();
  await expect(visibleImage).toBeVisible();
  await expect.poll(() => visibleImage.evaluate(image => image.complete && image.naturalWidth > 0)).toBe(true);
});

test('installation tabs show the matching platform command and support keyboard navigation', async ({ page }) => {
  const tabs = page.getByRole('tablist', { name: 'Installation platform' });
  const unix = tabs.getByRole('tab', { name: 'macOS / Linux', exact: true });
  const windows = tabs.getByRole('tab', { name: 'Windows', exact: true });
  await expect(unix).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#install-command-unix')).toContainText('install.sh | bash');
  await windows.click();
  await expect(windows).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#install-panel-windows')).toBeVisible();
  await expect(page.locator('#install-command-windows')).toContainText('install.ps1');
  await expect(page.locator('#install-panel-unix')).toBeHidden();
  await windows.press('ArrowLeft');
  await expect(unix).toBeFocused();
  await expect(unix).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#install-panel-unix')).toBeVisible();
});

test('installation copy buttons put each complete command on the clipboard', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.bringToFront();
  for (const platform of ['unix', 'windows']) {
    await page.locator(`[data-install-tab="${platform}"]`).click();
    const target = `install-command-${platform}`;
    const expected = (await page.locator(`#${target}`).textContent()).trim();
    await page.locator(`button[data-copy-target="${target}"]`).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
    await expect(page.getByRole('status')).toContainText(/copied/i);
  }
});

test('workspace tabs switch real views and keep selection and focus synchronized', async ({ page }) => {
  const tabs = page.getByRole('tablist', { name: 'Explore the workspace' });
  for (const [label, state] of [['Conversation', 'chat'], ['Approvals', 'approval'], ['Background agents', 'agents']]) {
    const tab = tabs.getByRole('tab', { name: label, exact: true });
    await tab.click();
    await expect(tab).toHaveAttribute('aria-selected', 'true');
    const panel = page.locator(`#showcase-panel-${state}`);
    await expect(panel).toBeVisible();
    await expect(panel.locator('img')).toHaveAttribute('src', new RegExp(`packetcode-${state}[.]jpg$`));
    expect(await tabs.locator('[aria-selected="true"]').count()).toBe(1);
  }
  await tabs.getByRole('tab', { name: 'Background agents', exact: true }).press('Home');
  await expect(tabs.getByRole('tab', { name: 'Conversation', exact: true })).toBeFocused();
  await expect(page.locator('#showcase-panel-chat')).toBeVisible();
});

test('navigation remains usable on mobile and leads to the getting-started guide', async ({ page, isMobile }) => {
  const toggle = page.locator('[data-menu-toggle]');
  const nav = page.locator('#site-nav');
  if (isMobile) {
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(nav).toBeVisible();
  } else {
    await expect(nav).toBeVisible();
  }
  await nav.locator('a[href="guide/"]').click();
  await expect(page).toHaveURL(/\/packetcode\/guide\/$/);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expect(page.locator('body')).toContainText('packetcode --version');
  const manual = page.locator('a[href="../docs/packetcode-manual.html"]').first();
  await expect(manual).toBeVisible();
  await manual.click();
  await expect(page).toHaveURL(/\/packetcode\/docs\/packetcode-manual\.html$/);
  await expect(page.getByRole('heading', { name: 'packetcode manual', exact: true })).toBeVisible();
});

test('guide commands copy correctly and contents links point to real sections', async ({ page, context }) => {
  await page.goto('guide/');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.bringToFront();
  const button = page.locator('button[data-copy-target]').first();
  const id = await button.getAttribute('data-copy-target');
  expect(id).toBeTruthy();
  const expected = (await page.locator(`[id="${id}"]`).textContent()).trim();
  await button.click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
  const sections = await page.locator('.guide-nav a[href^="#"]').evaluateAll(links => links.map(link => link.hash));
  expect(sections.length).toBeGreaterThanOrEqual(4);
  for (const section of sections) await expect(page.locator(section)).toHaveCount(1);
});

test('page and guide have no broken internal links, missing assets, or root-relative project paths', async ({ page, request, baseURL }) => {
  const root = new URL(baseURL);
  const checked = new Set();
  for (const path of ['./', 'guide/']) {
    await page.goto(path);
    const resources = await page.evaluate(() => {
      const links = [...document.querySelectorAll('a[href]')].map(link => ({
        raw: link.getAttribute('href'),
        url: link.href,
        sectionExists: !link.hash || Boolean(document.getElementById(decodeURIComponent(link.hash.slice(1)))),
      }));
      const assets = [...document.querySelectorAll('script[src], link[rel="stylesheet"], img[src]')]
        .map(element => ({ raw: element.getAttribute('src') || element.getAttribute('href'), url: element.src || element.href }));
      return { links, assets };
    });
    for (const link of resources.links) {
      if (link.raw.startsWith('#')) expect(link.sectionExists, link.raw).toBe(true);
      expect(link.raw, 'Links should have a meaningful destination.').not.toBe('#');
    }
    for (const resource of [...resources.links, ...resources.assets]) {
      const url = new URL(resource.url);
      if (url.origin !== root.origin) continue;
      expect(url.pathname.startsWith(root.pathname), resource.raw).toBe(true);
      url.hash = '';
      if (checked.has(url.href)) continue;
      checked.add(url.href);
      const response = await request.get(url.href);
      expect(response.ok(), resource.raw).toBe(true);
    }
  }
  expect(checked.size).toBeGreaterThan(6);
});

test('small screens contain long commands and tables without horizontal page scrolling', async ({ page }) => {
  for (const width of [320, 375, 768, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    for (const path of ['./', 'guide/']) {
      await page.goto(path);
      const dimensions = await page.evaluate(() => ({
        viewport: document.documentElement.clientWidth,
        page: document.documentElement.scrollWidth,
      }));
      expect(dimensions.page, `${path} at ${width}px`).toBeLessThanOrEqual(dimensions.viewport + 1);
    }
  }
});
