import { defineConfig } from '@playwright/test'

const port = 4179
export default defineConfig({
  testDir: './tests/e2e', fullyParallel: true,
  forbidOnly: !!process.env.CI, retries: 0, workers: 2,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: `http://127.0.0.1:${port}`, browserName: 'chromium',
    locale: 'zh-TW', colorScheme: 'light', trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'phone', use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true } },
    { name: 'tablet', use: { viewport: { width: 768, height: 1024 }, hasTouch: true } },
    { name: 'desktop', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'small-phone', use: { viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true } },
    { name: 'landscape', use: { viewport: { width: 844, height: 390 }, isMobile: true, hasTouch: true } },
  ],
  webServer: {
    command: `node scripts/serve-e2e.mjs ${port}`,
    url: `http://127.0.0.1:${port}/api/v1/health/live`,
    reuseExistingServer: false, timeout: 120_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 12_000 },
  },
})
