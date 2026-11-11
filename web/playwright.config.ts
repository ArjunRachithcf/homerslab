import { defineConfig, devices } from '@playwright/test';

// The flow tests open first-time setup in a real browser, against the real binary (../bin/homerslab, from
// `go build -o bin/homerslab ./cmd/homerslab`): each test starts its own agent and manager in its own directory.
export default defineConfig({
  testDir: 'e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: { trace: 'retain-on-failure' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
    { name: 'phone', use: { ...devices['Pixel 7'] } },
  ],
});
