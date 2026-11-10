const formatters = new Map<string, Intl.NumberFormat>();

const formatter = (currency: string, compact: boolean): Intl.NumberFormat | undefined => {
  const key = `${currency}|${compact}`;
  if (!formatters.has(key)) {
    try {
      formatters.set(
        key,
        new Intl.NumberFormat(undefined, {
          style: 'currency',
          currency,
          ...(compact
            ? { notation: 'compact', maximumFractionDigits: 1 }
            : { minimumFractionDigits: 2, maximumFractionDigits: 2 }),
        }),
      );
    } catch {
      // Not an ISO 4217 code: shown as a plain number with the code after it.
      return undefined;
    }
  }
  return formatters.get(key);
};

/** An amount in its own currency; amounts in different currencies are never added. */
export const formatMoney = (amount: number, currency: string): string =>
  formatter(currency, false)?.format(amount) ?? `${amount.toFixed(2)} ${currency}`;

export const formatMoneyCompact = (amount: number, currency: string): string =>
  formatter(currency, true)?.format(amount) ?? `${amount.toFixed(0)} ${currency}`;

/** "+12.5%" for a change against before; '' when there is nothing to compare with. */
export const formatChange = (now: number, before: number): string => {
  if (!before) return '';
  const change = ((now - before) / Math.abs(before)) * 100;
  return `${change >= 0 ? '+' : ''}${change.toFixed(1)}%`;
};
