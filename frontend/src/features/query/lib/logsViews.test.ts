import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { LOGS_SEARCH_VIEW, readStoredLogsView, storeLogsView } from './logsViews';

describe('stored logs view', () => {
  let stored: Map<string, string>;

  beforeEach(() => {
    stored = new Map();
    vi.stubGlobal('sessionStorage', {
      getItem: (key: string) => stored.get(key) ?? null,
      setItem: (key: string, value: string) => stored.set(key, value),
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it('reopens the view that was open before a reload', () => {
    storeLogsView('web');

    expect(readStoredLogsView()).toBe('web');
  });

  it('opens Search on a first visit', () => {
    expect(readStoredLogsView()).toBe(LOGS_SEARCH_VIEW);
  });

  it('opens Search when the stored view no longer exists', () => {
    stored.set('obzly-logs-view', 'retired-view');

    expect(readStoredLogsView()).toBe(LOGS_SEARCH_VIEW);
  });

  it('opens Search when storage is blocked', () => {
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new Error('blocked');
      },
    });

    expect(readStoredLogsView()).toBe(LOGS_SEARCH_VIEW);
  });
});
