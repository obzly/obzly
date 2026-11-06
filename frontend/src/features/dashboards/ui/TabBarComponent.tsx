import { CloseOutlined, PlusOutlined } from '@ant-design/icons';
import { Input, Tabs } from 'antd';
import React, { useState } from 'react';

import type { Tab } from '../../../entity/dashboards';

interface Props {
  tabs: Tab[];
  activeTabId: string;
  isEditing: boolean;
  onChangeActiveTab: (tabId: string) => void;
  onAddTab: () => void;
  onRenameTab: (tabId: string, title: string) => void;
  onDeleteTab: (tabId: string) => void;
}

// A dashboard always has at least one tab (backend normalizeTabs), so delete
// is disabled on the last one.
export const TabBarComponent = ({
  tabs,
  activeTabId,
  isEditing,
  onChangeActiveTab,
  onAddTab,
  onRenameTab,
  onDeleteTab,
}: Props): React.JSX.Element | null => {
  const [renamingTabId, setRenamingTabId] = useState<string | null>(null);

  // A single tab outside edit mode needs no tab bar.
  if (tabs.length <= 1 && !isEditing) return null;

  return (
    <Tabs
      className="dashboard-tab-bar"
      activeKey={activeTabId}
      onChange={onChangeActiveTab}
      type={isEditing ? 'editable-card' : 'card'}
      hideAdd={!isEditing}
      onEdit={(targetKey, action) => {
        if (action === 'add') onAddTab();
        if (action === 'remove' && typeof targetKey === 'string') onDeleteTab(targetKey);
      }}
      addIcon={<PlusOutlined />}
      items={tabs.map((tab) => ({
        key: tab.id,
        closable: isEditing && tabs.length > 1,
        closeIcon: <CloseOutlined />,
        label:
          isEditing && renamingTabId === tab.id ? (
            <Input
              size="small"
              autoFocus
              defaultValue={tab.title}
              className="w-32"
              onClick={(e) => e.stopPropagation()}
              onBlur={(e) => {
                onRenameTab(tab.id, e.target.value.trim() || tab.title);
                setRenamingTabId(null);
              }}
              onPressEnter={(e) => e.currentTarget.blur()}
            />
          ) : (
            <span
              onDoubleClick={() => isEditing && setRenamingTabId(tab.id)}
              title={isEditing ? 'Double-click to rename' : undefined}
            >
              {tab.title}
            </span>
          ),
      }))}
    />
  );
};
