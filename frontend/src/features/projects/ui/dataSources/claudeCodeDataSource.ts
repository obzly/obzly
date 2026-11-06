import type { DataSourceDefinition } from './types';

// Claude Code exports OTLP itself, so no collector: env vars pointing it at Obzly. Cumulative
// temporality is kept so older Sites, which can't convert delta metrics, count correctly.
export const claudeCodeDataSource: DataSourceDefinition = {
  id: 'claude-code',
  name: 'Claude Code',
  category: 'AI Tools',
  description:
    "Claude Code's built-in telemetry: cost, tokens, sessions and tool usage per user and model.",
  fields: [
    {
      key: 'team',
      label: 'Team (optional)',
      defaultValue: '',
      placeholder: 'platform',
      helpText: 'Added as a resource attribute, to break usage down by team.',
    },
    {
      key: 'logPrompts',
      label: 'Include prompt text',
      defaultValue: 'no',
      type: 'checkbox',
      helpText: 'Off by default: user_prompt events carry only the prompt length.',
    },
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const team = fieldValues.team?.trim();
    const env: Record<string, string> = {
      CLAUDE_CODE_ENABLE_TELEMETRY: '1',
      OTEL_METRICS_EXPORTER: 'otlp',
      OTEL_LOGS_EXPORTER: 'otlp',
      OTEL_EXPORTER_OTLP_PROTOCOL: 'http/protobuf',
      OTEL_EXPORTER_OTLP_ENDPOINT: baseUrl,
      OTEL_EXPORTER_OTLP_HEADERS: `X-API-Key=${apiKey}`,
      OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE: 'cumulative',
    };
    if (team) env.OTEL_RESOURCE_ATTRIBUTES = `team=${team}`;
    if (fieldValues.logPrompts === 'yes') env.OTEL_LOG_USER_PROMPTS = '1';

    const shell = Object.entries(env)
      .map(([key, value]) => `export ${key}="${value}"`)
      .join('\n');

    return {
      filename: 'obzly-claude-code.env',
      language: 'bash',
      content: '',
      files: [
        {
          filename: 'obzly-claude-code.env',
          language: 'bash',
          content:
            '# One machine: add to your shell profile, then start a new claude session.\n' +
            '# The TEMPORALITY line is only needed for Obzly Sites older than v0.57.0.\n' +
            `${shell}\n`,
        },
        {
          filename: 'managed-settings.json',
          language: 'json',
          content: JSON.stringify({ env }, null, 2) + '\n',
        },
      ],
      instructions:
        'For one machine, source the env file before running claude. To roll out to a team, deploy ' +
        'managed-settings.json (/etc/claude-code/ on Linux, /Library/Application Support/ClaudeCode/ ' +
        'on macOS) - or merge its "env" block into ~/.claude/settings.json. Usage shows up on the ' +
        'AI Observability page; cost is the API-equivalent estimate, not a subscription charge.',
    };
  },
};
