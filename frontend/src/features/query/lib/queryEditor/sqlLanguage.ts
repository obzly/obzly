import {
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from '@codemirror/autocomplete';
import { PostgreSQL, sql } from '@codemirror/lang-sql';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import type { Extension } from '@codemirror/state';
import { type EditorView, type Tooltip, hoverTooltip } from '@codemirror/view';
import { tags } from '@lezer/highlight';

import type { QueryableField } from '../../../../entity/query';

const COLUMNS: Record<string, string> = {
  timestamp: 'TIMESTAMP - when the log happened',
  level: 'VARCHAR - ERROR, WARN, INFO, DEBUG, ...',
  message: 'VARCHAR - the log line',
  client_ip: 'VARCHAR - address the log was sent from',
  trace_id: 'VARCHAR - trace context, when sent',
  span_id: 'VARCHAR - span context, when sent',
  fields: "JSON - custom fields; json_extract_string(fields, '$.name')",
};

const FUNCTIONS: Record<string, string> = {
  json_extract_string: "json_extract_string(fields, '$.name') - a custom field as text",
  json_extract: "json_extract(fields, '$.name') - a custom field as JSON",
  date_trunc: "date_trunc('minute', timestamp) - bucket times",
  time_bucket: "time_bucket(INTERVAL '5 minutes', timestamp) - fixed-size buckets",
  count: 'count(*) - rows per group',
  approx_count_distinct: 'approx_count_distinct(x) - fast distinct count',
  regexp_matches: "regexp_matches(message, 'pattern') - regex test",
  regexp_extract: "regexp_extract(message, 'pattern', 1) - regex capture",
  strftime: "strftime(timestamp, '%H:%M') - format a time",
  quantile_cont: 'quantile_cont(x, 0.95) - percentile',
};

function sqlExtras(getFields: () => QueryableField[]) {
  return (context: CompletionContext): CompletionResult | null => {
    const word = context.matchBefore(/[\w.$']*/);
    if (!word || (word.from === word.to && !context.explicit)) return null;

    const options: Completion[] = Object.entries(FUNCTIONS).map(([name, info]) => ({
      label: name,
      apply: name + '(',
      type: 'function',
      info,
    }));
    for (const field of getFields()) {
      if (!field.isCustom) continue;
      const expression = `json_extract_string(fields, '$.${field.name}')`;
      options.push({
        label: field.name,
        apply: expression,
        detail: 'custom field',
        type: 'variable',
        info: expression,
      });
    }
    return { from: word.from, options, validFor: /^[\w.]*$/ };
  };
}

// Same palette as the search syntax (classes in index.css).
const sqlHighlight = HighlightStyle.define([
  { tag: tags.keyword, class: 'cmq-keyword' },
  { tag: [tags.string, tags.special(tags.string)], class: 'cmq-value' },
  { tag: tags.number, class: 'cmq-number' },
  { tag: [tags.typeName, tags.propertyName, tags.name], class: 'cmq-field' },
  { tag: [tags.operator, tags.punctuation], class: 'cmq-operator' },
  { tag: [tags.bool, tags.null], class: 'cmq-atom' },
  { tag: tags.comment, class: 'cmq-paren' },
]);

const sqlHover = hoverTooltip((view: EditorView, pos: number): Tooltip | null => {
  const line = view.state.doc.lineAt(pos);
  const text = line.text;
  let start = pos - line.from;
  let end = start;
  while (start > 0 && /[\w]/.test(text[start - 1])) start--;
  while (end < text.length && /[\w]/.test(text[end])) end++;
  const word = text.slice(start, end).toLowerCase();
  const doc = COLUMNS[word] ?? FUNCTIONS[word];
  if (!doc) return null;
  return {
    pos: line.from + start,
    end: line.from + end,
    above: true,
    create: () => {
      const dom = document.createElement('div');
      dom.className = 'cmq-tip';
      dom.textContent = doc;
      return { dom };
    },
  };
});

export function sqlLanguage(getFields: () => QueryableField[]): Extension[] {
  const language = sql({
    dialect: PostgreSQL,
    schema: { logs: Object.keys(COLUMNS) },
    defaultTable: 'logs',
    upperCaseKeywords: true,
  });
  return [
    language,
    syntaxHighlighting(sqlHighlight),
    language.language.data.of({ autocomplete: sqlExtras(getFields) }),
    sqlHover,
  ];
}
