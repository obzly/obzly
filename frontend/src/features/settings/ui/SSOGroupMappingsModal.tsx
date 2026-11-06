import { App, Button, Input, Modal, Select, Table } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { projectApi } from '../../../entity/projects/api/projectApi';
import type { ProjectResponse } from '../../../entity/projects/model/ProjectResponse';
import { ssoApi } from '../../../entity/users/api/ssoApi';
import type { SSOConnector } from '../../../entity/users/model/SSOConnector';
import type { SSOGroupMapping } from '../../../entity/users/model/SSOGroupMapping';
import { SSOProviderType } from '../../../entity/users/model/SSOProviderType';

// OWNER is deliberately not offered: ownership only changes by explicit transfer.
const ROLE_OPTIONS = [
  { value: 'PROJECT_ADMIN', label: 'Admin' },
  { value: 'PROJECT_MEMBER', label: 'Member' },
  { value: 'PROJECT_VIEWER', label: 'Viewer' },
];

const GLOBAL_ADMIN_OPTION = '__global_admin__';

const roleLabel = (role: string) =>
  role === 'ADMIN' ? 'Global Admin' : (ROLE_OPTIONS.find((o) => o.value === role)?.label ?? role);

interface Props {
  connector: SSOConnector;
  onClose: () => void;
}

export const SSOGroupMappingsModal = ({ connector, onClose }: Props) => {
  const { message } = App.useApp();

  const [mappings, setMappings] = useState<SSOGroupMapping[]>([]);
  const [projects, setProjects] = useState<ProjectResponse[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);

  const [groupName, setGroupName] = useState('');
  const [projectId, setProjectId] = useState<string | undefined>(undefined);
  const [role, setRole] = useState('PROJECT_MEMBER');

  const load = async () => {
    setIsLoading(true);
    try {
      const [fetchedMappings, fetchedProjects] = await Promise.all([
        ssoApi.listGroupMappings(connector.id),
        projectApi.getProjects(),
      ]);
      setMappings(fetchedMappings);
      setProjects(fetchedProjects.projects);
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to load group mappings');
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [connector.id]);

  const isGlobalAdmin = projectId === GLOBAL_ADMIN_OPTION;

  const projectName = (id: string | null) =>
    id === null ? 'All projects' : (projects.find((p) => p.id === id)?.name ?? id);

  const handleAdd = async () => {
    if (!groupName.trim()) {
      message.error('Group name is required');
      return;
    }
    if (!projectId) {
      message.error('Choose a project or Global Admin');
      return;
    }

    setIsSaving(true);
    try {
      await ssoApi.createGroupMapping(
        connector.id,
        isGlobalAdmin
          ? { groupName: groupName.trim(), projectId: null, role: 'ADMIN' }
          : { groupName: groupName.trim(), projectId, role },
      );
      setGroupName('');
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to add mapping');
    } finally {
      setIsSaving(false);
    }
  };

  const handleDelete = async (mappingId: string) => {
    try {
      await ssoApi.deleteGroupMapping(connector.id, mappingId);
      await load();
    } catch (error: unknown) {
      message.error(error instanceof Error ? error.message : 'Failed to delete mapping');
    }
  };

  const columns: ColumnsType<SSOGroupMapping> = [
    { title: 'Group', dataIndex: 'groupName', key: 'groupName' },
    {
      title: 'Project',
      dataIndex: 'projectId',
      key: 'projectId',
      render: (id: string | null) => projectName(id),
    },
    {
      title: 'Role',
      dataIndex: 'role',
      key: 'role',
      render: (value: string) => roleLabel(value),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, record: SSOGroupMapping) => (
        <Button size="small" danger onClick={() => handleDelete(record.id)}>
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
      width={760}
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
            Enter the Object ID from Entra ID → Groups. Users in more than about 200 groups are
            looked up through Microsoft Graph; if that lookup fails, their access is left unchanged
            for that sign-in.
          </div>
        )}

        <div className="text-xs text-gray-500 dark:text-gray-300">
          A user in a mapped group gets an account on first sign-in even when &quot;Allow external
          registrations&quot; is off. Applied each time a user signs in with this connector. A user
          in several mapped groups gets the highest role per project. Access granted by a group is
          removed at their next sign-in once they leave it. Members you added by hand, and project
          owners, are never changed by mappings. &quot;Global Admin&quot; makes group members admins
          of the whole instance (only the root admin can add or remove such a mapping); they must
          still set up MFA, and only admins granted this way are demoted when they leave the group.
        </div>

        <Table
          dataSource={mappings}
          columns={columns}
          rowKey="id"
          size="small"
          pagination={false}
          loading={isLoading}
          locale={{ emptyText: 'No mappings yet' }}
        />

        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-48 grow">
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
          <div className="min-w-48 grow">
            <label className="mb-1 block text-xs font-medium">Project</label>
            <Select
              className="w-full"
              showSearch
              optionFilterProp="label"
              placeholder="Choose a project"
              value={projectId}
              onChange={setProjectId}
              options={[
                { value: GLOBAL_ADMIN_OPTION, label: 'Global Admin (entire instance)' },
                ...projects.map((p) => ({ value: p.id, label: p.name })),
              ]}
            />
          </div>
          <div className="w-32">
            <label className="mb-1 block text-xs font-medium">Role</label>
            <Select
              className="w-full"
              value={isGlobalAdmin ? 'ADMIN' : role}
              onChange={setRole}
              disabled={isGlobalAdmin}
              options={isGlobalAdmin ? [{ value: 'ADMIN', label: 'Global Admin' }] : ROLE_OPTIONS}
            />
          </div>
          <Button
            type="primary"
            loading={isSaving}
            onClick={handleAdd}
            className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
          >
            Add
          </Button>
        </div>
      </div>
    </Modal>
  );
};
