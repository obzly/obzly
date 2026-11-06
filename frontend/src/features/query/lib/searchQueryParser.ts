import type { ConditionNode, LogicalOperator, QueryNode } from '../../../entity/query';

// Parses the log search bar's free-text syntax into the QueryNode tree the
// structured query API runs; what it can't express goes through the Advanced
// builder or SQL mode.
//
// Grammar (case-insensitive keywords; precedence NOT > AND > OR; AND is
// implicit between adjacent terms):
//   expr       := orExpr
//   orExpr     := andExpr ( "OR" andExpr )*
//   andExpr    := unary ( ["AND"] unary )*        -- no operator = implicit AND
//   unary      := "NOT" unary | ("-" primary) | primary
//   primary    := "(" orExpr ")" | term
//   term       := field ":" value   -- contains (value "*" => exists)
//               | field "="  value  -- equals
//               | field "!=" value  -- not_equals
//               | field ">=" value  -- greater_or_equal
//               | field "<=" value  -- less_or_equal
//               | field ">"  value  -- greater_than
//               | field "<"  value  -- less_than
//               | bareWord          -- message contains bareWord
// A value may be a quoted phrase ("like this") to include spaces/
// operators literally; a field name is [a-zA-Z_][a-zA-Z0-9_.]*.

export class SearchQuerySyntaxError extends Error {
  /** Offsets into the original input, when the error has a location. */
  constructor(
    message: string,
    public readonly from?: number,
    public readonly to?: number,
  ) {
    super(message);
  }
}

type TokenType = 'AND' | 'OR' | 'NOT' | 'LPAREN' | 'RPAREN' | 'TERM';

interface Token {
  type: TokenType;
  text: string; // raw text for TERM tokens (includes any leading "-", quotes as typed)
  start: number; // offset in the original (untrimmed) input
}

/** Returns null for empty input, meaning "match everything". */
export function parseSearchQuery(input: string): QueryNode | null {
  const trimmed = input.trim();
  if (!trimmed) return null;

  const tokens = tokenize(trimmed, input.length - input.trimStart().length);
  if (tokens.length === 0) return null;

  const parser = new Parser(tokens, input.trimEnd().length);
  const node = parser.parseOr();
  parser.expectEnd();
  return node;
}

/**
 * Splits a flat AND-only search (the shape click-to-filter builds) into its
 * clauses so a repeat click can toggle a value off. Returns null for OR, NOT
 * or parens, where removing a clause by text could change the query's logic.
 */
export function splitTopLevelAndClauses(input: string): string[] | null {
  const trimmed = input.trim();
  if (!trimmed) return [];

  let tokens: Token[];
  try {
    tokens = tokenize(trimmed);
  } catch {
    return null;
  }
  if (tokens.length === 0) return [];

  const clauses: string[] = [];
  let expectTerm = true;
  for (const token of tokens) {
    if (
      token.type === 'OR' ||
      token.type === 'NOT' ||
      token.type === 'LPAREN' ||
      token.type === 'RPAREN'
    ) {
      return null;
    }
    if (token.type === 'AND') {
      if (expectTerm) return null; // "AND AND" or leading "AND" - not our flat shape
      expectTerm = true;
      continue;
    }
    if (!expectTerm) return null; // two adjacent terms with no AND between them
    clauses.push(token.text);
    expectTerm = false;
  }
  if (expectTerm) return null; // trailing "AND" with nothing after it

  return clauses;
}

function tokenize(input: string, offset = 0): Token[] {
  const tokens: Token[] = [];
  let i = 0;

  while (i < input.length) {
    const c = input[i];

    if (c === ' ' || c === '\t' || c === '\n') {
      i++;
      continue;
    }
    if (c === '(') {
      tokens.push({ type: 'LPAREN', text: '(', start: offset + i });
      i++;
      continue;
    }
    if (c === ')') {
      tokens.push({ type: 'RPAREN', text: ')', start: offset + i });
      i++;
      continue;
    }

    // One word, which may embed a quoted section with spaces or parens.
    const start = i;
    let raw = '';
    while (i < input.length && input[i] !== ' ' && input[i] !== '\t' && input[i] !== '\n') {
      if (input[i] === '(' || input[i] === ')') {
        // Stop at "(" so e.g. "field:(x)" isn't swallowed whole.
        if (raw !== '') break;
      }
      if (input[i] === '"') {
        const quoteStart = i;
        i++;
        while (i < input.length && input[i] !== '"') {
          if (input[i] === '\\' && i + 1 < input.length) i++; // skip escaped char
          i++;
        }
        if (i >= input.length) {
          throw new SearchQuerySyntaxError(
            `Unterminated quote starting at position ${quoteStart}`,
            offset + quoteStart,
            offset + input.length,
          );
        }
        i++; // consume closing quote
        raw += input.slice(quoteStart, i);
        continue;
      }
      raw += input[i];
      i++;
    }

    if (raw === '') {
      // Guard against an infinite loop on a lone "(" or ")".
      if (i === start) i++;
      continue;
    }

    const upper = raw.toUpperCase();
    const at = offset + start;
    if (upper === 'AND') tokens.push({ type: 'AND', text: raw, start: at });
    else if (upper === 'OR') tokens.push({ type: 'OR', text: raw, start: at });
    else if (upper === 'NOT') tokens.push({ type: 'NOT', text: raw, start: at });
    else tokens.push({ type: 'TERM', text: raw, start: at });
  }

  return tokens;
}

class Parser {
  private pos = 0;

  constructor(
    private readonly tokens: Token[],
    private readonly end: number,
  ) {}

  private peek(): Token | undefined {
    return this.tokens[this.pos];
  }

  private next(): Token {
    const token = this.tokens[this.pos];
    if (!token) throw new SearchQuerySyntaxError('Unexpected end of query', this.end, this.end);
    this.pos++;
    return token;
  }

  expectEnd(): void {
    if (this.pos < this.tokens.length) {
      const token = this.tokens[this.pos];
      throw new SearchQuerySyntaxError(
        `Unexpected "${token.text}" - check parentheses`,
        token.start,
        token.start + token.text.length,
      );
    }
  }

  parseOr(): QueryNode {
    let left = this.parseAnd();
    const children: QueryNode[] = [left];

    while (this.peek()?.type === 'OR') {
      this.next();
      children.push(this.parseAnd());
    }

    if (children.length === 1) return left;
    left = combine('or', children);
    return left;
  }

  parseAnd(): QueryNode {
    let left = this.parseUnary();
    const children: QueryNode[] = [left];

    for (;;) {
      const token = this.peek();
      if (!token) break;
      if (token.type === 'AND') {
        this.next();
        children.push(this.parseUnary());
        continue;
      }
      if (token.type === 'OR' || token.type === 'RPAREN') break;
      // Implicit AND: another primary follows directly.
      children.push(this.parseUnary());
    }

    if (children.length === 1) return left;
    left = combine('and', children);
    return left;
  }

  parseUnary(): QueryNode {
    const token = this.peek();
    if (token?.type === 'NOT') {
      this.next();
      return { type: 'logical', logic: { operator: 'not', children: [this.parseUnary()] } };
    }
    if (token?.type === 'TERM' && token.text.startsWith('-') && token.text.length > 1) {
      this.next();
      const inner = parseTerm(token.text.slice(1), token.start + 1);
      return { type: 'logical', logic: { operator: 'not', children: [inner] } };
    }
    return this.parsePrimary();
  }

  parsePrimary(): QueryNode {
    const token = this.peek();
    if (!token) throw new SearchQuerySyntaxError('Expected a term or "("', this.end, this.end);

    if (token.type === 'LPAREN') {
      this.next();
      const node = this.parseOr();
      const closing = this.peek();
      if (closing?.type !== 'RPAREN') {
        throw new SearchQuerySyntaxError('Missing closing ")"', token.start, token.start + 1);
      }
      this.next();
      return node;
    }

    if (token.type === 'TERM') {
      this.next();
      return parseTerm(token.text, token.start);
    }

    throw new SearchQuerySyntaxError(
      `Unexpected "${token.text}"`,
      token.start,
      token.start + token.text.length,
    );
  }
}

function combine(operator: LogicalOperator, children: QueryNode[]): QueryNode {
  return { type: 'logical', logic: { operator, children } };
}

const FIELD_OP_PATTERN = /^([a-zA-Z_][a-zA-Z0-9_.]*)(!=|>=|<=|:|=|>|<)(.*)$/s;

function parseTerm(raw: string, start = 0): QueryNode {
  const match = FIELD_OP_PATTERN.exec(raw);
  if (!match) {
    return condition('message', 'contains', dequote(raw));
  }

  const [, field, op, rawValue] = match;
  const value = dequote(rawValue);

  switch (op) {
    case ':':
      // The backend rejects "contains" on a timestamp; flag it while typing.
      if (field === 'timestamp') {
        throw new SearchQuerySyntaxError(
          'timestamp can\'t be matched with ":" - use the time picker, click or drag on the histogram, or a range like timestamp>=2026-09-23T06:40:00Z',
          start,
          start + raw.length,
        );
      }
      return value === '*' ? condition(field, 'exists', null) : condition(field, 'contains', value);
    case '=':
      return condition(field, 'equals', value);
    case '!=':
      return condition(field, 'not_equals', value);
    case '>':
      return condition(field, 'greater_than', coerceNumeric(value));
    case '>=':
      return condition(field, 'greater_or_equal', coerceNumeric(value));
    case '<':
      return condition(field, 'less_than', coerceNumeric(value));
    case '<=':
      return condition(field, 'less_or_equal', coerceNumeric(value));
    default:
      return condition('message', 'contains', dequote(raw));
  }
}

function condition(
  field: string,
  operator: ConditionNode['operator'],
  value: ConditionNode['value'],
): QueryNode {
  return { type: 'condition', condition: { field, operator, value } };
}

function dequote(value: string): string {
  if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
    return value.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  }
  return value;
}

function coerceNumeric(value: string): string | number {
  if (value.trim() === '') return value;
  const n = Number(value);
  return Number.isFinite(n) ? n : value;
}
