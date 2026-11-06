// On a control plane, Sites lives here instead of the sidebar.
import { Tabs } from 'antd';
import { useState } from 'react';

import { IS_CLOUD, IS_CONTROL_PLANE, SITE_NAME, getApplicationServer } from '../../../constants';
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

  const panes: { key: SettingsTab; children: React.ReactNode }[] = [
    { key: 'overview', children: <SettingsOverviewComponent onNavigate={open} /> },
    { key: 'general', children: <GeneralSettingsComponent /> },
    ...(IS_CONTROL_PLANE
      ? [
          { key: 'sites' as const, children: <SitesComponent focusSiteId={focusSiteId} /> },
          // Shared by every use of a connection (inventory, cost, posture).
          { key: 'cloud' as const, children: <CloudConnectionsComponent canManage /> },
        ]
      : []),
    { key: 'notifications', children: <NotificationSettingsComponent /> },
    { key: 'identity', children: <IdentityAccessComponent /> },
    { key: 'updates', children: <UpdatesComponent /> },
    { key: 'audit', children: <AuditLogsComponent /> },
  ];

  return (
    <div className="flex grow pl-3">
      <div
        className="w-full grow overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <div className="mb-1 flex flex-wrap items-baseline gap-3">
          <h1 className="m-0 text-2xl font-bold">Obzly settings</h1>
          <span className="text-sm text-gray-500 dark:text-gray-400">
            {role} · {host}
            {IS_CLOUD && ' · Obzly Cloud'}
          </span>
        </div>
        <Tabs
          activeKey={tab}
          onChange={(key) => open(key as SettingsTab)}
          destroyOnHidden
          items={panes.map((pane) => ({
            key: pane.key,
            label: TAB_LABEL[pane.key],
            children: pane.children,
          }))}
        />
      </div>
    </div>
  );
}
