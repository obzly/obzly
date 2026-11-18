import { Alert, Button, Typography } from 'antd';
import type { JSX, ReactNode } from 'react';

import { Card } from '../../../shared/ui/v2';

interface Props {
  /** The Site answered 404 for the AI usage API: it runs an Obzly build older than this page. */
  isSiteOutdated: boolean;
  onOpenDataSources: () => void;
}

// Mirrors claudeCodeDataSource's env block, with placeholders: the Data
// sources wizard fills in the real Site URL and API key.
const ENV_SNIPPET = [
  'export CLAUDE_CODE_ENABLE_TELEMETRY="1"',
  'export OTEL_METRICS_EXPORTER="otlp"',
  'export OTEL_LOGS_EXPORTER="otlp"',
  'export OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"',
  'export OTEL_EXPORTER_OTLP_ENDPOINT="<your Obzly Site URL>"',
  'export OTEL_EXPORTER_OTLP_HEADERS="X-API-Key=<project API key>"',
  '# Optional from Obzly v0.57.0 (delta temporality is supported)',
  'export OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE="cumulative"',
].join('\n');

const Step = ({ n, title, children }: { n: number; title: string; children: ReactNode }) => (
  <li className="flex gap-3 border-t border-gray-200 pt-4 dark:border-gray-700">
    <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-[var(--v2-ok-soft)] text-[13px] font-semibold text-[var(--v2-accent)]">
      {n}
    </span>
    <div className="min-w-0 flex-1">
      <div className="text-sm font-semibold text-gray-900 dark:text-gray-50">{title}</div>
      <div className="mt-1 text-[13px] text-gray-600 dark:text-gray-300">{children}</div>
    </div>
  </li>
);

export const AiOnboardingPanel = ({ isSiteOutdated, onOpenDataSources }: Props): JSX.Element => (
  <div className="flex max-w-4xl flex-col gap-4">
    {isSiteOutdated && (
      <Alert
        type="warning"
        showIcon
        message="This Site doesn't support AI Observability yet"
        description="The Site answered “not found” for the AI usage API, so it runs an older Obzly version than the control plane. Upgrade the Site (the Sites page shows each Site's version), then onboard your agents as below."
      />
    )}

    <Card title="Start collecting AI usage">
      <p className="m-0 text-sm text-gray-600 dark:text-gray-300">
        {isSiteOutdated
          ? 'Once the Site is upgraded, onboard your agents to see usage here.'
          : 'No Claude Code telemetry in this project for the selected time range. If agents are already set up, try a wider range; otherwise onboard them below.'}{' '}
        Claude Code exports its own OpenTelemetry metrics and events, so no collector is needed:
        point it at this project and cost, tokens, sessions and tool usage appear here per user and
        model.
      </p>

      <ol className="mt-4 mb-0 flex list-none flex-col gap-4 p-0">
        <Step n={1} title="Get the settings for this project">
          Open <b>Data sources</b>, pick <b>Claude Code</b> under AI Tools, and enter the Site URL
          and an API key for this project. It generates a ready-to-use env file and a
          managed-settings.json.
          <div className="mt-2">
            <Button type="primary" size="small" onClick={onOpenDataSources}>
              Open Claude Code setup
            </Button>
          </div>
        </Step>

        <Step n={2} title="Enable telemetry on each machine">
          Add these to the shell profile (the wizard fills in the placeholders), then start a new{' '}
          <code>claude</code> session.
          <Typography.Paragraph
            copyable={{ text: ENV_SNIPPET }}
            className="!mt-2 !mb-0 overflow-x-auto rounded-lg bg-[var(--v2-panel2)] p-3 font-mono !text-xs whitespace-pre text-gray-800 dark:text-gray-200"
          >
            {ENV_SNIPPET}
          </Typography.Paragraph>
        </Step>

        <Step n={3} title="Or roll it out to a team">
          Deploy the generated <code>managed-settings.json</code> to <code>/etc/claude-code/</code>{' '}
          on Linux or <code>/Library/Application Support/ClaudeCode/</code> on macOS, or merge its{' '}
          <code>env</code> block into <code>~/.claude/settings.json</code>.
        </Step>

        <Step n={4} title="Check it's arriving">
          Events show up within seconds and usage metrics within about a minute of the first prompt.
          Use the refresh button above if this page is already open.
        </Step>
      </ol>

      <p className="m-0 mt-4 border-t border-gray-200 pt-3 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
        Cost is Claude Code&apos;s API-equivalent estimate; on Pro, Max and Team plans it is not
        what you&apos;re billed. Prompt text is not collected unless you enable it in the wizard.
      </p>
    </Card>
  </div>
);
