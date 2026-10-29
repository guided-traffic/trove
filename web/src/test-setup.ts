import { vi } from 'vitest';

// The accessibility check renders and audits a whole screen in jsdom (R-UI-044). For the large
// editors that takes several seconds on a shared CI runner, above vitest's default of 5 s, which is
// a guard against hanging tests and not a performance budget.
vi.setConfig({ testTimeout: 30_000 });
