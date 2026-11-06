// The control plane's location picker: Site name tabs over a searchable list
// of that name's Site IDs and their projects.
import { CheckOutlined, DownOutlined, PlusOutlined, WarningOutlined } from '@ant-design/icons';
import { Input, Popover, Select, Spin, Tooltip } from 'antd';
import dayjs from 'dayjs';
import type { JSX, KeyboardEvent, ReactNode } from 'react';
import { useCallback, useEffect, useMemo, useState } from 'react';

import { type ProjectResponse, projectApi, splitSystemProject } from '../../../../entity/projects';
import { type Site, type SiteHealth, type SiteStatus, sitesApi } from '../../../../entity/sites';
import { useVisibleInterval } from '../../../../shared/lib';
import {
  ENVIRONMENT_STYLE,
  type SiteGroup,
  groupSites,
  siteGroupKey,
  siteLabel,
  strictestEnvironment,
} from '../../../../shared/site';
import { Heartbeat } from './Heartbeat';
import { HEALTH_LABEL } from './healthLabels';
import { type RecentLocation, getRecentLocations, pushRecentLocation } from './recentLocations';
import { assignSiteColors } from './siteColors';

const STATUS_REFRESH_MS = 30_000;
// Beyond this many Site names, tabs stop fitting the navbar.
const MAX_TABS = 5;

type StatusDetail = Pick<SiteStatus, 'siteId' | 'status'> & Partial<SiteStatus>;

interface SiteProjects {
  projects: ProjectResponse[];
  error?: string;
}

interface ProjectRow {
  site: Site;
  project: Pick<ProjectResponse, 'id' | 'name'>;
}

interface Props {
  sites: Site[];
  selectedSite?: Site;
  /** The selected Site's projects, as already loaded by the main screen. */
  projects: ProjectResponse[];
  /**
   * Which Site `projects` belongs to. It lags selectedSite by a render during a
   * switch; until they match, projects/selectedProject are the previous Site's.
   */
  projectsSiteId?: string;
  isLoadingProjects: boolean;
  selectedProject?: ProjectResponse;
  /** Central admins also see each Site's version, response time and check time. */
  isAdmin: boolean;
  onSelect: (site: Site, projectId?: string) => void;
  onCreateProject: (site: Site) => void;
}

const Bullet = ({ color }: { color?: string }) => (
  <span
    aria-hidden="true"
    className="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
    style={{ background: color ?? '#9ca3af' }}
  />
);

const EnvironmentBadge = ({ group }: { group?: SiteGroup }) => {
  if (!group) return null;
  const environment = strictestEnvironment(group.sites);
  return (
    <span
      className={`shrink-0 rounded px-1.5 py-0.5 text-[11px] ${
        environment
          ? ENVIRONMENT_STYLE[environment].badge
          : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300'
      }`}
    >
      {group.name}
    </span>
  );
};

const SectionLabel = ({ children }: { children: ReactNode }) => (
  <div className="px-3 pt-2.5 pb-1 text-xs text-gray-500 dark:text-gray-400">{children}</div>
);

export const LocationPickerComponent = ({
  sites,
  selectedSite,
  projects,
  projectsSiteId,
  isLoadingProjects,
  selectedProject,
  isAdmin,
  onSelect,
  onCreateProject,
}: Props): JSX.Element => {
  const [isOpen, setIsOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [statuses, setStatuses] = useState<Record<string, StatusDetail>>({});
  const [otherSiteProjects, setOtherSiteProjects] = useState<Record<string, SiteProjects>>({});
  const [loadingSiteIds, setLoadingSiteIds] = useState<string[]>([]);
  const [recents, setRecents] = useState<RecentLocation[]>(getRecentLocations);

  // Keeps what's already loaded while refreshing, so the list doesn't flicker.
  const loadOtherSites = useCallback(() => {
    const others = sites.filter((site) => site.id !== selectedSite?.id);
    setLoadingSiteIds(others.map((site) => site.id));
    others.forEach((site) => {
      projectApi
        .getProjectsOnSite(site.id)
        .then((response) =>
          setOtherSiteProjects((prev) => ({ ...prev, [site.id]: { projects: response.projects } })),
        )
        .catch((e: Error) =>
          setOtherSiteProjects((prev) => ({
            ...prev,
            [site.id]: {
              projects: prev[site.id]?.projects ?? [],
              error: e.message || 'unknown error',
            },
          })),
        )
        .finally(() => setLoadingSiteIds((prev) => prev.filter((id) => id !== site.id)));
    });
  }, [sites, selectedSite?.id]);

  const projectsFor = (site: Site): SiteProjects | undefined => {
    if (site.id !== selectedSite?.id) return otherSiteProjects[site.id];
    return projectsSiteId === site.id ? { projects } : undefined;
  };

  // Recents saved before the system project was hidden may still name it.
  const isSystemProject = (recent: RecentLocation) => {
    const site = sites.find((s) => s.id === recent.siteId);
    return (
      !!site && !!projectsFor(site)?.projects.some((p) => p.id === recent.projectId && p.isSystem)
    );
  };

  const select = (site: Site, projectId?: string) => {
    setIsOpen(false);
    setSearch('');
    onSelect(site, projectId);
  };

  // A Site name tab keeps your place: the same project name if that group has
  // it, else the project you last used there, else its first Site ID.
  const switchGroup = (group: SiteGroup) => {
    if (selectedSite && siteGroupKey(selectedSite) === group.key) return;
    for (const site of group.sites) {
      const match = projectsFor(site)?.projects.find(
        (p) => !p.isSystem && p.name === selectedProject?.name,
      );
      if (match) return select(site, match.id);
    }
    const recent = recents.find(
      (r) => !isSystemProject(r) && group.sites.some((site) => site.id === r.siteId),
    );
    const recentSite = recent && group.sites.find((site) => site.id === recent.siteId);
    if (recent && recentSite) return select(recentSite, recent.projectId);
    select(group.sites[0]);
  };

  const refreshStatuses = (isCancelled: () => boolean = () => false) =>
    (isAdmin ? sitesApi.getSiteStatuses() : sitesApi.getReachableSiteStatuses())
      .then((result: StatusDetail[]) => {
        if (!isCancelled()) setStatuses(Object.fromEntries(result.map((st) => [st.siteId, st])));
      })
      .catch(() => undefined);
  useEffect(() => {
    let cancelled = false;
    void refreshStatuses(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [sites, isAdmin]);
  useVisibleInterval(() => void refreshStatuses(), STATUS_REFRESH_MS);

  // Prefetch so Site name tabs can match projects by name, and refresh on open.
  useEffect(() => {
    loadOtherSites();
  }, [loadOtherSites]);

  useEffect(() => {
    if (isOpen) loadOtherSites();
  }, [isOpen, loadOtherSites]);

  useEffect(() => {
    if (
      selectedSite &&
      selectedProject &&
      projectsSiteId === selectedSite.id &&
      projects.some((p) => p.id === selectedProject.id && !p.isSystem)
    ) {
      setRecents(
        pushRecentLocation({
          siteId: selectedSite.id,
          projectId: selectedProject.id,
          projectName: selectedProject.name,
        }),
      );
    }
  }, [selectedSite, selectedProject, projects, projectsSiteId]);

  const groups = useMemo(() => groupSites(sites), [sites]);
  const colors = useMemo(() => assignSiteColors(sites), [sites]);
  const siteById = useMemo(() => new Map(sites.map((site) => [site.id, site])), [sites]);
  const groupOf = (site: Site) => groups.find((group) => group.key === siteGroupKey(site));
  const currentGroup = selectedSite ? groupOf(selectedSite) : undefined;
  const query = search.trim().toLowerCase();

  const matches = (site: Site, project: ProjectRow['project']) =>
    [project.name, site.id, site.displayName, site.name].some((text) =>
      text?.toLowerCase().includes(query),
    );

  const searchResults: { inGroup: ProjectRow[]; elsewhere: ProjectRow[] } = {
    inGroup: [],
    elsewhere: [],
  };
  if (query) {
    for (const site of sites) {
      for (const project of projectsFor(site)?.projects ?? []) {
        // The system project is reached by its link, not by search.
        if (project.isSystem || !matches(site, project)) continue;
        const row = { site, project };
        if (currentGroup && siteGroupKey(site) === currentGroup.key)
          searchResults.inGroup.push(row);
        else searchResults.elsewhere.push(row);
      }
    }
  }
  const firstResult = searchResults.inGroup[0] ?? searchResults.elsewhere[0];

  const visibleRecents = recents
    .map((recent) => ({ recent, site: siteById.get(recent.siteId) }))
    .filter((entry): entry is { recent: RecentLocation; site: Site } => !!entry.site)
    .filter(({ recent }) => !isSystemProject(recent))
    .filter(
      ({ recent }) =>
        !(recent.siteId === selectedSite?.id && recent.projectId === selectedProject?.id),
    );

  const onSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' && firstResult) select(firstResult.site, firstResult.project.id);
    if (event.key === 'Escape') setIsOpen(false);
  };

  const statusHint = (site: Site): ReactNode => {
    const status = statuses[site.id];
    if (!status) return 'Checking status…';
    return (
      <div className="text-xs">
        <div className="font-medium">{HEALTH_LABEL[status.status as SiteHealth]}</div>
        {status.message && <div>{status.message}</div>}
        {status.latencyMs !== undefined && status.status !== 'disabled' && (
          <div>Response time {status.latencyMs} ms</div>
        )}
        {status.version && <div>Version {status.version}</div>}
        {status.checkedAt && <div>Checked {dayjs(status.checkedAt).fromNow()}</div>}
      </div>
    );
  };

  const projectRow = (
    { site, project }: ProjectRow,
    options: { indent?: boolean; showSite?: boolean; showGroup?: boolean } = {},
  ) => {
    const isSelected = site.id === selectedSite?.id && project.id === selectedProject?.id;
    return (
      <button
        key={`${site.id}/${project.id}`}
        type="button"
        onClick={() => select(site, project.id)}
        className={`flex w-full cursor-pointer items-center gap-2 rounded px-3 py-1.5 text-left text-sm ${
          options.indent ? 'pl-8' : ''
        } ${
          isSelected
            ? 'bg-emerald-50 font-medium text-emerald-800 dark:bg-emerald-950 dark:text-emerald-200'
            : 'text-gray-800 hover:bg-gray-100 dark:text-gray-100 dark:hover:bg-gray-800'
        }`}
      >
        {options.showSite && <Bullet color={colors[site.id]} />}
        <span className="truncate">{project.name}</span>
        <span className="ml-auto flex shrink-0 items-center gap-2">
          {options.showSite && (
            <span className="max-w-[120px] truncate text-xs text-gray-500 dark:text-gray-400">
              {siteLabel(site)}
            </span>
          )}
          {options.showGroup && <EnvironmentBadge group={groupOf(site)} />}
          {isSelected && <CheckOutlined className="text-xs" />}
        </span>
      </button>
    );
  };

  const siteSection = (site: Site) => {
    const status = statuses[site.id]?.status as SiteHealth | undefined;
    const loaded = projectsFor(site);
    const isLoading =
      site.id === selectedSite?.id
        ? isLoadingProjects || !loaded
        : !loaded && loadingSiteIds.includes(site.id);
    const latency = statuses[site.id]?.latencyMs;
    // Admins open the system project from the Site's own UI.
    const { regular: listed } = splitSystemProject(loaded?.projects ?? []);

    let body: ReactNode;
    if (isLoading) {
      body = (
        <div className="py-1.5 pl-8">
          <Spin size="small" />
        </div>
      );
    } else if (status === 'down' && !listed.length) {
      body = (
        <div className="py-1.5 pr-3 pl-8 text-xs text-gray-500 dark:text-gray-400">
          Projects unavailable while the Site is down
        </div>
      );
    } else if (loaded?.error && !listed.length) {
      body = (
        <div className="py-1.5 pr-3 pl-8 text-xs text-red-600 dark:text-red-400">
          Couldn&apos;t load projects: {loaded.error}
        </div>
      );
    } else if (!listed.length) {
      body = (
        <>
          <div className="py-1.5 pr-3 pl-8 text-xs text-gray-500 dark:text-gray-400">
            No projects yet
          </div>
        </>
      );
    } else {
      body = <>{listed.map((project) => projectRow({ site, project }, { indent: true }))}</>;
    }

    return (
      <div key={site.id}>
        <div className="flex items-center gap-2 px-3 pt-2.5 pb-1 text-xs">
          <Bullet color={colors[site.id]} />
          <span className="truncate font-medium text-gray-800 dark:text-gray-100">
            {siteLabel(site)}
          </span>
          {site.displayName && (
            <span className="truncate font-mono text-[11px] text-gray-400 dark:text-gray-500">
              {site.id}
            </span>
          )}
          <Tooltip title={statusHint(site)}>
            <span className="inline-flex">
              <Heartbeat status={status} />
            </span>
          </Tooltip>
          <span className="ml-auto flex items-center gap-2">
            {status === 'down' ? (
              <span className="text-red-600 dark:text-red-400">Unreachable</span>
            ) : (
              isAdmin &&
              latency !== undefined && (
                <span className="text-gray-400 dark:text-gray-500">
                  {latency >= 1000 ? `${(latency / 1000).toFixed(1)} s` : `${latency} ms`}
                </span>
              )
            )}
            <Tooltip title={`New project on ${siteLabel(site)}`}>
              <button
                type="button"
                aria-label={`New project on ${siteLabel(site)}`}
                onClick={() => {
                  setIsOpen(false);
                  onCreateProject(site);
                }}
                className="cursor-pointer rounded px-1 text-gray-400 hover:bg-gray-100 hover:text-emerald-600 dark:hover:bg-gray-800"
              >
                <PlusOutlined />
              </button>
            </Tooltip>
          </span>
        </div>
        {body}
      </div>
    );
  };

  const dropdown = (
    <div className="w-[380px]">
      <div className="border-b border-gray-200 p-2 dark:border-gray-700">
        <Input
          autoFocus
          allowClear
          size="small"
          placeholder="Find a project or Site ID"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onKeyDown={onSearchKeyDown}
        />
      </div>
      <div className="max-h-[60vh] overflow-y-auto py-1">
        {query ? (
          <>
            {searchResults.inGroup.length > 0 && (
              <>
                <SectionLabel>In {currentGroup?.name}</SectionLabel>
                {searchResults.inGroup.map((row) => projectRow(row, { showSite: true }))}
              </>
            )}
            {searchResults.elsewhere.length > 0 && (
              <>
                <SectionLabel>Other Sites</SectionLabel>
                {searchResults.elsewhere.map((row) =>
                  projectRow(row, { showSite: true, showGroup: true }),
                )}
              </>
            )}
            {!firstResult && (
              <div className="px-3 py-4 text-center text-xs text-gray-500 dark:text-gray-400">
                No projects match &quot;{search.trim()}&quot;
              </div>
            )}
          </>
        ) : (
          <>
            {visibleRecents.length > 0 && (
              <>
                <SectionLabel>Recent</SectionLabel>
                {visibleRecents.map(({ recent, site }) =>
                  projectRow(
                    { site, project: { id: recent.projectId, name: recent.projectName } },
                    {
                      showSite: true,
                      showGroup: !currentGroup || siteGroupKey(site) !== currentGroup.key,
                    },
                  ),
                )}
                <div className="mx-3 mt-1.5 border-t border-gray-200 dark:border-gray-700" />
              </>
            )}
            {(currentGroup?.sites ?? []).map(siteSection)}
          </>
        )}
      </div>
    </div>
  );

  const tabClass = (group: SiteGroup, isActive: boolean) => {
    const environment = strictestEnvironment(group.sites);
    if (!isActive) {
      return 'border-transparent text-gray-600 hover:bg-white hover:text-gray-900 dark:text-gray-300 dark:hover:bg-gray-800 dark:hover:text-gray-100';
    }
    return environment
      ? ENVIRONMENT_STYLE[environment].tab
      : 'border-gray-300 bg-white text-gray-900 dark:border-gray-600 dark:bg-gray-800 dark:text-gray-100';
  };

  return (
    <div className="flex items-center gap-3 select-none">
      {groups.length > MAX_TABS ? (
        <Select
          size="small"
          className="!w-44"
          value={currentGroup?.key}
          onChange={(key) => {
            const group = groups.find((g) => g.key === key);
            if (group) switchGroup(group);
          }}
          options={groups.map((group) => ({ value: group.key, label: group.name }))}
        />
      ) : (
        groups.length > 1 && (
          <div
            role="tablist"
            aria-label="Site"
            className="flex gap-0.5 rounded-md bg-gray-100 p-0.5 dark:bg-gray-900"
          >
            {groups.map((group) => {
              const isActive = group.key === currentGroup?.key;
              const environment = strictestEnvironment(group.sites);
              return (
                <button
                  key={group.key}
                  type="button"
                  role="tab"
                  aria-selected={isActive}
                  title={environment ? ENVIRONMENT_STYLE[environment].label : undefined}
                  onClick={() => switchGroup(group)}
                  className={`flex cursor-pointer items-center gap-1.5 rounded border px-2.5 py-0.5 text-sm ${tabClass(group, isActive)}`}
                >
                  {isActive && environment === 'production' && <WarningOutlined />}
                  {group.name}
                </button>
              );
            })}
          </div>
        )
      )}

      <Popover
        open={isOpen}
        onOpenChange={(open) => {
          setIsOpen(open);
          if (!open) setSearch('');
        }}
        trigger="click"
        placement="bottomLeft"
        arrow={false}
        content={dropdown}
        styles={{ container: { padding: 0 } }}
      >
        <button
          type="button"
          aria-haspopup="dialog"
          aria-expanded={isOpen}
          className="flex max-w-[380px] cursor-pointer items-center gap-2 rounded-md border border-gray-300 bg-white px-2.5 py-1 text-sm hover:border-emerald-500 dark:border-gray-600 dark:bg-gray-900 dark:hover:border-emerald-500"
        >
          {selectedSite ? (
            <>
              <Bullet color={colors[selectedSite.id]} />
              <span className="shrink-0 text-gray-700 dark:text-gray-200">
                {siteLabel(selectedSite)}
              </span>
              <span className="text-gray-400">›</span>
              <span className="truncate font-medium text-gray-900 dark:text-gray-100">
                {selectedProject?.name ?? 'Select a project'}
              </span>
              <Heartbeat status={statuses[selectedSite.id]?.status as SiteHealth | undefined} />
            </>
          ) : (
            <span className="text-gray-500">Select a site and project</span>
          )}
          <DownOutlined className="text-[10px] text-gray-400" />
        </button>
      </Popover>
    </div>
  );
};
