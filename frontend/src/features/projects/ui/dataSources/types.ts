// Shared shape for every Data Sources catalog entry; one generic modal
// (DataSourceConfigModal) handles API key creation, copying and highlighting.

export interface DataSourceField {
  key: string;
  label: string;
  defaultValue: string;
  helpText?: string;
  placeholder?: string;
  /** When set, the field is a dropdown of these options (value is what build() receives) instead of free text. */
  options?: { value: string; label: string }[];
  /** A checkbox instead of free text; build() receives 'yes' or 'no'. */
  type?: 'checkbox';
}

export interface DataSourceBuildContext {
  /** Obzly's own endpoint, as reachable from wherever this source runs. */
  baseUrl: string;
  /** A freshly-created, dedicated API key's real value - shown once. */
  apiKey: string;
  /** Keyed by DataSourceField.key. */
  fieldValues: Record<string, string>;
}

export interface DataSourceConfigFile {
  /** Suggested filename, shown above this file's block and in the copy button. */
  filename: string;
  /** For the syntax highlighter: yaml, bash, ini, nginx, toml, etc. */
  language: string;
  content: string;
}

export interface DataSourceConfigResult {
  /** Suggested filename, shown in the "save/apply this as" hint. */
  filename: string;
  /** For the syntax highlighter: yaml, bash, ini, nginx, toml, etc. */
  language: string;
  content: string;
  /**
   * For configs spanning several files (e.g. a collector config plus its compose
   * service). Separate blocks stop users pasting everything into one file, where
   * OTel's `${env:VAR}` clashes with Docker Compose interpolation. When set,
   * `filename`/`content` are ignored.
   */
  files?: DataSourceConfigFile[];
  /** Short plain-text note shown below the generated config (e.g. how to apply/run it). */
  instructions: string;
}

export type DataSourceCategory =
  | 'Infrastructure'
  | 'Web Servers'
  | 'Databases'
  | 'Message Queues'
  | 'Languages'
  | 'Application Runtimes'
  | 'AI Tools'
  | 'Custom';

export interface DataSourceDefinition {
  id: string;
  name: string;
  category: DataSourceCategory;
  /** One line shown on the catalog card. */
  description: string;
  /** Extra fields beyond the always-present API key name + Obzly endpoint. */
  fields: DataSourceField[];
  build: (ctx: DataSourceBuildContext) => DataSourceConfigResult;
}
