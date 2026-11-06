import type { LogItem } from './LogItem';
import type { QueryNode } from './QueryNode';

export interface LogContextRequest {
  anchor: { id: string; timestamp: string };
  before?: number;
  after?: number;
  /** Fields the context shares with the anchor; omitted = its source, [] = the whole project. */
  scope?: string[];
  query?: QueryNode | null;
  windowMinutes?: number;
  /** Load more of one side from its cursor. */
  direction?: 'before' | 'after';
  cursor?: string;
}

export interface LogContextScope {
  field: string;
  value: string;
}

export interface LogContextResponse {
  /** null when the log is gone (retention, deletion). */
  anchor: LogItem | null;
  /** Oldest first. */
  before: LogItem[];
  /** Oldest first. */
  after: LogItem[];
  scope: LogContextScope[];
  beforeCursor?: string;
  afterCursor?: string;
  windowMinutes: number;
  partial?: boolean;
}
