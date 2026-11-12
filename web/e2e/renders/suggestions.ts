import { Page } from '@playwright/test';

/**
 * Every suggestion request answers 429 with `Retry-After`: the notice of R-UI-097 is a state of the field, which the seed's server
 * reaches only under load. The other states are the server's own answers (R-API-060 to R-API-063).
 */
export async function routeTooMany(page: Page, seconds: number): Promise<void> {
  await page.route('**/api/v1/search/suggestions?**', (route) =>
    route.fulfill({ status: 429, headers: { 'Retry-After': String(seconds) }, json: { status: 429, code: 'rate_limited' } }),
  );
}
