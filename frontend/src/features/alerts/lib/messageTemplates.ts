import type {
  ChannelKind,
  MessageTemplate,
  Severity,
  TemplateError,
} from '../../../entity/alerting';

export const SEVERITIES: { value: Severity; label: string }[] = [
  { value: 'critical', label: 'Critical' },
  { value: 'high', label: 'High' },
  { value: 'medium', label: 'Medium' },
  { value: 'low', label: 'Low' },
];

export const severityLabel = (severity: string | undefined): string =>
  SEVERITIES.find((item) => item.value === severity)?.label ?? 'Medium';

export const TEMPLATE_KINDS: { value: ChannelKind; label: string }[] = [
  { value: 'email', label: 'Email' },
  { value: 'slack', label: 'Slack' },
  { value: 'pagerduty', label: 'PagerDuty' },
  { value: 'webhook', label: 'Webhook' },
];

/** What a kind's body is, for the editor's label. */
export const BODY_LABEL: Record<ChannelKind, string> = {
  email: 'Body (plain text)',
  slack: 'Message (Slack mrkdwn)',
  pagerduty: 'Summary (up to 1024 characters)',
  webhook: 'Custom JSON body',
};

export interface TemplateVariable {
  /** What gets inserted. */
  snippet: string;
  label: string;
  description: string;
  group: 'Alert' | 'Rule' | 'Where' | 'Incident' | 'Helpers';
}

// Mirrors the backend's templateData (alerting/templates.go).
export const TEMPLATE_VARIABLES: TemplateVariable[] = [
  {
    group: 'Alert',
    snippet: '{{.Emoji}}',
    label: '.Emoji',
    description: 'Severity/status emoji (empty when "Use emoji" is off)',
  },
  {
    group: 'Alert',
    snippet: '{{.Status}}',
    label: '.Status',
    description: 'firing, resolved or test',
  },
  {
    group: 'Alert',
    snippet: '{{upper .Status}}',
    label: 'upper .Status',
    description: 'FIRING, RESOLVED or TEST',
  },
  {
    group: 'Alert',
    snippet: '{{.Severity}}',
    label: '.Severity',
    description: 'Critical, High, Medium or Low',
  },
  {
    group: 'Alert',
    snippet: '{{.SeverityKey}}',
    label: '.SeverityKey',
    description: 'critical, high, medium or low',
  },
  { group: 'Alert', snippet: '{{.Summary}}', label: '.Summary', description: "The rule's summary" },
  { group: 'Alert', snippet: '{{.Value}}', label: '.Value', description: 'The measured value' },
  {
    group: 'Alert',
    snippet: '{{.Comparison}} {{.Threshold}}',
    label: '.Comparison .Threshold',
    description: 'e.g. > 0.05',
  },
  {
    group: 'Alert',
    snippet: '{{.StartsAt}}',
    label: '.StartsAt',
    description: 'When it started (UTC); .StartsAtISO for RFC 3339',
  },
  {
    group: 'Alert',
    snippet: '{{.EndsAt}}',
    label: '.EndsAt',
    description: 'When it resolved (UTC), empty while firing',
  },
  {
    group: 'Alert',
    snippet: '{{.Duration}}',
    label: '.Duration',
    description: 'How long it has fired (or fired), e.g. 12m',
  },
  {
    group: 'Alert',
    snippet: '{{if .Test}}…{{end}}',
    label: '.Test',
    description: 'True for test notifications',
  },
  {
    group: 'Alert',
    snippet: '{{range .SortedLabels}}{{.Name}}={{.Value}}\n{{end}}',
    label: 'range .SortedLabels',
    description: 'Every label, sorted',
  },
  {
    group: 'Alert',
    snippet: '{{label .Labels "service_name"}}',
    label: 'label .Labels "name"',
    description: 'One label (empty when absent)',
  },
  {
    group: 'Rule',
    snippet: '{{.Rule.Name}}',
    label: '.Rule.Name',
    description: "The rule's name (or the incident's title)",
  },
  {
    group: 'Rule',
    snippet: '{{.Rule.Description}}',
    label: '.Rule.Description',
    description: "The rule's description / runbook",
  },
  {
    group: 'Rule',
    snippet: '{{.Source}}',
    label: '.Source',
    description: 'metrics, logs, traces, … or incident',
  },
  {
    group: 'Where',
    snippet: '{{.Project.Name}}',
    label: '.Project.Name',
    description: 'The project',
  },
  {
    group: 'Where',
    snippet: '{{.Site.Name}}',
    label: '.Site.Name',
    description: 'The Site (empty on a standalone install)',
  },
  {
    group: 'Where',
    snippet: '{{.Link}}',
    label: '.Link',
    description: 'Opens the alert or incident in Obzly (empty when no address is set)',
  },
  {
    group: 'Incident',
    snippet: '{{if .Incident}}…{{end}}',
    label: 'if .Incident',
    description: 'Incident notifications only; wrap every .Incident field in it',
  },
  {
    group: 'Incident',
    snippet: '{{join ", " .Incident.Services}}',
    label: '.Incident.Services',
    description: 'Services involved',
  },
  {
    group: 'Incident',
    snippet: '{{.Incident.SignalCount}}',
    label: '.Incident.SignalCount',
    description: 'Error signals (.ErrorLogs + .ErrorSpans)',
  },
  {
    group: 'Incident',
    snippet: '{{range .Incident.TopServices}}{{.Name}}: {{.Total}}\n{{end}}',
    label: 'range .Incident.TopServices',
    description: 'Top impacted services with .ErrorLogs, .ErrorSpans, .Total',
  },
  {
    group: 'Helpers',
    snippet: '{{truncate 80 .Summary}}',
    label: 'truncate',
    description: 'Cut to n characters',
  },
  {
    group: 'Helpers',
    snippet: '{{default "n/a" .Summary}}',
    label: 'default',
    description: 'A fallback for an empty value',
  },
  {
    group: 'Helpers',
    snippet: '{{lower .Severity}}',
    label: 'upper / lower / title / trim',
    description: 'Change case, trim spaces',
  },
];

/** Inserts snippet at [start, end) of text; returns the new text and the cursor after it. */
export const insertSnippet = (
  text: string,
  start: number,
  end: number,
  snippet: string,
): { text: string; cursor: number } => {
  const from = Math.max(0, Math.min(start, text.length));
  const to = Math.max(from, Math.min(end, text.length));
  return { text: text.slice(0, from) + snippet + text.slice(to), cursor: from + snippet.length };
};

/** "Body, line 3: unexpected …" for the error list. */
export const formatTemplateError = (error: TemplateError): string => {
  const field = error.field === 'subject' ? 'Subject' : 'Body';
  return error.line > 0
    ? `${field}, line ${error.line}: ${error.message}`
    : `${field}: ${error.message}`;
};

/** The 1-based lines of a field that have errors, for highlighting. */
export const errorLines = (errors: TemplateError[], field: TemplateError['field']): number[] =>
  [...new Set(errors.filter((e) => e.field === field && e.line > 0).map((e) => e.line))].sort(
    (a, b) => a - b,
  );

/** Whether the editor's template differs from what is stored. */
export const isTemplateDirty = (stored: MessageTemplate, draft: MessageTemplate): boolean =>
  stored.subject !== draft.subject || stored.body !== draft.body;

/** The kind's editable parts only (email has a subject, the others don't). */
export const templateFor = (kind: ChannelKind, template: MessageTemplate): MessageTemplate => ({
  subject: kind === 'email' ? template.subject : '',
  body: template.body,
});
