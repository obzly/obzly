import { Tabs } from 'antd';

import { ActiveAlertsComponent } from './ActiveAlertsComponent';
import { ChannelsComponent } from './ChannelsComponent';
import { RulesComponent } from './RulesComponent';
import { SilencesComponent } from './SilencesComponent';

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
}

export const AlertsComponent = ({
  projectId,
  contentHeight,
  canManageRules,
  canManageChannels,
  canSilence,
}: Props) => (
  <div className="flex grow pl-3">
    <div
      className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <h1 className="mb-2 text-2xl font-bold text-gray-900 dark:text-gray-100">Alerts</h1>
      <Tabs
        destroyInactiveTabPane
        items={[
          {
            key: 'alerts',
            label: 'Active',
            children: <ActiveAlertsComponent projectId={projectId} />,
          },
          {
            key: 'rules',
            label: 'Rules',
            children: <RulesComponent projectId={projectId} canEdit={canManageRules} />,
          },
          {
            key: 'channels',
            label: 'Channels',
            children: <ChannelsComponent projectId={projectId} canEdit={canManageChannels} />,
          },
          {
            key: 'silences',
            label: 'Silences',
            children: <SilencesComponent projectId={projectId} canEdit={canSilence} />,
          },
        ]}
      />
    </div>
  </div>
);
