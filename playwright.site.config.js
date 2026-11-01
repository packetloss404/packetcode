const { defineConfig } = require('@playwright/test');

const port = Number(process.env.SITE_PORT || 4177);
const baseURL = `http://127.0.0.1:${port}/packetcode/`;

module.exports = defineConfig({
  testDir: './tests/site',
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: 2,
  reporter: [
    [process.env.CI ? 'github' : 'list'],
    ['html', { open: 'never', outputFolder: 'playwright-report/site' }],
  ],
  outputDir: 'test-results/site',
  use: {
    baseURL,
    browserName: 'chromium',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    reducedMotion: 'reduce',
  },
  webServer: {
    command: 'node scripts/build-site.mjs --serve --skip-build',
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 15_000,
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 1000 } } },
    { name: 'mobile', use: { viewport: { width: 375, height: 812 }, isMobile: true } },
  ],
});
