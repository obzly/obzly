// The message template editor shared by Settings › Message templates and a
// channel's own template: variable picker, server-rendered live preview
// against sample notifications, errors with line numbers.
import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { Alert, Input, Segmented, Select, Spin } from 'antd';
import type { InputRef } from 'antd';
import type { TextAreaRef } from 'antd/es/input/TextArea';
import { useEffect, useRef, useState } from 'react';

import {
  type ChannelKind,
  type MessageTemplate,
  type TemplateError,
  type TemplatePreviewSample,
  notificationSettingsApi,
} from '../../../entity/alerting';
import {
  BODY_LABEL,
  TEMPLATE_VARIABLES,
  errorLines,
  formatTemplateError,
  insertSnippet,
} from '../lib/messageTemplates';
import { errorMessage } from './lib';

interface Props {
  kind: ChannelKind;
  value: MessageTemplate;
  onChange: (value: MessageTemplate) => void;
  /** Preview with the emoji (the channel's "Use emoji"). */
  useEmoji: boolean;
  readOnly?: boolean;
}

const PREVIEW_DELAY_MS = 400;

// What "Custom template" starts from for a webhook.
const WEBHOOK_STARTER = `{
  "status": "{{.Status}}",
  "severity": "{{.SeverityKey}}",
  "title": "{{.Emoji}} {{.Rule.Name}}",
  "summary": "{{.Summary}}",
  "link": "{{.Link}}"
}`;

const variableOptions = (['Alert', 'Rule', 'Where', 'Incident', 'Helpers'] as const).map(
  (group) => ({
    label: group,
    options: TEMPLATE_VARIABLES.filter((variable) => variable.group === group).map((variable) => ({
      value: variable.snippet,
      label: (
        <span className="flex flex-col">
          <span className="font-mono text-xs">{variable.label}</span>
          <span className="text-[11px] text-gray-500 dark:text-gray-400">
            {variable.description}
          </span>
        </span>
      ),
    })),
  }),
);

export const TemplateEditorComponent = ({ kind, value, onChange, useEmoji, readOnly }: Props) => {
  const [samples, setSamples] = useState<TemplatePreviewSample[]>([]);
  const [errors, setErrors] = useState<TemplateError[]>([]);
  const [previewError, setPreviewError] = useState<string>();
  const [isPreviewing, setIsPreviewing] = useState(false);
  const [sampleKey, setSampleKey] = useState('firing-critical');
  const [focused, setFocused] = useState<'subject' | 'body'>('body');
  const subjectRef = useRef<InputRef>(null);
  const bodyRef = useRef<TextAreaRef>(null);
  const requestId = useRef(0);

  const builtInWebhook = kind === 'webhook' && value.body.trim() === '';

  useEffect(() => {
    const id = ++requestId.current;
    const timer = window.setTimeout(async () => {
      setIsPreviewing(true);
      try {
        const response = await notificationSettingsApi.previewTemplate({
          kind,
          subject: value.subject,
          body: value.body,
          useEmoji,
        });
        if (id !== requestId.current) return;
        setSamples(response.samples);
        setErrors(response.errors);
        setPreviewError(undefined);
      } catch (error) {
        if (id === requestId.current) setPreviewError(errorMessage(error, 'Preview failed'));
      } finally {
        if (id === requestId.current) setIsPreviewing(false);
      }
    }, PREVIEW_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [kind, value.subject, value.body, useEmoji]);

  const insert = (snippet: string) => {
    if (readOnly) return;
    if (focused === 'subject' && kind === 'email') {
      const input = subjectRef.current?.input;
      const start = input?.selectionStart ?? value.subject.length;
      const end = input?.selectionEnd ?? start;
      const next = insertSnippet(value.subject, start, end, snippet.replace(/\n/g, ' '));
      onChange({ ...value, subject: next.text });
      return;
    }
    const area = bodyRef.current?.resizableTextArea?.textArea;
    const start = area?.selectionStart ?? value.body.length;
    const end = area?.selectionEnd ?? start;
    const next = insertSnippet(value.body, start, end, snippet);
    onChange({ ...value, body: next.text });
    window.requestAnimationFrame(() => {
      area?.focus();
      area?.setSelectionRange(next.cursor, next.cursor);
    });
  };

  const sample = samples.find((item) => item.key === sampleKey) ?? samples[0];
  const bodyErrorLines = errorLines(errors, 'body');

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="flex min-w-0 flex-col gap-3">
        {kind === 'webhook' && (
          <Segmented
            className="self-start"
            value={builtInWebhook ? 'builtin' : 'custom'}
            disabled={readOnly}
            options={[
              { value: 'builtin', label: 'Built-in JSON' },
              { value: 'custom', label: 'Custom template' },
            ]}
            onChange={(mode) =>
              onChange({ subject: '', body: mode === 'builtin' ? '' : WEBHOOK_STARTER })
            }
          />
        )}
        {!readOnly && !builtInWebhook && (
          <Select
            className="w-full sm:w-80"
            showSearch
            value={null}
            placeholder={
              <span>
                <PlusOutlined /> Insert a variable
              </span>
            }
            aria-label="Insert a variable"
            options={variableOptions}
            optionFilterProp="value"
            popupMatchSelectWidth={360}
            onChange={(snippet: string) => insert(snippet)}
          />
        )}
        {kind === 'email' && (
          <div className="flex flex-col gap-1">
            <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">
              Subject
            </span>
            <Input
              ref={subjectRef}
              className="font-mono"
              value={value.subject}
              readOnly={readOnly}
              aria-label="Subject"
              status={errors.some((e) => e.field === 'subject') ? 'error' : undefined}
              onFocus={() => setFocused('subject')}
              onChange={(event) => onChange({ ...value, subject: event.target.value })}
            />
          </div>
        )}
        {builtInWebhook ? (
          <div className="text-xs text-gray-500 dark:text-gray-400">
            Sends Obzly&apos;s fixed JSON (version obzly.alert/1). Choose Custom template to shape
            your own; it must render valid JSON, and every value is JSON-escaped.
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">
              {BODY_LABEL[kind]}
            </span>
            <Input.TextArea
              ref={bodyRef}
              className="font-mono !text-xs"
              autoSize={{ minRows: 8, maxRows: 22 }}
              value={value.body}
              readOnly={readOnly}
              aria-label={BODY_LABEL[kind]}
              status={bodyErrorLines.length ? 'error' : undefined}
              spellCheck={false}
              onFocus={() => setFocused('body')}
              onChange={(event) => onChange({ ...value, body: event.target.value })}
            />
          </div>
        )}
        {errors.length > 0 && (
          <Alert
            type="error"
            showIcon
            title={
              errors.length === 1 ? 'This template has a problem' : 'This template has problems'
            }
            description={
              <ul className="m-0 list-none p-0 font-mono text-xs">
                {errors.slice(0, 8).map((error, index) => (
                  <li key={index}>{formatTemplateError(error)}</li>
                ))}
              </ul>
            }
          />
        )}
        <div className="text-xs text-gray-500 dark:text-gray-400">
          Go text/template syntax. Every value is escaped for the channel (Slack control characters,
          JSON for webhooks), so labels from telemetry can&apos;t add mentions or links. Wrap
          incident fields in {'{{if .Incident}}…{{end}}'}.
        </div>
      </div>

      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-gray-700 dark:text-gray-200">Preview</span>
          {isPreviewing && <Spin size="small" indicator={<LoadingOutlined spin />} />}
        </div>
        <Select
          className="w-full sm:w-72"
          value={sample?.key}
          aria-label="Sample notification"
          options={samples.map((item) => ({
            value: item.key,
            label: `${item.emoji ? `${item.emoji} ` : ''}${item.label}`,
          }))}
          onChange={setSampleKey}
        />
        {previewError && <Alert type="warning" showIcon title={previewError} />}
        {sample && <PreviewPane kind={kind} sample={sample} />}
      </div>
    </div>
  );
};

const PreviewPane = ({ kind, sample }: { kind: ChannelKind; sample: TemplatePreviewSample }) => {
  if (sample.error) {
    return <Alert type="error" showIcon title="Doesn't render" description={sample.error} />;
  }
  const text = kind === 'webhook' ? sample.body : sample.text;
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-xl border border-[var(--v2-border)] bg-[var(--v2-panel2)] p-3">
      {kind === 'email' && (
        <div className="text-sm font-semibold break-words text-gray-900 dark:text-gray-50">
          {sample.subject}
        </div>
      )}
      <pre
        className={`m-0 max-h-[420px] overflow-auto font-mono text-xs break-words whitespace-pre-wrap text-gray-800 dark:text-gray-100 ${
          kind === 'slack' ? 'border-l-4 pl-3' : ''
        }`}
        // The Slack side bar's colour comes from the server's fixed status colours.
        style={kind === 'slack' && sample.color ? { borderLeftColor: sample.color } : undefined}
      >
        {text}
      </pre>
    </div>
  );
};
