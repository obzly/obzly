import { describe, expect, it } from 'vitest';

import {
  TEMPLATE_VARIABLES,
  errorLines,
  formatTemplateError,
  insertSnippet,
  isTemplateDirty,
  severityLabel,
  templateFor,
} from './messageTemplates';

describe('insertSnippet', () => {
  it('replaces the selection and puts the cursor after the snippet', () => {
    expect(insertSnippet('Hello world', 6, 11, '{{.Rule.Name}}')).toEqual({
      text: 'Hello {{.Rule.Name}}',
      cursor: 20,
    });
  });

  it('clamps a cursor outside the text', () => {
    expect(insertSnippet('ab', 10, 12, 'x')).toEqual({ text: 'abx', cursor: 3 });
    expect(insertSnippet('ab', -3, -1, 'x')).toEqual({ text: 'xab', cursor: 1 });
  });
});

describe('template errors', () => {
  const errors = [
    { field: 'body' as const, line: 3, message: 'unexpected "}"' },
    { field: 'body' as const, line: 1, message: 'bad' },
    { field: 'body' as const, line: 3, message: 'again' },
    { field: 'subject' as const, line: 0, message: "can't be empty" },
  ];

  it('formats with the line when known', () => {
    expect(formatTemplateError(errors[0])).toBe('Body, line 3: unexpected "}"');
    expect(formatTemplateError(errors[3])).toBe("Subject: can't be empty");
  });

  it('lists each erroring line of a field once, in order', () => {
    expect(errorLines(errors, 'body')).toEqual([1, 3]);
    expect(errorLines(errors, 'subject')).toEqual([]);
  });
});

describe('templates', () => {
  it('keeps a subject for email only', () => {
    expect(templateFor('slack', { subject: 's', body: 'b' })).toEqual({ subject: '', body: 'b' });
    expect(templateFor('email', { subject: 's', body: 'b' })).toEqual({ subject: 's', body: 'b' });
  });

  it('is dirty when subject or body changed', () => {
    expect(isTemplateDirty({ subject: '', body: 'a' }, { subject: '', body: 'a' })).toBe(false);
    expect(isTemplateDirty({ subject: '', body: 'a' }, { subject: '', body: 'b' })).toBe(true);
  });

  it('labels severities, medium when unknown', () => {
    expect(severityLabel('critical')).toBe('Critical');
    expect(severityLabel(undefined)).toBe('Medium');
  });

  it('offers every variable as a template action', () => {
    for (const variable of TEMPLATE_VARIABLES) {
      expect(variable.snippet).toMatch(/^\{\{.*\}\}/s);
    }
    expect(TEMPLATE_VARIABLES.map((v) => v.label)).toContain('.Emoji');
    expect(TEMPLATE_VARIABLES.map((v) => v.label)).toContain('.Link');
  });
});
