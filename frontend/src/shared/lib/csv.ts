// A plain number can't be a formula, and prefixing "-5" would corrupt it.
const NUMERIC = /^[+-]?(\d+(\.\d*)?|\.\d+)(e[+-]?\d+)?$/i;

// A leading =, +, -, @, tab or carriage return runs as a formula in some
// spreadsheets (OWASP CSV injection), so such text gets a ' prefix.
const FORMULA_START = /^[=+\-@\t\r]/;

const csvField = (value: string | number | undefined | null): string => {
  if (value === undefined || value === null) return '""';
  const text = String(value);
  const safe =
    typeof value !== 'number' && FORMULA_START.test(text) && !NUMERIC.test(text)
      ? `'${text}`
      : text;
  return `"${safe.replace(/"/g, '""')}"`;
};

/** RFC 4180 CSV: every field quoted, so commas, quotes and newlines survive. */
export const toCSV = (rows: (string | number | undefined | null)[][]): string =>
  rows.map((row) => row.map(csvField).join(',')).join('\r\n');
