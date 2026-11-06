import {
  DownOutlined,
  InfoCircleOutlined,
  LoadingOutlined,
  LogoutOutlined,
  MoonOutlined,
  SunOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { App, Button, Dropdown, Modal, Spin, Tooltip } from 'antd';
import { useEffect, useRef, useState } from 'react';

import { APP_VERSION, IS_CONTROL_PLANE, WEBSITE_URL, setCurrentSiteId } from '../../../constants';
import { type DiskUsage, diskApi } from '../../../entity/disk';
import { type ProjectResponse, projectApi } from '../../../entity/projects';
import type { Site } from '../../../entity/sites';
import {
  ProjectRole,
  type UserProfile,
  UserRole,
  type UsersSettings,
  settingsApi,
  userApi,
} from '../../../entity/users';
import { AiObservabilityComponent } from '../../../features/aiObservability';
import { AlertsComponent } from '../../../features/alerts';
import { DashboardsComponent } from '../../../features/dashboards';
import { SessionsListComponent } from '../../../features/incidents';
import { InfrastructureComponent, type NavigateTarget } from '../../../features/infrastructure';
import { OrganizationsComponent } from '../../../features/organizations';
import {
  CreateProjectDialogComponent,
  DataSourcesComponent,
  ProjectApiKeysComponent,
  ProjectMembershipComponent,
  ProjectSettingsComponent,
} from '../../../features/projects';
import { QueryComponentComponent } from '../../../features/query';
import { ServiceMapComponent } from '../../../features/serviceMap';
import { SettingsComponent, rememberSettingsTab } from '../../../features/settings';
import { SyntheticsComponent } from '../../../features/synthetics';
import { TracesComponent } from '../../../features/traces';
import { ProfileComponent } from '../../../features/users/ui/ProfileComponent';
import { UsersComponent } from '../../../features/users/ui/UsersComponent';
import { useScreenHeight } from '../../../shared/hooks';
import {
  ENVIRONMENT_STYLE,
  groupSites,
  scopedStorageKey,
  siteGroupKey,
  strictestEnvironment,
  useSite,
} from '../../../shared/site';
import { useTheme } from '../../../shared/theme';
import { MobilePlaceholderComponent } from './MobilePlaceholderComponent';
import { ProjectSelectionComponent } from './ProjectSelectionComponent';
import { LocationPickerComponent } from './locationPicker/LocationPickerComponent';

type TabName =
  | 'profile'
  | 'obzly-settings'
  | 'users'
  | 'organizations'
  | 'synthetics'
  | 'infrastructure'
  | 'search'
  | 'traces'
  | 'service-map'
  | 'incidents'
  | 'dashboards'
  | 'alerts'
  | 'ai-observability'
  | 'settings'
  | 'api-keys'
  | 'members'
  | 'data-sources';

const TAB_NAMES: TabName[] = [
  'profile',
  'obzly-settings',
  'users',
  'organizations',
  'synthetics',
  'infrastructure',
  'search',
  'traces',
  'service-map',
  'incidents',
  'dashboards',
  'alerts',
  'ai-observability',
  'settings',
  'api-keys',
  'members',
  'data-sources',
];

// Scoped per Site on a control plane (scopedStorageKey) so one Site's
// project id is never restored on another.
const SELECTED_PROJECT_STORAGE_KEY = 'selected_project_id';

const SELECTED_TAB_STORAGE_KEY = 'selected_tab';

// Tabs whose empty state depends on there being a project (or a Site).
const PROJECT_SCOPED_TABS: TabName[] = [
  'search',
  'traces',
  'service-map',
  'incidents',
  'dashboards',
  'alerts',
  'ai-observability',
  'settings',
  'api-keys',
  'data-sources',
  'members',
];

const isProjectScopedTab = (tab: TabName): boolean => PROJECT_SCOPED_TABS.includes(tab);

// Restore the last-viewed tab so a refresh lands back on it.
const getInitialSelectedTab = (): TabName => {
  const stored = localStorage.getItem(SELECTED_TAB_STORAGE_KEY);
  // The Site registry moved into Obzly settings.
  if (stored === 'sites') {
    rememberSettingsTab('sites');
    return 'obzly-settings';
  }
  return TAB_NAMES.includes(stored as TabName) ? (stored as TabName) : 'search';
};

export const MainScreenComponent = () => {
  const { message } = App.useApp();
  const { theme, toggleTheme } = useTheme();
  const screenHeight = useScreenHeight();
  const contentHeight = screenHeight - 95;

  // Empty/no-op in single-site mode.
  const { sites, selectedSite, selectSite, isLoading: isSiteLoading } = useSite();

  const [selectedTab, setSelectedTab] = useState<TabName>(getInitialSelectedTab);
  // Service the Traces tab opens filtered to (from the service map); cleared on leaving it.
  const [tracesServiceFilter, setTracesServiceFilter] = useState<string | undefined>(undefined);
  // Trace the Traces tab opens (from a log's "Open trace"); cleared on leaving it.
  const [tracesOpenTrace, setTracesOpenTrace] = useState<
    { traceId: string; at: string } | undefined
  >(undefined);
  useEffect(() => {
    if (selectedTab !== 'traces') {
      setTracesServiceFilter(undefined);
      setTracesOpenTrace(undefined);
    }
  }, [selectedTab]);
  const [diskUsage, setDiskUsage] = useState<DiskUsage | undefined>(undefined);
  // centralUser is the account on this deployment (gates admin tabs and the
  // profile menu); user is the identity on the selected Site.
  const [centralUser, setCentralUser] = useState<UserProfile | undefined>(undefined);
  const [isCentralLoading, setIsCentralLoading] = useState(true);
  const [user, setUser] = useState<UserProfile | undefined>(undefined);
  const [globalSettings, setGlobalSettings] = useState<UsersSettings | undefined>(undefined);

  const [projects, setProjects] = useState<ProjectResponse[]>([]);
  const [selectedProject, setSelectedProject] = useState<ProjectResponse | undefined>(undefined);

  const [isLoading, setIsLoading] = useState(false);
  // The Site's own refusal relayed by the control plane; shown instead of
  // the project area, never as "Create project".
  const [siteLoadError, setSiteLoadError] = useState<string | undefined>(undefined);
  // The Site `projects` was loaded for; see loadedSiteKeyRef for why it lags.
  const [projectsSiteId, setProjectsSiteId] = useState<string | undefined>(undefined);
  const [showCreateProjectDialog, setShowCreateProjectDialog] = useState(false);
  const [isAboutModalOpen, setIsAboutModalOpen] = useState(false);

  // "Create project here" on any Site: switch to it, then open the dialog.
  const [pendingCreateSiteId, setPendingCreateSiteId] = useState<string | undefined>(undefined);
  const handleCreateOnSite = (site: Site) => {
    setPendingCreateSiteId(site.id);
    selectSite(site);
  };
  useEffect(() => {
    if (!pendingCreateSiteId || selectedSite?.id !== pendingCreateSiteId) return;
    setPendingCreateSiteId(undefined);
    setShowCreateProjectDialog(true);
  }, [pendingCreateSiteId, selectedSite?.id]);

  const handleLocationSelect = (site: Site, projectId?: string) => {
    if (site.id === selectedSite?.id) {
      const project = projects.find((p) => p.id === projectId);
      if (project) setSelectedProject(project);
      return;
    }
    // The Site-change effect restores this project once the Site's projects load.
    if (projectId) {
      localStorage.setItem(scopedStorageKey(SELECTED_PROJECT_STORAGE_KEY, site.id), projectId);
    }
    selectSite(site);
  };

  const openFromInventory = (target: NavigateTarget) => {
    if (target.logsSearch) {
      try {
        localStorage.setItem(
          `obzly-query-${target.projectId}`,
          JSON.stringify({
            mode: 'simple',
            simpleText: target.logsSearch,
            sqlText: '',
            query: null,
            sortOrder: 'desc',
          }),
        );
      } catch {
        /* the Logs tab opens with its last search */
      }
    }
    if (IS_CONTROL_PLANE) {
      const site = sites.find((s) => s.id === target.siteId);
      if (!site) {
        message.error('That Site is no longer available');
        return;
      }
      handleLocationSelect(site, target.projectId);
    } else {
      const project = projects.find((p) => p.id === target.projectId);
      if (project) setSelectedProject(project);
    }
    setSelectedTab(target.tab);
    if (target.serviceName) setTracesServiceFilter(target.serviceName);
  };

  // Which Site's projects are loaded ('' single-site, null while switching).
  // selectedSite changes a render before projects reset, so the save effect
  // would otherwise write the old Site's project id under the new Site's key.
  const loadedSiteKeyRef = useRef<string | null>(null);
  // Drops a slow response for a Site the user already switched away from.
  const activeSiteKeyRef = useRef<string>('');

  const selectedProjectStorageKey = selectedSite
    ? scopedStorageKey(SELECTED_PROJECT_STORAGE_KEY, selectedSite.id)
    : SELECTED_PROJECT_STORAGE_KEY;

  // Not Site-dependent, so admin tabs work on a control plane with no Sites yet.
  useEffect(() => {
    (async () => {
      try {
        const [me, settings] = await Promise.all([
          userApi.getCurrentUser(),
          settingsApi.getSettings(),
        ]);
        setCentralUser(me);
        setGlobalSettings(settings);
      } catch (e) {
        message.error((e as Error).message);
      }
      setIsCentralLoading(false);
    })();
  }, []);

  const loadData = async () => {
    const siteKey = selectedSite?.id ?? '';
    setIsLoading(true);
    setSiteLoadError(undefined);

    try {
      const [diskUsage, siteUser, projects] = await Promise.all([
        diskApi.getDiskUsage(),
        userApi.getSiteUser(),
        projectApi.getProjects(),
      ]);
      if (activeSiteKeyRef.current !== siteKey) return;

      setDiskUsage(diskUsage);
      setUser(siteUser);
      loadedSiteKeyRef.current = siteKey;
      setProjects(projects.projects);
      setProjectsSiteId(siteKey);
    } catch (e) {
      if (activeSiteKeyRef.current !== siteKey) return;
      setSiteLoadError((e as Error).message);
      message.error((e as Error).message);
    }

    setIsLoading(false);
  };

  // Reset the project selection first so it never points at the previous
  // Site's project while the new Site's projects load.
  useEffect(() => {
    if (IS_CONTROL_PLANE && !selectedSite) {
      setIsLoading(false);
      return;
    }
    if (IS_CONTROL_PLANE && selectedSite) {
      // Guard against effect ordering: getApiBase() must address this Site
      // before the first call goes out.
      setCurrentSiteId(selectedSite.id);
    }
    activeSiteKeyRef.current = selectedSite?.id ?? '';
    loadedSiteKeyRef.current = null;
    setProjectsSiteId(undefined);
    setSelectedProject(undefined);
    setProjects([]);
    loadData();
  }, [selectedSite?.id]);

  useEffect(() => {
    if (!selectedProject && projects.length > 0) {
      const previouslySelectedProjectId = localStorage.getItem(selectedProjectStorageKey);
      const previouslySelectedProject = projects.find(
        (project) => project.id === previouslySelectedProjectId,
      );
      // Never default into the system project; an admin opens it on purpose.
      // The Site picker doesn't list it, so don't restore it there either.
      const restorable =
        previouslySelectedProject && !(IS_CONTROL_PLANE && previouslySelectedProject.isSystem)
          ? previouslySelectedProject
          : undefined;
      const projectToSelect = restorable || projects.find((project) => !project.isSystem);
      if (projectToSelect) setSelectedProject(projectToSelect);
    }
  }, [projects, selectedProject, selectedProjectStorageKey]);

  useEffect(() => {
    if (selectedProject && loadedSiteKeyRef.current === (selectedSite?.id ?? '')) {
      localStorage.setItem(selectedProjectStorageKey, selectedProject.id);
    }
  }, [selectedProject, selectedProjectStorageKey]);

  useEffect(() => {
    localStorage.setItem(SELECTED_TAB_STORAGE_KEY, selectedTab);
  }, [selectedTab]);

  const handleCreateProject = () => {
    setShowCreateProjectDialog(true);
  };

  const handleProjectCreated = async (newProject: ProjectResponse) => {
    try {
      const projectsResponse = await projectApi.getProjects();
      setProjects(projectsResponse.projects);
      setSelectedProject(newProject);
      setSelectedTab('search');
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  // The selected Site name's environment colours a strip under the navbar.
  const selectedGroup =
    selectedSite && groupSites(sites).find((group) => group.key === siteGroupKey(selectedSite));
  const selectedEnvironment = selectedGroup ? strictestEnvironment(selectedGroup.sites) : '';

  const isUsedMoreThan95Percent =
    diskUsage && diskUsage.usedSpaceBytes / diskUsage.totalSpaceBytes > 0.95;

  return (
    <>
      <div className="[@media(min-width:450px)]:hidden">
        <MobilePlaceholderComponent />
      </div>

      <div
        style={{ height: screenHeight }}
        className="hidden bg-[#f5f5f5] p-3 dark:bg-[#0b1220] [@media(min-width:450px)]:block"
      >
        <div className="relative mb-3 flex h-[60px] items-center rounded bg-white p-3 shadow dark:bg-[#141b2d] dark:shadow-none">
          {selectedEnvironment && (
            <div
              aria-hidden="true"
              className={`absolute inset-x-0 bottom-0 h-[3px] rounded-b ${ENVIRONMENT_STYLE[selectedEnvironment].strip}`}
            />
          )}
          <div className="flex items-center gap-3 hover:opacity-80">
            <button
              type="button"
              onClick={() => setSelectedTab('search')}
              className="cursor-pointer"
            >
              <img className="h-[35px] w-[35px]" src="/logo.svg" />
            </button>
          </div>

          {IS_CONTROL_PLANE ? (
            sites.length > 0 && (
              <div className="ml-6">
                <LocationPickerComponent
                  sites={sites}
                  selectedSite={selectedSite}
                  projects={projects}
                  projectsSiteId={projectsSiteId}
                  isLoadingProjects={isLoading}
                  selectedProject={selectedProject}
                  isAdmin={centralUser?.role === UserRole.ADMIN}
                  onSelect={handleLocationSelect}
                  onCreateProject={handleCreateOnSite}
                />
              </div>
            )
          ) : (
            <div className="ml-6">
              {!isLoading && (
                <ProjectSelectionComponent
                  projects={projects}
                  selectedProject={selectedProject}
                  onCreateProject={handleCreateProject}
                  onProjectSelect={setSelectedProject}
                />
              )}
            </div>
          )}

          <div className="mr-3 ml-auto flex items-center gap-5">
            {isUsedMoreThan95Percent && diskUsage && (
              <Tooltip title="To make backups locally and restore them, you need to have enough space on your disk. For restore, you need to have same amount of space that the backup size.">
                <div
                  className={`cursor-pointer text-center text-xs ${isUsedMoreThan95Percent ? 'text-red-500' : 'text-gray-500 dark:text-gray-300'}`}
                >
                  {(diskUsage.usedSpaceBytes / 1024 ** 3).toFixed(1)} of{' '}
                  {(diskUsage.totalSpaceBytes / 1024 ** 3).toFixed(1)} GB
                  <br />
                  ROM used (
                  {((diskUsage.usedSpaceBytes / diskUsage.totalSpaceBytes) * 100).toFixed(1)}%)
                </div>
              </Tooltip>
            )}

            <Tooltip title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}>
              <Button
                type="text"
                shape="circle"
                icon={theme === 'dark' ? <SunOutlined /> : <MoonOutlined />}
                onClick={toggleTheme}
              />
            </Tooltip>

            <Dropdown
              menu={{
                items: [
                  {
                    key: 'profile',
                    label: 'Edit profile',
                    icon: <UserOutlined />,
                    onClick: () => setSelectedTab('profile'),
                  },
                  {
                    key: 'about',
                    label: 'About',
                    icon: <InfoCircleOutlined />,
                    onClick: () => setIsAboutModalOpen(true),
                  },
                  { type: 'divider' },
                  {
                    key: 'logout',
                    label: 'Logout',
                    icon: <LogoutOutlined />,
                    danger: true,
                    onClick: () => {
                      userApi.logout();
                      window.location.reload();
                    },
                  },
                ],
              }}
              trigger={['click']}
            >
              <Button
                type={selectedTab === 'profile' ? 'primary' : 'default'}
                className={
                  selectedTab === 'profile'
                    ? 'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700'
                    : ''
                }
                icon={<UserOutlined />}
              >
                {centralUser?.name || centralUser?.email || 'Profile'}
                <DownOutlined className="!text-xs" />
              </Button>
            </Dropdown>
          </div>
        </div>

        {isLoading || isCentralLoading || (IS_CONTROL_PLANE && isSiteLoading) ? (
          <div className="flex items-center justify-center py-2" style={{ height: contentHeight }}>
            <Spin indicator={<LoadingOutlined spin />} size="large" />
          </div>
        ) : (
          <div className="relative flex">
            <div
              className="max-w-[60px] min-w-[60px] rounded bg-white py-2 shadow dark:bg-[#141b2d] dark:shadow-none"
              style={{ height: contentHeight }}
            >
              {(() => {
                // Project tabs and instance-wide global tabs, split by a divider.
                type SidebarTab = {
                  text: string;
                  name: TabName;
                  icon: string;
                  selectedIcon: string;
                  onClick: () => void;
                };

                const allProjectTabs: (SidebarTab & { isVisible: boolean })[] = [
                  {
                    text: 'Search',
                    name: 'search',
                    icon: '/icons/menu/search-gray.svg',
                    selectedIcon: '/icons/menu/search-white.svg',
                    onClick: () => setSelectedTab('search'),
                    isVisible: true,
                  },
                  {
                    text: 'Traces',
                    name: 'traces',
                    icon: '/icons/menu/traces-gray.svg',
                    selectedIcon: '/icons/menu/traces-white.svg',
                    onClick: () => setSelectedTab('traces'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Service Map',
                    name: 'service-map',
                    icon: '/icons/menu/service-map-gray.svg',
                    selectedIcon: '/icons/menu/service-map-white.svg',
                    onClick: () => setSelectedTab('service-map'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Incidents',
                    name: 'incidents',
                    icon: '/icons/menu/incidents-gray.svg',
                    selectedIcon: '/icons/menu/incidents-white.svg',
                    onClick: () => setSelectedTab('incidents'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Dashboards',
                    name: 'dashboards',
                    icon: '/icons/menu/dashboard-gray.svg',
                    selectedIcon: '/icons/menu/dashboard-white.svg',
                    onClick: () => setSelectedTab('dashboards'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Infrastructure',
                    name: 'infrastructure',
                    icon: '/icons/menu/infrastructure-gray.svg',
                    selectedIcon: '/icons/menu/infrastructure-white.svg',
                    onClick: () => setSelectedTab('infrastructure'),
                    isVisible: true,
                  },
                  {
                    text: 'Synthetics',
                    name: 'synthetics',
                    icon: '/icons/menu/synthetics-gray.svg',
                    selectedIcon: '/icons/menu/synthetics-white.svg',
                    onClick: () => setSelectedTab('synthetics'),
                    isVisible: IS_CONTROL_PLANE,
                  },
                  {
                    text: 'Alerts',
                    name: 'alerts',
                    icon: '/icons/menu/alerts-gray.svg',
                    selectedIcon: '/icons/menu/alerts-white.svg',
                    onClick: () => setSelectedTab('alerts'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'AI Observability',
                    name: 'ai-observability',
                    icon: '/icons/menu/ai-gray.svg',
                    selectedIcon: '/icons/menu/ai-white.svg',
                    onClick: () => setSelectedTab('ai-observability'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Project settings',
                    name: 'settings',
                    icon: '/icons/menu/project-settings-gray.svg',
                    selectedIcon: '/icons/menu/project-settings-white.svg',
                    onClick: () => setSelectedTab('settings'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Members',
                    name: 'members',
                    icon: '/icons/menu/users-gray.svg',
                    selectedIcon: '/icons/menu/users-white.svg',
                    onClick: () => setSelectedTab('members'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'API Keys',
                    name: 'api-keys',
                    icon: '/icons/menu/key-gray.svg',
                    selectedIcon: '/icons/menu/key-white.svg',
                    onClick: () => setSelectedTab('api-keys'),
                    isVisible: !!selectedProject,
                  },
                  {
                    text: 'Data Sources',
                    name: 'data-sources',
                    icon: '/icons/menu/data-sources-gray.svg',
                    selectedIcon: '/icons/menu/data-sources-white.svg',
                    onClick: () => setSelectedTab('data-sources'),
                    isVisible: !!selectedProject,
                  },
                ];
                const projectTabs = allProjectTabs.filter((tab) => tab.isVisible);

                const allGlobalTabs: (SidebarTab & { isVisible: boolean })[] = [
                  {
                    text: 'Obzly settings',
                    name: 'obzly-settings',
                    icon: '/icons/menu/global-settings-gray.svg',
                    selectedIcon: '/icons/menu/global-settings-white.svg',
                    onClick: () => setSelectedTab('obzly-settings'),
                    isVisible: true,
                  },
                  {
                    text: 'Users',
                    name: 'users',
                    icon: '/icons/menu/user-card-gray.svg',
                    selectedIcon: '/icons/menu/user-card-white.svg',
                    onClick: () => setSelectedTab('users'),
                    isVisible: true,
                  },
                  {
                    text: 'Organizations',
                    name: 'organizations',
                    icon: '/icons/menu/organizations-gray.svg',
                    selectedIcon: '/icons/menu/organizations-white.svg',
                    onClick: () => setSelectedTab('organizations'),
                    isVisible: true,
                  },
                ];
                // Gated on the central role: they manage this deployment, not the Site.
                const globalTabs = allGlobalTabs.filter(
                  (tab) =>
                    tab.isVisible &&
                    (centralUser?.role === UserRole.ADMIN || tab.name === 'organizations'),
                );

                const renderTab = (tab: SidebarTab) => (
                  <div key={tab.text} className="flex justify-center">
                    <Tooltip title={tab.text} placement="right">
                      <div
                        className={`flex h-[50px] w-[50px] cursor-pointer items-center justify-center rounded ${selectedTab === tab.name ? 'bg-emerald-600' : 'hover:bg-blue-50 dark:hover:bg-gray-700'}`}
                        onClick={tab.onClick}
                      >
                        <div className="mb-1">
                          <div className="flex justify-center">
                            <img
                              src={selectedTab === tab.name ? tab.selectedIcon : tab.icon}
                              width={20}
                              alt={tab.text}
                              loading="lazy"
                            />
                          </div>
                        </div>
                      </div>
                    </Tooltip>
                  </div>
                );

                return (
                  <>
                    {projectTabs.map(renderTab)}
                    {projectTabs.length > 0 && globalTabs.length > 0 && (
                      <div className="mx-2 my-2 border-t border-gray-200 dark:border-gray-700" />
                    )}
                    {globalTabs.map(renderTab)}
                  </>
                );
              })()}
            </div>

            {selectedTab === 'profile' && <ProfileComponent contentHeight={contentHeight} />}

            {selectedTab === 'obzly-settings' && (
              <SettingsComponent contentHeight={contentHeight} />
            )}

            {selectedTab === 'users' && (
              <UsersComponent contentHeight={contentHeight} currentUserId={user?.id} />
            )}

            {selectedTab === 'synthetics' && IS_CONTROL_PLANE && (
              <SyntheticsComponent
                contentHeight={contentHeight}
                canManage={centralUser?.role === UserRole.ADMIN}
              />
            )}
            {selectedTab === 'infrastructure' && (
              <InfrastructureComponent
                contentHeight={contentHeight}
                multiSite={IS_CONTROL_PLANE}
                onManageCloud={
                  centralUser?.role === UserRole.ADMIN
                    ? () => {
                        rememberSettingsTab('cloud');
                        setSelectedTab('obzly-settings');
                      }
                    : undefined
                }
                onNavigate={openFromInventory}
              />
            )}
            {selectedTab === 'organizations' && (
              <OrganizationsComponent
                contentHeight={contentHeight}
                isAdmin={centralUser?.role === UserRole.ADMIN}
              />
            )}

            {IS_CONTROL_PLANE &&
            selectedSite &&
            siteLoadError &&
            isProjectScopedTab(selectedTab) ? (
              <div
                className="flex grow flex-col items-center justify-center gap-3 rounded pl-5 text-center"
                style={{ height: contentHeight }}
              >
                <div className="text-lg font-semibold">
                  Couldn&apos;t open site &quot;{selectedSite.name}&quot;
                </div>
                <div className="max-w-lg text-gray-500 dark:text-gray-300">{siteLoadError}</div>
                <Button
                  type="primary"
                  size="large"
                  onClick={() => loadData()}
                  className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                >
                  Retry
                </Button>
              </div>
            ) : IS_CONTROL_PLANE && !selectedSite && isProjectScopedTab(selectedTab) ? (
              // No reachable Site: "Create project" would have nowhere to create it.
              <div
                className="flex grow flex-col items-center justify-center gap-3 rounded pl-5 text-center"
                style={{ height: contentHeight }}
              >
                <div className="text-lg font-semibold">No Site to show yet</div>
                {centralUser?.role === UserRole.ADMIN ? (
                  <>
                    <div className="max-w-md text-gray-500 dark:text-gray-300">
                      Register a Site and grant yourself access to it - then pick it in the top bar.
                    </div>
                    <Button
                      type="primary"
                      size="large"
                      onClick={() => {
                        rememberSettingsTab('sites');
                        setSelectedTab('obzly-settings');
                      }}
                      className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                    >
                      Open Sites
                    </Button>
                  </>
                ) : (
                  <div className="max-w-md text-gray-500 dark:text-gray-300">
                    You don&apos;t have access to any Site yet - ask an administrator to grant you
                    one.
                  </div>
                )}
              </div>
            ) : !selectedProject &&
              projects.every((project) => project.isSystem) &&
              isProjectScopedTab(selectedTab) ? (
              <div
                className="flex grow items-center justify-center rounded pl-5"
                style={{ height: contentHeight }}
              >
                <Button
                  type="primary"
                  size="large"
                  onClick={handleCreateProject}
                  className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                >
                  Create project
                </Button>
              </div>
            ) : (
              <>
                {selectedTab === 'settings' && selectedProject && user && (
                  <ProjectSettingsComponent
                    projectResponse={selectedProject}
                    contentHeight={contentHeight}
                    user={user}
                  />
                )}
                {selectedTab === 'api-keys' && selectedProject && user && (
                  <ProjectApiKeysComponent
                    projectResponse={selectedProject}
                    contentHeight={contentHeight}
                    user={user}
                  />
                )}
                {selectedTab === 'data-sources' && selectedProject && (
                  <DataSourcesComponent
                    projectResponse={selectedProject}
                    contentHeight={contentHeight}
                  />
                )}
                {selectedTab === 'members' && selectedProject && user && (
                  <ProjectMembershipComponent
                    projectResponse={selectedProject}
                    contentHeight={contentHeight}
                    user={user}
                  />
                )}
                {selectedTab === 'search' && selectedProject && user && (
                  <QueryComponentComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    user={user}
                    onOpenTrace={(traceId, at) => {
                      setTracesOpenTrace({ traceId, at });
                      setSelectedTab('traces');
                    }}
                  />
                )}
                {selectedTab === 'traces' && selectedProject && user && (
                  <TracesComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    initialServiceName={tracesServiceFilter}
                    initialTrace={tracesOpenTrace}
                  />
                )}
                {selectedTab === 'service-map' && selectedProject && user && (
                  <ServiceMapComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    onViewTraces={(serviceName) => {
                      setTracesServiceFilter(serviceName);
                      setSelectedTab('traces');
                    }}
                  />
                )}
                {selectedTab === 'incidents' && selectedProject && user && (
                  <SessionsListComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                  />
                )}
                {selectedTab === 'dashboards' && selectedProject && user && (
                  <DashboardsComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    userRole={selectedProject.userRole}
                  />
                )}
                {selectedTab === 'alerts' && selectedProject && user && (
                  <AlertsComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    canManageRules={
                      user.role === UserRole.ADMIN ||
                      selectedProject.userRole === ProjectRole.OWNER ||
                      selectedProject.userRole === ProjectRole.ADMIN
                    }
                    canManageChannels={
                      user.role === UserRole.ADMIN || selectedProject.userRole === ProjectRole.OWNER
                    }
                    canSilence={
                      user.role === UserRole.ADMIN ||
                      selectedProject.userRole !== ProjectRole.VIEWER
                    }
                  />
                )}
                {selectedTab === 'ai-observability' && selectedProject && user && (
                  <AiObservabilityComponent
                    projectId={selectedProject.id}
                    contentHeight={contentHeight}
                    onOpenDataSources={() => setSelectedTab('data-sources')}
                    onOpenTrace={(traceId, at) => {
                      setTracesOpenTrace({ traceId, at });
                      setSelectedTab('traces');
                    }}
                    canManage={
                      user.role === UserRole.ADMIN ||
                      selectedProject.userRole === ProjectRole.OWNER ||
                      selectedProject.userRole === ProjectRole.ADMIN
                    }
                  />
                )}
              </>
            )}
          </div>
        )}

        {showCreateProjectDialog && user && globalSettings && (
          <CreateProjectDialogComponent
            user={user}
            globalSettings={globalSettings}
            onClose={() => setShowCreateProjectDialog(false)}
            onProjectCreated={handleProjectCreated}
          />
        )}

        <Modal
          title="About Obzly"
          open={isAboutModalOpen}
          onCancel={() => setIsAboutModalOpen(false)}
          footer={null}
        >
          <div className="space-y-3">
            <div className="flex items-center gap-3">
              <img className="h-[40px] w-[40px]" src="/logo.svg" />
              <div>
                <div className="text-lg font-bold">Obzly</div>
                <div className="text-sm text-gray-500 dark:text-gray-300">
                  Version {APP_VERSION}
                </div>
              </div>
            </div>

            <div className="text-sm text-gray-600 dark:text-gray-300">
              Self-hosted observability platform for logs, traces, metrics, and dashboards.
            </div>

            <a
              href={WEBSITE_URL}
              target="_blank"
              rel="noreferrer"
              className="text-sm text-emerald-600 hover:underline"
            >
              {WEBSITE_URL}
            </a>
          </div>
        </Modal>
      </div>
    </>
  );
};
