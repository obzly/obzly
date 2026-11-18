import { useEffect, useState } from 'react';

import {
  type AlertsSummary,
  type Matcher,
  type RuleRequest,
  alertingApi,
} from '../../../entity/alerting';
import { useVisibleInterval } from '../../../shared/lib';
import { PageHeader } from '../../../shared/ui/v2';
import { summaryLine } from '../lib/activity';
import { ActiveAlertsComponent } from './ActiveAlertsComponent';
import { ChannelsComponent } from './ChannelsComponent';
import { RulesComponent } from './RulesComponent';
import { SilencesComponent } from './SilencesComponent';
import { PillTabs } from './bits';

interface Props {
  projectId: string;
  contentHeight: number;
  // Project owners and admins edit rules.
  canManageRules: boolean;
  // The project owner or a global admin manages channels (they hold
  // credentials).
  canManageChannels: boolean;
  // Members who can write create silences.
  canSilence: boolean;
  /** Opens Obzly settings on Notification channels (admins only; omit to hide the link). */
  onManageChannels?: () => void;
  /** Opens Rules › New rule with these values (Logs › Chart › Create alert). */
  initialRuleDraft?: Partial<RuleRequest> | null;
}

type TabKey = 'alerts' | 'rules' | 'channels' | 'silences';

const TABS: { key: TabKey; label: string }[] = [
  { key: 'alerts', label: 'Active' },
  { key: 'rules', label: 'Rules' },
  { key: 'channels', label: 'Channels' },
  { key: 'silences', label: 'Silences' },
];

export const AlertsComponent = ({
  projectId,
  contentHeight,
  canManageRules,
  canManageChannels,
  canSilence,
  onManageChannels,
  initialRuleDraft,
}: Props) => {
  const [tab, setTab] = useState<TabKey>(initialRuleDraft ? 'rules' : 'alerts');
  // Read once: the page mounts fresh each time it is opened.
  const [ruleDraft, setRuleDraft] = useState(initialRuleDraft ?? null);
  // Set by "Silence" on a firing card: the Silences tab opens its form with these.
  const [silenceFor, setSilenceFor] = useState<Matcher[] | null>(null);
  // The header's counts; null until loaded (or when they fail), then the line is left out.
  const [summary, setSummary] = useState<AlertsSummary | null>(null);

  const loadSummary = async () => {
    try {
      setSummary(await alertingApi.getSummary(projectId));
    } catch {
      setSummary(null);
    }
  };

  const changeTab = (next: TabKey) => {
    setSilenceFor(null);
    setRuleDraft(null);
    setTab(next);
    loadSummary();
  };

  useEffect(() => {
    loadSummary();
  }, [projectId]);
  // As often as the Active tab refreshes, so "N firing" keeps up with it.
  useVisibleInterval(loadSummary, 30_000);

  return (
    <div className="flex grow">
      <div className="w-full overflow-y-auto" style={{ height: contentHeight }}>
        <div className="flex max-w-[1400px] flex-col gap-5 px-1 pt-2 pb-8">
          <PageHeader
            title="Alerts"
            subtitle={
              <>
                {summary && (
                  <span className="block font-medium text-gray-700 dark:text-gray-200">
                    {summaryLine(summary)}
                  </span>
                )}
                What is firing, the rules that check it, where notifications go and what is muted
              </>
            }
            actions={
              <PillTabs items={TABS} active={tab} onChange={changeTab} label="Alert views" />
            }
          />
          {/* Only the open tab is mounted, so each one loads fresh when opened. */}
          {tab === 'alerts' && (
            <ActiveAlertsComponent
              projectId={projectId}
              onSilence={
                canSilence
                  ? (matchers) => {
                      setTab('silences');
                      setSilenceFor(matchers);
                    }
                  : undefined
              }
            />
          )}
          {tab === 'rules' && (
            <RulesComponent
              projectId={projectId}
              canEdit={canManageRules}
              onChanged={loadSummary}
              initialDraft={ruleDraft}
            />
          )}
          {tab === 'channels' && (
            <ChannelsComponent
              projectId={projectId}
              canEdit={canManageChannels}
              onManageChannels={onManageChannels}
            />
          )}
          {tab === 'silences' && (
            <SilencesComponent
              projectId={projectId}
              canEdit={canSilence}
              initialMatchers={silenceFor}
              onChanged={loadSummary}
            />
          )}
        </div>
      </div>
    </div>
  );
};
