import {
  AppstoreAddOutlined,
  CodeOutlined,
  DownloadOutlined,
  EditOutlined,
  FolderOutlined,
  FullscreenExitOutlined,
  FullscreenOutlined,
  FunctionOutlined,
  HomeOutlined,
  PlusOutlined,
  PrinterOutlined,
  PushpinFilled,
  PushpinOutlined,
  ReloadOutlined,
  SettingOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import {
  App,
  Breadcrumb,
  Button,
  Dropdown,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Spin,
  Tooltip,
} from 'antd';
import React, { useEffect, useRef, useState } from 'react';

import {
  AUTO_REFRESH_OPTIONS,
  type AutoRefreshIntervalMs,
  type Dashboard,
  type Folder,
  type Panel,
  type Tab,
  type Variable,
  dashboardApi,
} from '../../../entity/dashboards';
import { ProjectRole } from '../../../entity/users';
import { type TimeRange, TimeRangePickerComponent } from '../../query';
import { type DashboardFilters, EMPTY_FILTERS } from '../lib/dashboardFilters';
import { dashboardTemplates } from '../lib/dashboardTemplates';
import { DashboardFilterBarComponent } from './DashboardFilterBarComponent';
import { DashboardGridComponent } from './DashboardGridComponent';
import { FolderManagerModalComponent } from './FolderManagerModalComponent';
import { ImportDashboardModalComponent } from './ImportDashboardModalComponent';
import { PanelEditModalComponent } from './PanelEditModalComponent';
import { TabBarComponent } from './TabBarComponent';
import { VariableBarComponent } from './VariableBarComponent';
import { VariableEditorModalComponent } from './VariableEditorModalComponent';

const newTab = (title: string): Tab => ({ id: crypto.randomUUID(), title, panels: [] });

// "Unfiled only": a Select value can't otherwise be told apart from undefined
// (no filter).
const UNFILED = '__unfiled__';

interface Props {
  projectId: string;
  contentHeight: number;
  // undefined counts as "can write" (the backend lets every role but viewer
  // write), so controls don't vanish while the role loads.
  userRole?: ProjectRole;
}

export const DashboardsComponent = ({
  projectId,
  contentHeight,
  userRole,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const canWrite = userRole !== ProjectRole.VIEWER;

  const [dashboards, setDashboards] = useState<Dashboard[]>([]);
  const [selectedID, setSelectedID] = useState<string | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [isEditing, setIsEditing] = useState(false);
  const [editingPanel, setEditingPanel] = useState<Panel | null | 'new'>(null);
  const [showImportModal, setShowImportModal] = useState(false);
  const [timeRange, setTimeRange] = useState<TimeRange | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [filters, setFilters] = useState<DashboardFilters>(EMPTY_FILTERS);
  const [autoRefreshMs, setAutoRefreshMs] = useState<AutoRefreshIntervalMs>(0);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [showJsonModal, setShowJsonModal] = useState(false);
  const [showVariableEditor, setShowVariableEditor] = useState(false);
  const [variableValues, setVariableValues] = useState<Record<string, string>>({});
  const [activeTabId, setActiveTabId] = useState<string | undefined>(undefined);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [currentFolderId, setCurrentFolderId] = useState<string | undefined>(undefined);
  const [showFolderManager, setShowFolderManager] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const selected = dashboards.find((d) => d.id === selectedID);
  const activeTab = selected?.tabs.find((t) => t.id === activeTabId) ?? selected?.tabs[0];
  const currentFolder = folders.find((f) => f.id === currentFolderId);

  const visibleDashboards =
    currentFolderId === undefined
      ? dashboards
      : currentFolderId === UNFILED
        ? dashboards.filter((d) => !d.folderId)
        : dashboards.filter((d) => d.folderId === currentFolderId);

  // Reset variables and tab when the selected dashboard changes.
  useEffect(() => {
    const defaults: Record<string, string> = {};
    for (const variable of selected?.variables ?? []) {
      if (variable.defaultValue) defaults[variable.key] = variable.defaultValue;
    }
    setVariableValues(defaults);
    setActiveTabId(selected?.tabs[0]?.id);
  }, [selectedID]);

  useEffect(() => {
    if (autoRefreshMs === 0) return;
    const interval = setInterval(() => setRefreshToken((v) => v + 1), autoRefreshMs);
    return () => clearInterval(interval);
  }, [autoRefreshMs]);

  // Esc exits fullscreen without the toolbar button, so follow the browser's state.
  useEffect(() => {
    const onChange = () => setIsFullscreen(document.fullscreenElement === containerRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      containerRef.current?.requestFullscreen();
    }
  };

  const handleTogglePinned = async (dashboard: Dashboard) => {
    const nextPinned = !dashboard.isPinned;
    setDashboards((prev) =>
      prev.map((d) => (d.id === dashboard.id ? { ...d, isPinned: nextPinned } : d)),
    );
    try {
      await dashboardApi.setPinned(projectId, dashboard.id, nextPinned);
      loadDashboards(dashboard.id);
    } catch (e) {
      message.error((e as Error).message);
      loadDashboards(dashboard.id);
    }
  };

  const loadDashboards = async (selectIDAfterLoad?: string) => {
    setIsLoading(true);
    try {
      const response = await dashboardApi.list(projectId);
      setDashboards(response.dashboards);

      const nextSelected =
        response.dashboards.find((d) => d.id === selectIDAfterLoad) ??
        response.dashboards.find((d) => d.id === selectedID) ??
        response.dashboards[0];
      setSelectedID(nextSelected?.id);
    } catch (e) {
      message.error((e as Error).message);
    } finally {
      setIsLoading(false);
    }
  };

  const loadFolders = async () => {
    try {
      const response = await dashboardApi.listFolders(projectId);
      setFolders(response.folders);
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  useEffect(() => {
    loadDashboards();
    loadFolders();
    setIsEditing(false);
    setCurrentFolderId(undefined);
  }, [projectId]);

  const handleSetFolder = async (dashboard: Dashboard, folderId: string | undefined) => {
    setDashboards((prev) => prev.map((d) => (d.id === dashboard.id ? { ...d, folderId } : d)));
    try {
      await dashboardApi.setFolder(projectId, dashboard.id, folderId ?? null);
    } catch (e) {
      message.error((e as Error).message);
      loadDashboards();
    }
  };

  const persistTabs = async (dashboard: Dashboard, tabs: Tab[]) => {
    setDashboards((prev) => prev.map((d) => (d.id === dashboard.id ? { ...d, tabs } : d)));
    try {
      await dashboardApi.update(projectId, dashboard.id, {
        title: dashboard.title,
        description: dashboard.description,
        tabs,
        variables: dashboard.variables,
      });
    } catch (e) {
      message.error((e as Error).message);
      loadDashboards();
    }
  };

  const persistTabPanels = (dashboard: Dashboard, tabId: string, panels: Panel[]) =>
    persistTabs(
      dashboard,
      dashboard.tabs.map((t) => (t.id === tabId ? { ...t, panels } : t)),
    );

  const handleAddTab = () => {
    if (!selected) return;
    const tab = newTab(`Tab ${selected.tabs.length + 1}`);
    setActiveTabId(tab.id);
    persistTabs(selected, [...selected.tabs, tab]);
  };

  const handleRenameTab = (tabId: string, title: string) => {
    if (!selected) return;
    persistTabs(
      selected,
      selected.tabs.map((t) => (t.id === tabId ? { ...t, title } : t)),
    );
  };

  const handleDeleteTab = (tabId: string) => {
    if (!selected || selected.tabs.length <= 1) return;
    const remaining = selected.tabs.filter((t) => t.id !== tabId);
    if (activeTabId === tabId) setActiveTabId(remaining[0]?.id);
    persistTabs(selected, remaining);
  };

  const handleSaveVariables = async (variables: Variable[]) => {
    if (!selected) return;
    setShowVariableEditor(false);
    setDashboards((prev) => prev.map((d) => (d.id === selected.id ? { ...d, variables } : d)));
    try {
      await dashboardApi.update(projectId, selected.id, {
        title: selected.title,
        description: selected.description,
        tabs: selected.tabs,
        variables,
      });
      message.success('Variables saved');
    } catch (e) {
      message.error((e as Error).message);
      loadDashboards();
    }
  };

  const handleCreateDashboard = async () => {
    try {
      const dashboard = await dashboardApi.create(projectId, { title: 'New dashboard' });
      await loadDashboards(dashboard.id);
      setIsEditing(true);
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleGenerateOverview = async () => {
    try {
      const dashboard = await dashboardApi.generateOverview(projectId);
      await loadDashboards(dashboard.id);
      message.success('Overview dashboard generated');
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleCreateFromTemplate = async (templateKey: string) => {
    const template = dashboardTemplates.find((t) => t.key === templateKey);
    if (!template) return;
    try {
      const dashboard = await dashboardApi.create(projectId, {
        title: template.title,
        description: template.description,
        tabs: template.buildTabs(),
        variables: template.variables,
      });
      await loadDashboards(dashboard.id);
      message.success(`"${template.title}" dashboard created`);
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleDeleteDashboard = async () => {
    if (!selected) return;
    try {
      await dashboardApi.remove(projectId, selected.id);
      setIsEditing(false);
      await loadDashboards();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleRenameDashboard = async (title: string) => {
    if (!selected || !title.trim() || title === selected.title) return;
    const updatedDashboard = { ...selected, title };
    setDashboards((prev) => prev.map((d) => (d.id === selected.id ? updatedDashboard : d)));
    try {
      await dashboardApi.update(projectId, selected.id, {
        title,
        description: selected.description,
        tabs: selected.tabs,
        variables: selected.variables,
      });
    } catch (e) {
      message.error((e as Error).message);
      loadDashboards();
    }
  };

  const handleSavePanel = (panel: Panel) => {
    if (!selected || !activeTab) return;
    const exists = activeTab.panels.some((p) => p.id === panel.id);
    const panels = exists
      ? activeTab.panels.map((p) => (p.id === panel.id ? panel : p))
      : [...activeTab.panels, panel];
    setEditingPanel(null);
    persistTabPanels(selected, activeTab.id, panels);
  };

  const handleDeletePanel = (panelId: string) => {
    if (!selected || !activeTab) return;
    persistTabPanels(
      selected,
      activeTab.id,
      activeTab.panels.filter((p) => p.id !== panelId),
    );
  };

  const handleImported = async (dashboard: Dashboard) => {
    setShowImportModal(false);
    await loadDashboards(dashboard.id);
    message.success('Dashboard imported');
  };

  if (isLoading) {
    return (
      <div className="flex grow items-center justify-center" style={{ height: contentHeight }}>
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      className="grow overflow-auto rounded bg-white p-3 shadow dark:bg-gray-800"
      style={{ height: contentHeight }}
    >
      <Breadcrumb
        className="mb-1.5 text-xs"
        items={[
          {
            title: (
              <button
                type="button"
                className="flex items-center gap-1"
                onClick={() => setCurrentFolderId(undefined)}
              >
                <HomeOutlined /> Dashboards
              </button>
            ),
          },
          ...(currentFolder
            ? [{ title: currentFolder.title }]
            : currentFolderId === UNFILED
              ? [{ title: 'Unfiled' }]
              : []),
        ]}
      />

      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        {dashboards.length > 0 ? (
          <Select
            size="small"
            value={visibleDashboards.some((d) => d.id === selectedID) ? selectedID : undefined}
            onChange={(id) => {
              setSelectedID(id);
              setIsEditing(false);
              setFilters(EMPTY_FILTERS);
            }}
            className="w-56"
            placeholder="Dashboard"
            options={visibleDashboards.map((d) => ({
              value: d.id,
              label: `${d.isPinned ? '\u{1F4CC} ' : ''}${d.title}${d.isAutoGenerated ? ' (auto)' : ''}`,
            }))}
          />
        ) : (
          <span className="text-sm text-gray-500 dark:text-gray-300">No dashboards yet</span>
        )}

        <Select
          size="small"
          value={currentFolderId ?? ''}
          onChange={(v) => setCurrentFolderId(v === '' ? undefined : v)}
          className="w-32"
          options={[
            { value: '', label: 'All folders' },
            { value: UNFILED, label: 'Unfiled' },
            ...folders.map((f) => ({ value: f.id, label: f.title })),
          ]}
        />
        {canWrite && (
          <Tooltip title="Manage folders">
            <Button
              size="small"
              icon={<SettingOutlined />}
              onClick={() => setShowFolderManager(true)}
            />
          </Tooltip>
        )}

        <TimeRangePickerComponent compact onChange={setTimeRange} />

        <Tooltip title="Refresh">
          <Button
            size="small"
            icon={<ReloadOutlined />}
            onClick={() => setRefreshToken((v) => v + 1)}
          />
        </Tooltip>

        <Select
          size="small"
          value={autoRefreshMs}
          onChange={setAutoRefreshMs}
          className="w-20"
          title="Auto refresh"
          options={AUTO_REFRESH_OPTIONS}
        />

        {selected && canWrite && (
          <Tooltip title={selected.isPinned ? 'Unpin dashboard' : 'Pin dashboard'}>
            <Button
              size="small"
              icon={selected.isPinned ? <PushpinFilled /> : <PushpinOutlined />}
              onClick={() => handleTogglePinned(selected)}
            />
          </Tooltip>
        )}
        {selected && (
          <Tooltip title="View as JSON">
            <Button size="small" icon={<CodeOutlined />} onClick={() => setShowJsonModal(true)} />
          </Tooltip>
        )}
        <Tooltip title="Print">
          <Button size="small" icon={<PrinterOutlined />} onClick={() => window.print()} />
        </Tooltip>
        <Tooltip title={isFullscreen ? 'Exit fullscreen' : 'Fullscreen'}>
          <Button
            size="small"
            icon={isFullscreen ? <FullscreenExitOutlined /> : <FullscreenOutlined />}
            onClick={toggleFullscreen}
          />
        </Tooltip>

        {canWrite && (
          <>
            <Tooltip title="New dashboard">
              <Button size="small" icon={<PlusOutlined />} onClick={handleCreateDashboard} />
            </Tooltip>
            <Tooltip
              title={
                dashboards.some((d) => d.isAutoGenerated)
                  ? 'Regenerate overview'
                  : 'Generate overview'
              }
            >
              <Button
                size="small"
                icon={<ThunderboltOutlined />}
                onClick={handleGenerateOverview}
              />
            </Tooltip>
            <Dropdown
              menu={{
                items: dashboardTemplates.map((t) => ({
                  key: t.key,
                  label: t.title,
                  title: t.description,
                  onClick: () => handleCreateFromTemplate(t.key),
                })),
              }}
            >
              <Tooltip title="Dashboard Presets">
                <Button size="small" icon={<AppstoreAddOutlined />} />
              </Tooltip>
            </Dropdown>
            <Tooltip title="Import">
              <Button
                size="small"
                icon={<DownloadOutlined />}
                onClick={() => setShowImportModal(true)}
              />
            </Tooltip>
          </>
        )}
      </div>

      {!selected && (
        <Empty description="No dashboards yet - create a blank one, generate the auto Overview, or import an existing Grafana/OpenObserve dashboard." />
      )}

      {selected && timeRange && (
        <>
          <div className="mb-2 flex items-center gap-2">
            {isEditing ? (
              <Input
                size="small"
                value={selected.title}
                onChange={(e) =>
                  setDashboards((prev) =>
                    prev.map((d) => (d.id === selected.id ? { ...d, title: e.target.value } : d)),
                  )
                }
                onBlur={(e) => handleRenameDashboard(e.target.value)}
                className="w-72 text-base font-medium"
              />
            ) : (
              <h2 className="text-base font-medium text-gray-800 dark:text-gray-100">
                {selected.title}
              </h2>
            )}

            {canWrite && (
              <Button
                size="small"
                type={isEditing ? 'primary' : 'default'}
                icon={<EditOutlined />}
                onClick={() => setIsEditing((v) => !v)}
                className={
                  isEditing
                    ? 'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700'
                    : ''
                }
              >
                {isEditing ? 'Done editing' : 'Edit'}
              </Button>
            )}

            {isEditing && (
              <>
                <Button
                  size="small"
                  icon={<PlusOutlined />}
                  onClick={() => setEditingPanel('new')}
                  className="border-emerald-600 bg-emerald-600 text-white hover:border-emerald-700 hover:bg-emerald-700"
                >
                  Add panel
                </Button>
                <Button
                  size="small"
                  icon={<FunctionOutlined />}
                  onClick={() => setShowVariableEditor(true)}
                >
                  Variables
                </Button>
                <Select
                  size="small"
                  className="w-36"
                  prefix={<FolderOutlined />}
                  placeholder="Unfiled"
                  allowClear
                  value={selected.folderId}
                  onChange={(folderId) => handleSetFolder(selected, folderId)}
                  options={folders.map((f) => ({ value: f.id, label: f.title }))}
                />
                <Popconfirm title="Delete this dashboard?" onConfirm={handleDeleteDashboard}>
                  <Button size="small" danger>
                    Delete dashboard
                  </Button>
                </Popconfirm>
              </>
            )}
          </div>

          <VariableBarComponent
            projectId={projectId}
            variables={selected.variables}
            values={variableValues}
            onChange={setVariableValues}
          />

          <DashboardFilterBarComponent
            projectId={projectId}
            filters={filters}
            onChange={setFilters}
          />

          <TabBarComponent
            tabs={selected.tabs}
            activeTabId={activeTab?.id ?? ''}
            isEditing={isEditing}
            onChangeActiveTab={setActiveTabId}
            onAddTab={handleAddTab}
            onRenameTab={handleRenameTab}
            onDeleteTab={handleDeleteTab}
          />

          {activeTab && (
            <DashboardGridComponent
              projectId={projectId}
              panels={activeTab.panels}
              timeRange={timeRange}
              isEditing={isEditing}
              refreshToken={refreshToken}
              filters={filters}
              variableValues={variableValues}
              onLayoutChange={(panels) => persistTabPanels(selected, activeTab.id, panels)}
              onEditPanel={(panel) => setEditingPanel(panel)}
              onDeletePanel={handleDeletePanel}
            />
          )}
        </>
      )}

      {showVariableEditor && selected && (
        <VariableEditorModalComponent
          variables={selected.variables}
          onSave={handleSaveVariables}
          onCancel={() => setShowVariableEditor(false)}
        />
      )}

      {editingPanel && (
        <PanelEditModalComponent
          projectId={projectId}
          panel={editingPanel === 'new' ? null : editingPanel}
          onSave={handleSavePanel}
          onCancel={() => setEditingPanel(null)}
        />
      )}

      {showImportModal && (
        <ImportDashboardModalComponent
          projectId={projectId}
          onImported={handleImported}
          onCancel={() => setShowImportModal(false)}
        />
      )}

      {showFolderManager && (
        <FolderManagerModalComponent
          projectId={projectId}
          folders={folders}
          onChange={loadFolders}
          onCancel={() => setShowFolderManager(false)}
        />
      )}

      {showJsonModal && selected && (
        <Modal
          title={`${selected.title} - JSON`}
          open
          onCancel={() => setShowJsonModal(false)}
          footer={
            <Button
              onClick={() => {
                navigator.clipboard.writeText(JSON.stringify(selected, null, 2));
                message.success('Copied to clipboard');
              }}
            >
              Copy
            </Button>
          }
          width={720}
        >
          <pre className="max-h-[60vh] overflow-auto rounded bg-gray-50 p-3 text-xs dark:bg-gray-900">
            {JSON.stringify(selected, null, 2)}
          </pre>
        </Modal>
      )}
    </div>
  );
};
