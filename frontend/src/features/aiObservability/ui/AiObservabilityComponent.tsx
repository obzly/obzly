import React, { useState } from 'react';

import { PageHeader } from '../../../shared/ui/v2';
import { BudgetsComponent } from './BudgetsComponent';
import { ClaudeCodeUsageComponent } from './ClaudeCodeUsageComponent';
import { LlmAppsComponent } from './LlmAppsComponent';
import { ModelPricesComponent } from './ModelPricesComponent';
import { PillTabs } from './bits';

interface Props {
  projectId: string;
  contentHeight: number;
  onOpenDataSources: () => void;
  onOpenTrace: (traceId: string, at: string) => void;
  /** Project owners and admins manage budgets and model prices. */
  canManage: boolean;
}

type SectionKey = 'claude-code' | 'llm-apps' | 'budgets' | 'settings';

const SECTION_TABS: { key: SectionKey; label: string }[] = [
  { key: 'claude-code', label: 'Claude Code' },
  { key: 'llm-apps', label: 'LLM apps' },
  { key: 'budgets', label: 'Budgets' },
  { key: 'settings', label: 'Settings' },
];

const SECTIONS: SectionKey[] = SECTION_TABS.map((tab) => tab.key);

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

  const changeSection = (key: SectionKey) => {
    setSection(key);
    try {
      localStorage.setItem(SECTION_STORAGE_KEY, key);
    } catch {
      // Storage unavailable (private mode); the section just isn't remembered.
    }
  };

  return (
    <div className="flex grow">
      <div className="w-full overflow-y-auto" style={{ height: contentHeight }}>
        <div className="flex max-w-[1400px] flex-col gap-5 px-1 pt-2 pb-8">
          <PageHeader
            title="AI Observability"
            subtitle="Claude Code and LLM apps for this project: spend, tokens, sessions and failures"
            actions={
              <PillTabs
                items={SECTION_TABS}
                active={section}
                onChange={changeSection}
                label="AI Observability views"
              />
            }
          />
          {/* Only the open section is mounted, as before (destroyOnHidden). */}
          {section === 'claude-code' && (
            <ClaudeCodeUsageComponent projectId={projectId} onOpenDataSources={onOpenDataSources} />
          )}
          {section === 'llm-apps' && (
            <LlmAppsComponent
              projectId={projectId}
              onOpenDataSources={onOpenDataSources}
              onOpenTrace={onOpenTrace}
              onOpenPrices={() => changeSection('settings')}
            />
          )}
          {section === 'budgets' && (
            <BudgetsComponent projectId={projectId} canManage={canManage} />
          )}
          {section === 'settings' && (
            <ModelPricesComponent projectId={projectId} canManage={canManage} />
          )}
        </div>
      </div>
    </div>
  );
};
