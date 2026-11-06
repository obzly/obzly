// Mirrors backend internal/util/attrnames: the one naming rule for field,
// label and metric names (no dots, no "resource." prefix, PromQL-safe).

/** Drops a leading "resource.", turns anything outside [a-zA-Z0-9_] into "_", prefixes "_" before a digit. */
export function normalizeAttributeName(key: string): string {
  const withoutResource = key.startsWith('resource.') ? key.slice('resource.'.length) : key;
  const sanitized = withoutResource.replace(/[^a-zA-Z0-9_]/g, '_');
  return /^[0-9]/.test(sanitized) ? `_${sanitized}` : sanitized;
}
