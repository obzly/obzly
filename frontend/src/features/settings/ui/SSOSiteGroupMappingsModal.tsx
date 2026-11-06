import { App, Button, Input, Modal, Select, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import {
  type SSOSiteGroupMapping,
  type SiteAdmin,
  sitesApi,
  ssoSiteMappingsApi,
} from '../../../entity/sites';
import { ssoApi } from '../../../entity/users/api/ssoApi';
import type { SSOConnector } from '../../../entity/users/model/SSOConnector';
import { SSOProviderType } from '../../../entity/users/model/SSOProviderType';

const CONTROL_PLANE_ADMIN = '__control_plane_admin__';

const SITE_WIDE_ROLES: Record<string, string> = {
  ADMIN: 'Site ADMIN',
  MEMBER: 'Site member (all projects)',
  VIEWER: 'Site viewer (all projects)',
};
const PROJECT_ROLES: Record<string, string> = {
  PROJECT_ADMIN: 'Project admin',
  PROJECT_MEMBER: 'Project member',
  PROJECT_VIEWER: 'Project viewer',
};
const ROLE_OPTIONS = [
  {
    label: 'Whole Site',
    options: Object.entries(SITE_WIDE_ROLES).map(([value, label]) => ({ value, label })),
  },
  {
    label: 'One project',
    options: Object.entries(PROJECT_ROLES).map(([value, label]) => ({ value, label })),
  },
];
const isSiteWideRole = (role: string) => role in SITE_WIDE_ROLES;

type Row =
  | { kind: 'controlPlaneAdmin'; id: string; groupName: string }
  | ({ kind: 'site' } & SSOSiteGroupMapping);

interface Props {
  connector: SSOConnector;
  onClose: () => void;
}

/**
 * Control-plane SSO group mappings: groups map to Site access, applied as
 * grants at each SSO sign-in. A control plane has no projects of its own.
 */
export const SSOSiteGroupMappingsModal = ({ connector, onClose }: Props) => {
  const { message } = App.useApp();

  const [rows, setRows] = useState<Row[]>([]);
  const [sites, setSites] = useState<SiteAdmin[]>([]);
  // siteId -> (projectId -> name), filled lazily via each Site's proxy.
  const [projectNames, setProjectNames] = useState<Record<string, Record<string, string>>>({});
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  const [groupName, setGroupName] = useState('');
  const [target, setTarget] = useState<string | undefined>(undefined);
  const [role, setRole] = useState('VIEWER');
  const [projectId, setProjectId] = useState('');
  const [siteProjects, setSiteProjects] = useState<{ id: string; name: string }[] | null>(null);
  const [projectsError, setProjectsError] = useState<string | null>(null);
  const [isManualProjectId, setIsManualProjectId] = useState(false);

  const load = async () => {
    setIsLoading(true);
    try {
      const [globalMappings, siteMappings, allSites] = await Promise.all([
        ssoApi.listGroupMappings(connector.id),
        ssoSiteMappingsApi.list(connector.id),
        sitesApi.listSitesAdmin(),
      ]);
      setSites(allSites);
      setRows([
        ...globalMappings
          .filter((m) => m.projectId === null && m.role === 'ADMIN')
          .map((m): Row => ({ kind: 'controlPlaneAdmin', id: m.id, groupName: m.groupName })),
        ...siteMappings.map((m): Row => ({ kind: 'site', ...m })),
      ]);
      // Best effort: naming needs your own access to the Site; ids show otherwise.
      const siteIds = [...new Set(siteMappings.filter((m) => m.projectId).map((m) => m.siteId))];
      for (const siteId of siteIds) {
        projectApi
          .getProjectsOnSite(siteId)
          .then((r) =>
            setProjectNames((prev) => ({
              ...prev,
              [siteId]: Object.fromEntries(r.projects.map((p) => [p.id, p.name])),
            })),
          )
          .catch(() => undefined);
      }
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to load group mappings');
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [connector.id]);

  const isControlPlaneAdmin = target === CONTROL_PLANE_ADMIN;
  const siteId = target && !isControlPlaneAdmin ? target : undefined;
  const needsProject = !!siteId && !isSiteWideRole(role);

  useEffect(() => {
    setProjectId('');
    setSiteProjects(null);
    setProjectsError(null);
    setIsManualProjectId(false);
    if (!siteId) return;
    projectApi
      .getProjectsOnSite(siteId)
      .then((r) =>
        setSiteProjects(
          r.projects
            .map((p) => ({ id: p.id, name: p.name }))
            .sort((a, b) => a.name.localeCompare(b.name)),
        ),
      )
      .catch((e: unknown) => setProjectsError((e as Error).message));
  }, [siteId]);

  const siteName = useMemo(() => new Map(sites.map((s) => [s.id, s.name])), [sites]);

  const handleAdd = async () => {
    if (!groupName.trim()) {
      message.error('Group name is required');
      return;
    }
    if (!target) {
      message.error('Choose a Site, or Control plane admin');
      return;
    }
    if (needsProject && !projectId.trim()) {
      message.error('Choose a project for a project role');
      return;
    }

    setIsSaving(true);
    try {
      if (isControlPlaneAdmin) {
        await ssoApi.createGroupMapping(connector.id, {
          groupName: groupName.trim(),
          projectId: null,
          role: 'ADMIN',
        });
      } else {
        await ssoSiteMappingsApi.create(connector.id, {
          groupName: groupName.trim(),
          siteId: siteId as string,
          projectId: needsProject ? projectId.trim() : null,
          role,
        });
      }
      setGroupName('');
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to add mapping');
    } finally {
      setIsSaving(false);
    }
  };

  const handleDelete = async (row: Row) => {
    try {
      if (row.kind === 'controlPlaneAdmin') await ssoApi.deleteGroupMapping(connector.id, row.id);
      else await ssoSiteMappingsApi.delete(connector.id, row.id);
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to delete mapping');
    }
  };

  const columns: ColumnsType<Row> = [
    { title: 'Group', dataIndex: 'groupName', key: 'groupName' },
    {
      title: 'Access',
      key: 'access',
      render: (_: unknown, row: Row) => {
        if (row.kind === 'controlPlaneAdmin') return <Tag color="gold">Control plane admin</Tag>;
        const site = siteName.get(row.siteId) ?? row.siteId;
        if (!row.projectId) {
          return (
            <span>
              <b>{site}</b> - {SITE_WIDE_ROLES[row.role] ?? row.role}
            </span>
          );
        }
        const project = projectNames[row.siteId]?.[row.projectId];
        return (
          <span>
            <b>{site}</b> / {project ?? <span className="font-mono text-xs">{row.projectId}</span>}{' '}
            - {PROJECT_ROLES[row.role] ?? row.role}
          </span>
        );
      },
    },
    {
      title: '',
      key: 'actions',
      width: 80,
      render: (_: unknown, row: Row) => (
        <Button size="small" danger onClick={() => handleDelete(row)}>
          Delete
        </Button>
      ),
    },
  ];

  return (
    <Modal
      title={`Group mappings - ${connector.name}`}
      open
      onCancel={onClose}
      footer={<Button onClick={onClose}>Close</Button>}
      width={820}
    >
      <div className="space-y-3">
        {!connector.groupsClaim && (
          <div className="rounded border border-amber-300 bg-amber-50 p-3 text-xs text-amber-800">
            No groups claim is set on this connector, so these mappings are ignored. Edit the
            connector and set it (AWS Cognito uses <code>cognito:groups</code>).
          </div>
        )}

        {connector.providerType === SSOProviderType.ENTRA_ID && (
          <div className="rounded border border-blue-200 bg-blue-50 p-3 text-xs text-blue-800">
            Entra ID sends each group&apos;s <strong>Object ID</strong> (a GUID), not its name.
            Enter the Object ID from Entra ID → Groups.
          </div>
        )}

        <div className="text-xs text-gray-500 dark:text-gray-300">
          On a control plane, groups map to <b>Sites</b>: a whole-Site role (Site ADMIN, member or
          viewer) or a role on one of the Site&apos;s projects. A user in a mapped group gets an
          account on first sign-in even when &quot;Allow external registrations&quot; is off.
          Applied each time a user signs in with this connector, as Site grants: several groups
          mapping the same Site or project give the highest role; leaving a group removes that
          access at the next sign-in. Grants you added by hand under Sites → Grants are never
          changed. &quot;Control plane admin&quot; makes group members admins of this control plane
          (only the root admin can add or remove it) - it gives no Site access by itself.
        </div>

        <Table
          dataSource={rows}
          columns={columns}
          rowKey="id"
          size="small"
          pagination={false}
          loading={isLoading}
          locale={{ emptyText: 'No mappings yet' }}
        />

        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-44 grow">
            <label className="mb-1 block text-xs font-medium">Group name</label>
            <Input
              placeholder={
                connector.providerType === SSOProviderType.ENTRA_ID
                  ? 'Group Object ID (GUID)'
                  : 'e.g. obzly-viewers'
              }
              value={groupName}
              onChange={(e) => setGroupName(e.target.value)}
              onPressEnter={handleAdd}
            />
          </div>
          <div className="w-52">
            <label className="mb-1 block text-xs font-medium">Site</label>
            <Select
              className="w-full"
              showSearch
              optionFilterProp="label"
              placeholder="Choose a Site"
              value={target}
              onChange={setTarget}
              options={[
                { value: CONTROL_PLANE_ADMIN, label: 'Control plane admin' },
                ...sites.map((s) => ({ value: s.id, label: `${s.name} (${s.id})` })),
              ]}
            />
          </div>
          {siteId && (
            <div className="w-56">
              <label className="mb-1 block text-xs font-medium">Role</label>
              <Select className="w-full" value={role} onChange={setRole} options={ROLE_OPTIONS} />
            </div>
          )}
          {needsProject && (
            <div className="w-60">
              <label className="mb-1 flex items-center justify-between text-xs font-medium">
                <span>Project</span>
                {siteProjects !== null && (
                  <button
                    type="button"
                    className="font-normal text-emerald-600 hover:underline"
                    onClick={() => {
                      setIsManualProjectId((m) => !m);
                      setProjectId('');
                    }}
                  >
                    {isManualProjectId ? 'Pick from list' : 'Enter ID'}
                  </button>
                )}
              </label>
              {isManualProjectId || siteProjects === null ? (
                <Input
                  placeholder={projectsError ? 'Project ID (UUID)' : 'Loading projects...'}
                  value={projectId}
                  onChange={(e) => setProjectId(e.target.value)}
                  disabled={siteProjects === null && !projectsError}
                />
              ) : (
                <Select
                  className="w-full"
                  showSearch
                  optionFilterProp="label"
                  placeholder="Select a project"
                  value={projectId || undefined}
                  onChange={(v: string) => setProjectId(v)}
                  notFoundContent="No projects on this Site"
                  options={siteProjects.map((p) => ({ value: p.id, label: p.name }))}
                />
              )}
            </div>
          )}
          <Button
            type="primary"
            loading={isSaving}
            onClick={handleAdd}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Add
          </Button>
        </div>
        {needsProject && projectsError && (
          <div className="text-xs text-gray-500 dark:text-gray-400">
            Couldn&apos;t list this Site&apos;s projects ({projectsError}). The list uses your own
            access to the Site - grant yourself access under Sites → Grants, or paste the
            project&apos;s ID (shown on its Project settings page).
          </div>
        )}
      </div>
    </Modal>
  );
};
