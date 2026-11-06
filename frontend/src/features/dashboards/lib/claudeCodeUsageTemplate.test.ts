import { describe, expect, it } from 'vitest';

import { claudeCodeUsageTabs, claudeCodeUsageVariables } from './claudeCodeUsageTemplate';
import { dashboardTemplates } from './dashboardTemplates';
import { applyVariablesToPanel } from './dashboardVariables';

describe('Claude Code usage template', () => {
  const panels = claudeCodeUsageTabs().flatMap((tab) => tab.panels);

  it('is offered under New from template', () => {
    expect(dashboardTemplates.some((template) => template.key === 'claude-code-usage')).toBe(true);
  });

  it('only references its own variables, and every one resolves', () => {
    const keys = new Set(claudeCodeUsageVariables.map((variable) => variable.key));
    for (const panel of panels) {
      for (const [, key] of panel.promQuery!.matchAll(/\$\{(\w+)\}/g)) {
        expect(keys.has(key)).toBe(true);
      }
      const applied = applyVariablesToPanel(panel, { user: 'a.rivera@example.com' });
      expect(applied.promQuery).not.toContain('${');
      expect(applied.promQuery).toContain('user_email=~"a.rivera@example.com"');
    }
  });

  it('uses the normalized Claude Code metric names', () => {
    const queries = panels.map((panel) => panel.promQuery).join('\n');
    for (const metric of [
      'claude_code_cost_usage',
      'claude_code_token_usage',
      'claude_code_session_count',
      'claude_code_active_time_total',
      'claude_code_lines_of_code_count',
      'claude_code_commit_count',
      'claude_code_pull_request_count',
      'claude_code_code_edit_tool_decision',
    ]) {
      expect(queries).toContain(metric);
    }
    expect(queries).not.toMatch(/claude_code\./);
  });
});
