import type { AssetQuery } from '../model/Inventory';

/** The query string of an asset list request; lists are comma-separated, empty values left out. */
export const assetSearchParams = (query: AssetQuery): URLSearchParams => {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query) as [string, unknown][]) {
    if (Array.isArray(value)) {
      if (value.length > 0) params.set(name, value.join(','));
    } else if (typeof value === 'boolean') {
      if (value) params.set(name, 'true');
    } else if (value !== undefined && value !== '') {
      params.set(name, String(value));
    }
  }
  return params;
};
