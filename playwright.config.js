const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests/browser',
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: 2,
  reporter: [
    [process.env.CI ? 'github' : 'list'],
    ['html', { open: 'never' }],
  ],
  use: {
    browserName: 'chromium',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'mobile', use: { viewport: { width: 375, height: 812 }, isMobile: true } },
  ],
});
