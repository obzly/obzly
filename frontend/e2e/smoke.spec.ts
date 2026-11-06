import { type Page, expect, test } from '@playwright/test';

const email = process.env.OBZLY_EMAIL;
const password = process.env.OBZLY_PASSWORD;

test.skip(
  !process.env.OBZLY_URL || !email || !password,
  'set OBZLY_URL, OBZLY_EMAIL, OBZLY_PASSWORD',
);

// Collected per test; any uncaught page error fails it.
let pageErrors: string[] = [];

test.beforeEach(async ({ page }) => {
  pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.goto('/');
  await page.getByPlaceholder('your@email.com').fill(email!);
  await page.getByPlaceholder('********').fill(password!);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('button', { name: 'Run' })).toBeVisible({ timeout: 45_000 });
});

test.afterEach(() => {
  expect(pageErrors).toEqual([]);
});

const openTab = async (page: Page, name: string) => {
  await page.locator(`img[alt="${name}"]`).first().click();
};

// The app has no error boundary: a view that throws blanks the page and
// raises a page error, so "still rendered" plus "no page errors" is the check.
const expectStillRendered = async (page: Page) => {
  await expect(page.locator('img[alt="Search"]').first()).toBeVisible();
};

test('search runs', async ({ page }) => {
  await page.getByRole('button', { name: 'Run' }).click();
  await page.waitForTimeout(2000);
  await expectStillRendered(page);
});

test('the main tabs render', async ({ page }) => {
  for (const tab of ['Traces', 'Dashboards', 'Alerts', 'Organizations']) {
    await openTab(page, tab);
    await page.waitForTimeout(1500);
    await expectStillRendered(page);
  }
});
