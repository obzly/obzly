import { Tabs } from 'antd';
import React, { useState } from 'react';

import { BudgetsComponent } from './BudgetsComponent';
import { ClaudeCodeUsageComponent } from './ClaudeCodeUsageComponent';
import { LlmAppsComponent } from './LlmAppsComponent';
import { ModelPricesComponent } from './ModelPricesComponent';

interface Props {
  projectId: string;
  contentHeight: number;
  onOpenDataSources: () => void;
  onOpenTrace: (traceId: string, at: string) => void;
  /** Project owners and admins manage budgets and model prices. */
  canManage: boolean;
}

type SectionKey = 'claude-code' | 'llm-apps' | 'budgets' | 'settings';

const SECTIONS: SectionKey[] = ['claude-code', 'llm-apps', 'budgets', 'settings'];

const SECTION_STORAGE_KEY = 'ai_observability_section';

const readStoredSection = (): SectionKey => {
  try {
    const stored = localStorage.getItem(SECTION_STORAGE_KEY) as SectionKey | null;
    return stored && SECTIONS.includes(stored) ? stored : 'claude-code';
  } catch {
    return 'claude-code';
  }
};

export const AiObservabilityComponent = ({
  projectId,
  contentHeight,
  onOpenDataSources,
  onOpenTrace,
  canManage,
}: Props): React.JSX.Element => {
  const [section, setSection] = useState<SectionKey>(readStoredSection);

  const changeSection = (key: string) => {
    setSection(key as SectionKey);
    try {
      localStorage.setItem(SECTION_STORAGE_KEY, key);
    } catch {
      // Storage unavailable (private mode); the section just isn't remembered.
    }
  };

  return (
    <div className="overflow-y-auto p-4" style={{ height: contentHeight }}>
      <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">AI Observability</h2>
      <Tabs
        activeKey={section}
        onChange={changeSection}
        destroyOnHidden
        items={[
          {
            key: 'claude-code',
            label: 'Claude Code',
            children: (
              <ClaudeCodeUsageComponent
                projectId={projectId}
                onOpenDataSources={onOpenDataSources}
              />
            ),
          },
          {
            key: 'llm-apps',
            label: 'LLM apps',
            children: (
              <LlmAppsComponent
                projectId={projectId}
                onOpenDataSources={onOpenDataSources}
                onOpenTrace={onOpenTrace}
                onOpenPrices={() => changeSection('settings')}
              />
            ),
          },
          {
            key: 'budgets',
            label: 'Budgets',
            children: <BudgetsComponent projectId={projectId} canManage={canManage} />,
          },
          {
            key: 'settings',
            label: 'Settings',
            children: <ModelPricesComponent projectId={projectId} canManage={canManage} />,
          },
        ]}
      />
    </div>
  );
};
