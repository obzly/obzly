// Who can reach one Site, and at what role. Central ADMIN only.
import { DeleteOutlined, LoadingOutlined } from '@ant-design/icons';
import { App, Button, Input, Select, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';

import { projectApi } from '../../../entity/projects';
import { type SiteAdmin, type SiteGrant, grantsApi } from '../../../entity/sites';
import { userManagementApi } from '../../../entity/users';

interface Props {
  site: SiteAdmin;
}

// Site-wide roles take no project; MEMBER / VIEWER apply to every project,
// including ones created later (picked up at the next Site sign-in).
const SITE_WIDE_ROLE_LABELS: Record<string, string> = {
  ADMIN: 'Site ADMIN',
  MEMBER: 'Site member',
  VIEWER: 'Site viewer',
};

const ROLE_OPTIONS = [
  {
    label: 'Whole Site',
    options: [
      { value: 'ADMIN', label: 'Site ADMIN (global)' },
      { value: 'MEMBER', label: 'Site member (all projects)' },
      { value: 'VIEWER', label: 'Site viewer (all projects)' },
    ],
  },
  {
    label: 'One project',
    options: [
      { value: 'PROJECT_ADMIN', label: 'Project admin' },
      { value: 'PROJECT_MEMBER', label: 'Project member' },
      { value: 'PROJECT_VIEWER', label: 'Project viewer' },
    ],
  },
];

const isSiteWideRole = (role: string) => role in SITE_WIDE_ROLE_LABELS;

export function SiteGrantsComponent({ site }: Props) {
  const { message } = App.useApp();
  const [grants, setGrants] = useState<SiteGrant[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  const [userOptions, setUserOptions] = useState<{ value: string; label: string }[]>([]);
  const [userQuery, setUserQuery] = useState('');
  const [selectedUserId, setSelectedUserId] = useState<string | undefined>(undefined);
  const [role, setRole] = useState<string>('PROJECT_VIEWER');
  const [projectId, setProjectId] = useState('');
  const [isSaving, setIsSaving] = useState(false);

  // Listed with your own access to the Site (the control plane has no
  // Site-wide credential); without it, fall back to pasting a project ID.
  const [siteProjects, setSiteProjects] = useState<{ id: string; name: string }[] | null>(null);
  const [projectsError, setProjectsError] = useState<string | null>(null);
  const [isProjectsLoading, setIsProjectsLoading] = useState(false);
  const [isManualProjectId, setIsManualProjectId] = useState(false);

  const loadSiteProjects = async () => {
    setIsProjectsLoading(true);
    setProjectsError(null);
    try {
      const result = await projectApi.getProjectsOnSite(site.id);
      setSiteProjects(
        result.projects
          .map((p) => ({ id: p.id, name: p.name }))
          .sort((a, b) => a.name.localeCompare(b.name)),
      );
    } catch (e) {
      setSiteProjects(null);
      setProjectsError((e as Error).message);
    }
    setIsProjectsLoading(false);
  };

  useEffect(() => {
    setSiteProjects(null);
    setProjectsError(null);
    setIsManualProjectId(false);
    loadSiteProjects();
  }, [site.id]);

  const projectNames = useMemo(
    () => new Map((siteProjects ?? []).map((p) => [p.id, p.name])),
    [siteProjects],
  );
  const showManualProjectId = isManualProjectId || (!isProjectsLoading && siteProjects === null);

  const loadGrants = async () => {
    setIsLoading(true);
    try {
      const result = await grantsApi.listForSite(site.id);
      setGrants(result);
    } catch (e) {
      message.error((e as Error).message);
    }
    setIsLoading(false);
  };

  useEffect(() => {
    loadGrants();
  }, [site.id]);

  useEffect(() => {
    const timer = setTimeout(async () => {
      try {
        const result = await userManagementApi.getUsers({ query: userQuery, limit: 20 });
        setUserOptions(result.users.map((u) => ({ value: u.id, label: `${u.name} <${u.email}>` })));
      } catch {
        /* a failed search just leaves the previous options - not worth a toast */
      }
    }, 300);
    return () => clearTimeout(timer);
  }, [userQuery]);

  const handleAddGrant = async () => {
    if (!selectedUserId) {
      message.error('Select a user first');
      return;
    }
    if (!isSiteWideRole(role) && !projectId.trim()) {
      message.error('A project role needs a project id');
      return;
    }

    setIsSaving(true);
    try {
      await grantsApi.createGrant({
        userId: selectedUserId,
        siteId: site.id,
        role,
        projectId: isSiteWideRole(role) ? null : projectId.trim(),
      });
      message.success('Grant added');
      setSelectedUserId(undefined);
      setProjectId('');
      await loadGrants();
      // The grant may be your own, which can make the project list loadable.
      if (siteProjects === null || isSiteWideRole(role)) await loadSiteProjects();
    } catch (e) {
      message.error((e as Error).message);
    }
    setIsSaving(false);
  };

  const handleDelete = async (grant: SiteGrant) => {
    try {
      await grantsApi.deleteGrant(grant.id);
      message.success('Grant removed');
      await loadGrants();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const columns: ColumnsType<SiteGrant> = [
    { title: 'User', dataIndex: 'userEmail', key: 'userEmail' },
    {
      title: 'Access',
      key: 'access',
      render: (_, grant) => {
        // Removing an SSO-synced grant only lasts until the user's next SSO sign-in.
        const ssoTag = grant.ssoManaged ? (
          <Tag color="blue" className="ml-2">
            via SSO group
          </Tag>
        ) : null;
        if (!grant.projectId) {
          return (
            <span>
              {SITE_WIDE_ROLE_LABELS[grant.role] ?? grant.role}
              {ssoTag}
            </span>
          );
        }
        const name = projectNames.get(grant.projectId);
        return (
          <span>
            {grant.role} on {name ? <b>{name}</b> : 'project'}{' '}
            <span className="font-mono text-xs text-gray-400">{grant.projectId}</span>
            {ssoTag}
          </span>
        );
      },
    },
    {
      title: '',
      key: 'actions',
      width: 60,
      render: (_, grant) => (
        <Button type="text" danger icon={<DeleteOutlined />} onClick={() => handleDelete(grant)} />
      ),
    },
  ];

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-end gap-2">
        <div className="w-[260px]">
          <div className="mb-1 text-xs text-gray-500">User</div>
          <Select
            showSearch
            filterOption={false}
            value={selectedUserId}
            onSearch={setUserQuery}
            onChange={setSelectedUserId}
            options={userOptions}
            placeholder="Search by name or email"
            className="w-full"
          />
        </div>
        <div className="w-[200px]">
          <div className="mb-1 text-xs text-gray-500">Role</div>
          <Select value={role} onChange={setRole} options={ROLE_OPTIONS} className="w-full" />
        </div>
        {!isSiteWideRole(role) && (
          <div className="w-[280px]">
            <div className="mb-1 flex items-center justify-between text-xs text-gray-500">
              <span>Project (on this Site)</span>
              {siteProjects !== null && (
                <button
                  type="button"
                  className="text-emerald-600 hover:underline"
                  onClick={() => {
                    setIsManualProjectId((m) => !m);
                    setProjectId('');
                  }}
                >
                  {isManualProjectId ? 'Pick from list' : 'Enter ID'}
                </button>
              )}
            </div>
            {showManualProjectId ? (
              <Input
                value={projectId}
                onChange={(e) => setProjectId(e.target.value)}
                placeholder="Project ID (UUID)"
              />
            ) : (
              <Select
                showSearch
                loading={isProjectsLoading}
                value={projectId || undefined}
                onChange={(value: string) => setProjectId(value)}
                placeholder={isProjectsLoading ? 'Loading projects...' : 'Select a project'}
                className="w-full"
                optionFilterProp="label"
                notFoundContent="No projects on this Site"
                options={(siteProjects ?? []).map((p) => ({
                  value: p.id,
                  label: p.name,
                }))}
                optionRender={(option) => (
                  <div className="flex flex-col">
                    <span>{option.label}</span>
                    <span className="font-mono text-[10px] text-gray-400">{option.value}</span>
                  </div>
                )}
              />
            )}
          </div>
        )}
        <Button type="primary" loading={isSaving} onClick={handleAddGrant}>
          Add grant
        </Button>
      </div>

      {!isSiteWideRole(role) && projectsError && (
        <div className="-mt-2 mb-4 text-xs text-gray-500">
          Couldn&apos;t list this Site&apos;s projects ({projectsError}). The list uses your own
          access to the Site, so grant yourself <b>Site ADMIN</b> here first (then{' '}
          <button
            type="button"
            className="text-emerald-600 hover:underline"
            onClick={loadSiteProjects}
          >
            retry
          </button>
          ), or paste the project&apos;s ID (ask the Site&apos;s admin for it).
        </div>
      )}

      {isLoading ? (
        <div className="flex justify-center py-6">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <Table<SiteGrant>
          rowKey="id"
          columns={columns}
          dataSource={grants}
          pagination={false}
          size="small"
        />
      )}
    </div>
  );
}
