import { describe, expect, it } from 'vitest';

import { toCSV } from './csv';

describe('toCSV', () => {
  it('quotes fields, doubles quotes and defuses formulas', () => {
    expect(
      toCSV([
        ['a', 'say "hi", ok'],
        ['=SUM(A1)', undefined],
      ]),
    ).toBe('"a","say ""hi"", ok"\r\n"\'=SUM(A1)",""');
  });

  it('Test_ToCSV_LeadingTabOrCarriageReturn_IsPrefixed', () => {
    expect(toCSV([['\t=1+1', '\r=HYPERLINK("x")', '@cmd']])).toBe(
      '"\'\t=1+1","\'\r=HYPERLINK(""x"")","\'@cmd"',
    );
  });

  it('Test_ToCSV_Numbers_AreLeftAlone', () => {
    expect(toCSV([[-5, '-12.5', '+3', '1e-3', '-1+cmd|x']])).toBe(
      '"-5","-12.5","+3","1e-3","\'-1+cmd|x"',
    );
  });
});
