import { describe, expect, it } from 'vitest';

import { escapeHtml } from './escapeHtml';

describe('escapeHtml', () => {
  it('neutralizes markup from ingested data', () => {
    expect(escapeHtml(`<img src=x onerror="alert('1')">&`)).toBe(
      '&lt;img src=x onerror=&quot;alert(&#39;1&#39;)&quot;&gt;&amp;',
    );
  });
});
