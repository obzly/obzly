import { defineConfig } from '@playwright/test';

// Smoke tests against a running Obzly: OBZLY_URL, OBZLY_EMAIL and
// OBZLY_PASSWORD (a non-admin account; admins need MFA). Skipped when unset.
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  retries: 0,
  reporter: 'list',
  use: {
    baseURL: process.env.OBZLY_URL,
    locale: 'en-US',
    timezoneId: 'UTC',
    viewport: { width: 1600, height: 1000 },
    trace: 'retain-on-failure',
  },
});
