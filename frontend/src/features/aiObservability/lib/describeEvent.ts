import { formatCount, formatMs, formatUsd } from './format';

const num = (fields: Record<string, string>, key: string): number => Number(fields[key] ?? 0);

export const describeEvent = (event: string, f: Record<string, string>): string => {
  switch (event) {
    case 'user_prompt':
      return `Prompt (${f.prompt_length ?? '?'} chars)`;
    case 'api_request': {
      const tokens =
        num(f, 'input_tokens') +
        num(f, 'output_tokens') +
        num(f, 'cache_read_tokens') +
        num(f, 'cache_creation_tokens');
      return `${f.model} · ${formatCount(tokens)} tokens · ${formatUsd(num(f, 'cost_usd_micros') / 1e6)} · ${formatMs(num(f, 'duration_ms'))}`;
    }
    case 'api_error':
      return `${f.model ?? ''} ${f.status_code ?? ''} ${f.error ?? ''}`.trim();
    case 'tool_decision':
      return `${f.tool_name}: ${f.decision} (${f.source ?? ''})`;
    case 'tool_result':
      return `${f.tool_name} ${f.success === 'true' ? 'succeeded' : 'failed'} in ${formatMs(num(f, 'duration_ms'))}`;
    case 'assistant_response':
      return `${f.model ?? 'Response'} · ${f.response_length ?? '?'} chars`;
    case 'plugin_loaded':
      return `Plugin ${f.plugin_name ?? ''} loaded`;
    case 'mcp_server_connection':
      return `MCP server ${f.status ?? ''} (${f.transport_type ?? ''}, ${formatMs(num(f, 'duration_ms'))})`;
    default:
      return event.replace(/_/g, ' ');
  }
};

export const colorFor = (event: string, f: Record<string, string>): string => {
  if (event === 'api_error' || (event === 'tool_result' && f.success === 'false')) return 'red';
  if (event === 'tool_decision' && f.decision === 'reject') return 'orange';
  if (event === 'user_prompt') return 'blue';
  if (event === 'api_request') return 'green';
  return 'gray';
};
