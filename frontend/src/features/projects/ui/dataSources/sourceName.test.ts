import { describe, expect, it } from 'vitest';

import { apacheDataSource } from './apacheDataSource';
import { nginxDataSource } from './nginxDataSource';
import { sourceNameAttributeYaml } from './otelCollectorCommon';
import type { DataSourceDefinition } from './types';

const build = (source: DataSourceDefinition, overrides: Record<string, string> = {}) =>
  source.build({
    baseUrl: 'https://obzly.example.com',
    apiKey: 'test-key',
    fieldValues: {
      ...Object.fromEntries(source.fields.map((f) => [f.key, f.defaultValue])),
      ...overrides,
    },
  }).content;

describe('Source name', () => {
  it('sets obzly_source only when given, quoted', () => {
    expect(sourceNameAttributeYaml({ sourceName: '  ' })).toBe('');
    expect(sourceNameAttributeYaml({ sourceName: 'edge "1"' })).toContain('value: "edge \\"1\\""');
    expect(build(nginxDataSource)).not.toContain('obzly_source');
    expect(build(nginxDataSource, { sourceName: 'edge-proxy' })).toMatch(
      /- key: service\.name\n\s+value: nginx\n\s+action: upsert\n\s+- key: obzly_source\n\s+value: "edge-proxy"/,
    );
  });

  it('keeps the field names the configs always wrote (Web analytics maps them)', () => {
    const nginx = build(nginxDataSource, { sourceName: 'edge-proxy' });
    expect(nginx).toContain('(?P<status>');
    expect(nginx).toContain('parse_from: attributes.status');
    expect(build(apacheDataSource)).toContain('(?P<bytes_sent>');
  });
});
