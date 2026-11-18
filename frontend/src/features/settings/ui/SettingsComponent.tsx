// On a control plane, Sites lives here instead of the sidebar.
import {
  AppstoreOutlined,
  BellOutlined,
  CloudOutlined,
  CloudSyncOutlined,
  ClusterOutlined,
  FileSearchOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
} from '@ant-design/icons';
import { type ReactNode, useState } from 'react';

import { IS_CLOUD, IS_CONTROL_PLANE, SITE_NAME, getApplicationServer } from '../../../constants';
import { PageHeader } from '../../../shared/ui/v2';
import { NotificationSettingsComponent } from '../../alerts';
import { CloudConnectionsComponent } from '../../infrastructure';
import { SitesComponent } from '../../sites';
import { type SettingsTab, TAB_LABEL, readSettingsTab, rememberSettingsTab } from '../lib/tabs';
import { AuditLogsComponent } from './AuditLogsComponent';
import { GeneralSettingsComponent } from './GeneralSettingsComponent';
import { IdentityAccessComponent } from './IdentityAccessComponent';
import { SettingsOverviewComponent } from './SettingsOverviewComponent';
import { UpdatesComponent } from './UpdatesComponent';

interface Props {
  contentHeight: number;
}

const TAB_ICON: Record<SettingsTab, ReactNode> = {
  overview: <AppstoreOutlined />,
  general: <SettingOutlined />,
  sites: <ClusterOutlined />,
  cloud: <CloudOutlined />,
  notifications: <BellOutlined />,
  identity: <SafetyCertificateOutlined />,
  updates: <CloudSyncOutlined />,
  audit: <FileSearchOutlined />,
};

export function SettingsComponent({ contentHeight }: Props) {
  const [tab, setTab] = useState<SettingsTab>(readSettingsTab);
  const [focusSiteId, setFocusSiteId] = useState<string>();

  const open = (next: SettingsTab, siteId?: string) => {
    setTab(next);
    setFocusSiteId(siteId);
    rememberSettingsTab(next);
  };

  const host = (() => {
    try {
      return new URL(getApplicationServer() || window.location.origin).host;
    } catch {
      return window.location.host;
    }
  })();
  const role = IS_CONTROL_PLANE ? 'control plane' : SITE_NAME ? `Site ${SITE_NAME}` : 'standalone';

  const panes: { key: SettingsTab; render: () => ReactNode }[] = [
    { key: 'overview', render: () => <SettingsOverviewComponent onNavigate={open} /> },
    { key: 'general', render: () => <GeneralSettingsComponent /> },
    ...(IS_CONTROL_PLANE
      ? [
          { key: 'sites' as const, render: () => <SitesComponent focusSiteId={focusSiteId} /> },
          // Shared by every use of a connection (inventory, cost, posture).
          { key: 'cloud' as const, render: () => <CloudConnectionsComponent canManage /> },
        ]
      : []),
    { key: 'notifications', render: () => <NotificationSettingsComponent /> },
    { key: 'identity', render: () => <IdentityAccessComponent /> },
    { key: 'updates', render: () => <UpdatesComponent /> },
    { key: 'audit', render: () => <AuditLogsComponent /> },
  ];
  // Only the active section is mounted, like the tabs it replaced.
  const active = panes.find((pane) => pane.key === tab) ?? panes[0];

  return (
    <div className="flex grow">
      <div
        className="w-full grow overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <div className="flex flex-col gap-5">
          <PageHeader
            title="Obzly settings"
            subtitle={
              <>
                {role} · {host}
                {IS_CLOUD && ' · Obzly Cloud'}
              </>
            }
          />
          <div className="flex flex-wrap items-start gap-4">
            <nav
              aria-label="Settings sections"
              className="flex max-w-full flex-[1_1_200px] flex-col gap-0.5 rounded-2xl border border-gray-200 bg-white p-2.5 lg:max-w-[240px] dark:border-gray-700 dark:bg-gray-800"
            >
              {panes.map((pane) => {
                const selected = pane.key === active.key;
                return (
                  <button
                    key={pane.key}
                    type="button"
                    aria-current={selected ? 'page' : undefined}
                    onClick={() => open(pane.key)}
                    className={`flex cursor-pointer items-center gap-2.5 rounded-[10px] border-0 px-3 py-2.5 text-left text-sm ${
                      selected
                        ? 'bg-[var(--v2-ok-soft)] font-semibold text-[var(--v2-accent)]'
                        : 'bg-transparent text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-700/40'
                    }`}
                  >
                    {TAB_ICON[pane.key]}
                    <span>{TAB_LABEL[pane.key]}</span>
                  </button>
                );
              })}
            </nav>
            <div className="flex min-w-0 flex-[999_1_640px] flex-col gap-4">{active.render()}</div>
          </div>
        </div>
      </div>
    </div>
  );
}
