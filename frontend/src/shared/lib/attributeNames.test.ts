import { describe, expect, it } from 'vitest';

import { normalizeAttributeName } from './attributeNames';

// The same rule as backend internal/util/attrnames - the two must agree, or
// a field picked in the UI wouldn't match what was stored.
describe('normalizeAttributeName', () => {
  it.each([
    ['resource.k8s.pod.name', 'k8s_pod_name'],
    ['system.cpu.time', 'system_cpu_time'],
    ['http.status-code', 'http_status_code'],
    ['already_normal', 'already_normal'],
    ['9lives', '_9lives'],
    ['resourceful.thing', 'resourceful_thing'],
  ])('%s -> %s', (input, expected) => {
    expect(normalizeAttributeName(input)).toBe(expected);
  });
});
