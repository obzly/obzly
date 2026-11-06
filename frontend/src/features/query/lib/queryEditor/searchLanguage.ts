import {
  type Completion,
  type CompletionContext,
  type CompletionResult,
  startCompletion,
} from '@codemirror/autocomplete';
import {
  HighlightStyle,
  StreamLanguage,
  type StringStream,
  syntaxHighlighting,
} from '@codemirror/language';
import { type Diagnostic, linter } from '@codemirror/lint';
import { type EditorState, type Extension, StateEffect, StateField } from '@codemirror/state';
import { EditorView, type Tooltip, hoverTooltip, showTooltip } from '@codemirror/view';
import { tags } from '@lezer/highlight';

import type { QueryableField } from '../../../../entity/query';
import { normalizeAttributeName } from '../../../../shared/lib/attributeNames';
import { KNOWN_LOG_LEVELS } from '../logLevels';
import { SearchQuerySyntaxError, parseSearchQuery } from '../searchQueryParser';

export interface ValueSuggestion {
  value: string;
  count?: number;
  /** Shown instead of the count (e.g. "last seen 3 hours ago"). */
  detail?: string;
}

export interface SearchLanguageOptions {
  getFields: () => QueryableField[];
  /** Top values of a field over the current time range. */
  loadValues?: (field: string) => Promise<ValueSuggestion[]>;
}

// --- Highlighting ---------------------------------------------------------

interface TokenState {
  afterOperator: boolean;
}

const FIELD_BEFORE_OPERATOR = /^[a-zA-Z_][a-zA-Z0-9_.]*(?=(!=|>=|<=|:|=|>|<))/;
const OPERATOR = /^(!=|>=|<=|:|=|>|<)/;
const QUOTED = /^"(?:[^"\\]|\\.)*"?/;
const KEYWORD = /^(AND|OR|NOT)(?=[\s()]|$)/i;

const searchStreamLanguage = StreamLanguage.define<TokenState>({
  name: 'obzly-search',
  startState: () => ({ afterOperator: false }),
  token(stream: StringStream, state: TokenState): string | null {
    if (stream.eatSpace()) {
      state.afterOperator = false;
      return null;
    }
    if (state.afterOperator) {
      state.afterOperator = false;
      if (stream.match(QUOTED)) return 'string';
      if (stream.match(/^\*(?=[\s)]|$)/)) return 'atom';
      if (stream.match(/^-?\d+(\.\d+)?(?=[\s)]|$)/)) return 'number';
      if (stream.match(/^[^\s()"]+/)) return 'string';
    }
    const ch = stream.peek();
    if (ch === '(' || ch === ')') {
      stream.next();
      return 'paren';
    }
    if (stream.sol() || /[\s(]/.test(stream.string.charAt(stream.pos - 1) || ' ')) {
      if (stream.match(KEYWORD)) return 'keyword';
      if (ch === '-' && /[^\s()]/.test(stream.string.charAt(stream.pos + 1))) {
        stream.next();
        return 'operator';
      }
    }
    if (stream.match(FIELD_BEFORE_OPERATOR)) return 'propertyName';
    if (stream.match(OPERATOR)) {
      state.afterOperator = true;
      return 'operator';
    }
    if (stream.match(QUOTED)) return 'string';
    if (!stream.match(/^[^\s()"]+/)) stream.next();
    return null;
  },
  tokenTable: { paren: tags.paren },
});

const searchHighlight = HighlightStyle.define([
  { tag: tags.propertyName, class: 'cmq-field' },
  { tag: tags.operator, class: 'cmq-operator' },
  { tag: tags.string, class: 'cmq-value' },
  { tag: tags.number, class: 'cmq-number' },
  { tag: tags.atom, class: 'cmq-atom' },
  { tag: tags.keyword, class: 'cmq-keyword' },
  { tag: tags.paren, class: 'cmq-paren' },
]);

// --- Shared term scanning ---------------------------------------------------

interface Term {
  from: number;
  to: number;
  negated: boolean;
  field?: string;
  fieldFrom?: number;
  operator?: string;
  operatorFrom?: number;
  valueFrom?: number;
}

/** Splits the query into terms (outside quotes, split on spaces and parens). */
function scanTerms(text: string): Term[] {
  const terms: Term[] = [];
  let i = 0;
  while (i < text.length) {
    if (/[\s()]/.test(text[i])) {
      i++;
      continue;
    }
    const start = i;
    let inQuote = false;
    while (i < text.length && (inQuote || !/[\s()]/.test(text[i]))) {
      if (text[i] === '\\' && inQuote) i++;
      else if (text[i] === '"') inQuote = !inQuote;
      i++;
    }
    terms.push(describeTerm(text.slice(start, i), start));
  }
  return terms;
}

const TERM_PATTERN = /^([a-zA-Z_][a-zA-Z0-9_.]*)(!=|>=|<=|:|=|>|<)/;

function describeTerm(raw: string, start: number): Term {
  const negated = raw.startsWith('-') && raw.length > 1;
  const body = negated ? raw.slice(1) : raw;
  const bodyStart = negated ? start + 1 : start;
  const term: Term = { from: start, to: start + raw.length, negated };
  const match = TERM_PATTERN.exec(body);
  if (match) {
    term.field = match[1];
    term.fieldFrom = bodyStart;
    term.operator = match[2];
    term.operatorFrom = bodyStart + match[1].length;
    term.valueFrom = term.operatorFrom + match[2].length;
  }
  return term;
}

/** The (possibly unfinished) term that ends at pos, if the cursor is inside one. */
function termAt(text: string, pos: number): Term | null {
  return scanTerms(text).find((t) => t.from <= pos && pos <= t.to) ?? null;
}

// --- Documentation ------------------------------------------------------------

const OPERATOR_DOCS: Record<string, { name: string; detail: string }> = {
  ':': {
    name: 'contains',
    detail: 'Text match (case-sensitive, except level). field:* means the field exists.',
  },
  '=': { name: 'equals', detail: 'Exact value.' },
  '!=': { name: 'not equals', detail: 'Anything but this value (also matches missing).' },
  '>': { name: 'greater than', detail: 'Numbers, or timestamps as RFC3339.' },
  '>=': { name: 'at least', detail: 'Numbers, or timestamps as RFC3339.' },
  '<': { name: 'less than', detail: 'Numbers, or timestamps as RFC3339.' },
  '<=': { name: 'at most', detail: 'Numbers, or timestamps as RFC3339.' },
};

const KEYWORD_DOCS: Record<string, string> = {
  AND: 'Both sides must match. Also implied between terms: a b = a AND b.',
  OR: 'Either side matches. AND binds tighter; use parentheses to group.',
  NOT: 'Excludes matches. -term is the short form.',
};

const SYSTEM_FIELD_DOCS: Record<string, string> = {
  level: 'Severity: ' + KNOWN_LOG_LEVELS.join(', ') + ' (any case matches).',
  message: 'The log line. A bare word searches it.',
  client_ip: 'Address the log was sent from.',
  timestamp: 'When the log happened. Use ranges: timestamp>=2026-09-24T06:00:00Z.',
  trace_id: 'Trace the log belongs to, when sent with trace context.',
  span_id: 'Span the log belongs to.',
  pattern_hash: 'Message template id (see Patterns).',
};

const OPERATOR_SYMBOLS: Record<string, string> = {
  contains: ':',
  equals: '=',
  not_equals: '!=',
  greater_than: '>',
  greater_or_equal: '>=',
  less_than: '<',
  less_or_equal: '<=',
  exists: ':*',
};

function fieldSummary(field: QueryableField | undefined, name: string): string {
  const doc = SYSTEM_FIELD_DOCS[name];
  const type = field?.type ?? 'unknown';
  const ops = (field?.operations ?? [])
    .map((op) => OPERATOR_SYMBOLS[op])
    .filter(Boolean)
    .join('  ');
  return [doc, `Type: ${type}${field?.isCustom ? ' (custom)' : ''}`, ops ? `Operators: ${ops}` : '']
    .filter(Boolean)
    .join('\n');
}

function tooltipDom(title: string, body: string): HTMLElement {
  const dom = document.createElement('div');
  dom.className = 'cmq-tip';
  const heading = document.createElement('div');
  heading.className = 'cmq-tip-title';
  heading.textContent = title;
  dom.appendChild(heading);
  for (const line of body.split('\n').filter(Boolean)) {
    const row = document.createElement('div');
    row.textContent = line;
    dom.appendChild(row);
  }
  return dom;
}

// --- Completion -------------------------------------------------------------

const valueCache = new Map<string, { at: number; values: Promise<ValueSuggestion[]> }>();
const VALUE_CACHE_MS = 30_000;

function cachedValues(options: SearchLanguageOptions, field: string): Promise<ValueSuggestion[]> {
  if (field === 'level') {
    return Promise.resolve(KNOWN_LOG_LEVELS.map((value) => ({ value })));
  }
  if (!options.loadValues) return Promise.resolve([]);
  const cached = valueCache.get(field);
  if (cached && Date.now() - cached.at < VALUE_CACHE_MS) return cached.values;
  const values = options.loadValues(field).catch(() => [] as ValueSuggestion[]);
  valueCache.set(field, { at: Date.now(), values });
  return values;
}

/** Clears cached value suggestions (e.g. after the time range changes). */
export function clearValueSuggestions(): void {
  valueCache.clear();
}

// Inserts a field and its operator, then opens value suggestions straight away.
const applyAndReopen =
  (text: string) =>
  (view: EditorView, _completion: Completion, from: number, to: number): void => {
    view.dispatch({
      changes: { from, to, insert: text },
      selection: { anchor: from + text.length },
      userEvent: 'input.complete',
    });
    startCompletion(view);
  };

const quoteIfNeeded = (value: string): string =>
  /^[^\s()"]+$/.test(value) ? value : `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;

function searchCompletion(options: SearchLanguageOptions) {
  return async (context: CompletionContext): Promise<CompletionResult | null> => {
    const text = context.state.doc.toString();
    const pos = context.pos;
    const term = termAt(text, pos);

    // Value position: field<op>|
    if (term?.field && term.valueFrom !== undefined && pos >= term.valueFrom) {
      const typed = text.slice(term.valueFrom, pos).replace(/^"/, '').toLowerCase();
      const values = await cachedValues(options, term.field);
      if (context.aborted) return null;
      const completions: Completion[] = values
        .filter((v) => v.value.toLowerCase().includes(typed))
        .slice(0, 50)
        .map((v, i) => ({
          label: v.value,
          apply: quoteIfNeeded(v.value) + ' ',
          detail: v.detail ?? (v.count !== undefined ? v.count.toLocaleString() : undefined),
          type: 'constant',
          boost: -i,
        }));
      if (term.operator === ':') {
        completions.push({
          label: '*',
          apply: '* ',
          detail: 'field exists',
          type: 'keyword',
          boost: -99,
        });
      }
      return completions.length
        ? { from: term.valueFrom, options: completions, filter: false }
        : null;
    }

    // Field / keyword position.
    const wordStart = term ? (term.negated ? term.from + 1 : term.from) : pos;
    const word = text.slice(wordStart, pos);
    if (!context.explicit && word.length === 0) return null;

    const completions: Completion[] = options.getFields().map((f) => ({
      label: f.name,
      apply: applyAndReopen(`${f.name}${f.name === 'timestamp' ? '>=' : ':'}`),
      detail: f.type + (f.isCustom ? ' · custom' : ''),
      info: () => tooltipDom(f.name, fieldSummary(f, f.name)),
      type: f.isCustom ? 'variable' : 'property',
      boost: f.isCustom ? 0 : 1,
    }));
    const precededByTerm = text.slice(0, wordStart).trim().length > 0;
    if (precededByTerm && !term?.negated) {
      for (const keyword of ['AND', 'OR', 'NOT']) {
        completions.push({
          label: keyword,
          apply: keyword + ' ',
          type: 'keyword',
          info: KEYWORD_DOCS[keyword],
          boost: 2,
        });
      }
    }
    return { from: wordStart, options: completions, validFor: /^[\w.]*$/ };
  };
}

// --- Hover and inline hints ---------------------------------------------------

function searchHover(options: SearchLanguageOptions) {
  return hoverTooltip((view: EditorView, pos: number): Tooltip | null => {
    const text = view.state.doc.toString();
    const term = termAt(text, pos);
    if (!term) return null;

    const upper = text.slice(term.from, term.to).toUpperCase();
    if (KEYWORD_DOCS[upper]) {
      return tip(term.from, term.to, upper, KEYWORD_DOCS[upper]);
    }
    if (term.negated && pos === term.from) {
      return tip(term.from, term.from + 1, 'NOT', KEYWORD_DOCS.NOT);
    }
    if (term.field && term.fieldFrom !== undefined && term.operatorFrom !== undefined) {
      if (pos < term.operatorFrom) {
        const field = options
          .getFields()
          .find((f) => f.name === normalizeAttributeName(term.field ?? ''));
        const known = field || SYSTEM_FIELD_DOCS[term.field];
        return tip(
          term.fieldFrom,
          term.operatorFrom,
          term.field,
          known ? fieldSummary(field, term.field) : 'No logs in this project have this field.',
        );
      }
      if (term.operator && pos < (term.valueFrom ?? 0)) {
        const doc = OPERATOR_DOCS[term.operator];
        return tip(
          term.operatorFrom,
          term.valueFrom ?? term.operatorFrom,
          `${term.operator}  ${doc.name}`,
          doc.detail,
        );
      }
      return null;
    }
    return tip(
      term.from,
      term.to,
      'message contains',
      'A bare word or "quoted phrase" searches the message.',
    );
  });
}

function tip(from: number, to: number, title: string, body: string): Tooltip {
  return { pos: from, end: to, above: true, create: () => ({ dom: tooltipDom(title, body) }) };
}

const setFocused = StateEffect.define<boolean>();

/** Parameter hint while typing a value: what the operator does and what to type. */
function inlineHint(options: SearchLanguageOptions): Extension {
  const compute = (state: EditorState, focused: boolean): readonly Tooltip[] => {
    if (!focused) return [];
    const selection = state.selection.main;
    if (!selection.empty) return [];
    const text = state.doc.toString();
    const term = termAt(text, selection.head);
    if (!term?.field || term.valueFrom === undefined || selection.head < term.valueFrom) return [];

    const doc = OPERATOR_DOCS[term.operator ?? ''];
    const field = options
      .getFields()
      .find((f) => f.name === normalizeAttributeName(term.field ?? ''));
    const type = field?.type ?? (term.field === 'timestamp' ? 'timestamp' : 'text');
    const example =
      term.field === 'level'
        ? 'e.g. error'
        : type === 'timestamp'
          ? 'e.g. 2026-09-24T06:00:00Z'
          : type === 'number' || type === 'integer' || type === 'float'
            ? 'e.g. 500'
            : 'quote values with spaces: "like this"';
    return [
      {
        pos: term.fieldFrom ?? term.from,
        above: true,
        strictSide: true,
        arrow: false,
        create: () => ({
          dom: tooltipDom(
            `${term.field} ${term.operator}  ${doc?.name ?? ''}`,
            `${doc?.detail ?? ''}\n${type} · ${example}`,
          ),
        }),
      },
    ];
  };

  // Hidden while the editor is blurred so it doesn't cover the page.
  const field = StateField.define<{ focused: boolean; tips: readonly Tooltip[] }>({
    create: () => ({ focused: false, tips: [] }),
    update: (value, tr) => {
      let focused = value.focused;
      for (const e of tr.effects) if (e.is(setFocused)) focused = e.value;
      if (focused === value.focused && !tr.docChanged && !tr.selection) return value;
      return { focused, tips: compute(tr.state, focused) };
    },
    provide: (f) => showTooltip.computeN([f], (state) => state.field(f).tips),
  });
  return [field, EditorView.focusChangeEffect.of((_state, focusing) => setFocused.of(focusing))];
}

// --- Lint --------------------------------------------------------------------

function searchLint(options: SearchLanguageOptions): Extension {
  return linter(
    (view: EditorView): Diagnostic[] => {
      const text = view.state.doc.toString();
      const diagnostics: Diagnostic[] = [];
      try {
        parseSearchQuery(text);
      } catch (e: unknown) {
        let from = e instanceof SearchQuerySyntaxError && e.from !== undefined ? e.from : 0;
        let to = e instanceof SearchQuerySyntaxError && e.to !== undefined ? e.to : text.length;
        // An error at the very end (e.g. a trailing AND) underlines the last word.
        if (from >= text.trimEnd().length) {
          to = text.trimEnd().length;
          from = Math.max(0, text.slice(0, to).search(/\S+$/));
        }
        diagnostics.push({
          from: Math.min(from, text.length),
          to: Math.min(Math.max(to, from + 1), text.length),
          severity: 'error',
          message: (e as Error).message,
        });
      }

      const fields = options.getFields();
      if (fields.length > 0) {
        const known = new Set(fields.map((f) => f.name));
        for (const term of scanTerms(text)) {
          if (
            term.field &&
            term.fieldFrom !== undefined &&
            !known.has(normalizeAttributeName(term.field)) &&
            !SYSTEM_FIELD_DOCS[term.field]
          ) {
            diagnostics.push({
              from: term.fieldFrom,
              to: term.operatorFrom ?? term.to,
              severity: 'warning',
              message: `No logs in this project have a "${term.field}" field.`,
            });
          }
        }
      }
      return diagnostics;
    },
    { delay: 250 },
  );
}

export function searchLanguage(options: SearchLanguageOptions): Extension[] {
  return [
    searchStreamLanguage,
    syntaxHighlighting(searchHighlight),
    searchStreamLanguage.data.of({ autocomplete: searchCompletion(options) }),
    searchHover(options),
    inlineHint(options),
    searchLint(options),
  ];
}
