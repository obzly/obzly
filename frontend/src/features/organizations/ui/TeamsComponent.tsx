import { CloseOutlined, DeleteOutlined, LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Popconfirm, Select, Spin, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import { type OrgDetail, type Team, organizationsApi } from '../../../entity/organizations';
import { projectApi } from '../../../entity/projects';
import { Card } from '../../../shared/ui/v2';
import { AVATAR_CLASS, NOTE_CLASS, WARN_CLASS, errorMessage, initialsOf, pillClass } from './lib';

interface Props {
  org: OrgDetail;
  canManage: boolean;
}

interface GrantDraft {
  team: Team;
  siteId: string;
  projectId: string | null;
  role: string;
}

const SITE_ROLES = ['ADMIN', 'MEMBER', 'VIEWER'];
const LOCAL_ROLES = ['MEMBER', 'VIEWER'];
const PROJECT_ROLES = ['PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'];

const roleLabel = (role: string) => role.replace('PROJECT_', '').toLowerCase();

export const TeamsComponent = ({ org, canManage }: Props) => {
  const { message } = App.useApp();
  const [teams, setTeams] = useState<Team[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [newTeam, setNewTeam] = useState('');
  const [memberEmail, setMemberEmail] = useState<Record<string, string>>({});
  const [grant, setGrant] = useState<GrantDraft | null>(null);
  const [projects, setProjects] = useState<{ id: string; name: string }[]>([]);
  const [projectNames, setProjectNames] = useState<Record<string, string>>({});

  const load = async () => {
    setIsLoading(true);
    try {
      setTeams(await organizationsApi.listTeams(org.id));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load teams'));
    } finally {
      setIsLoading(false);
    }
  };

  const run = async (action: () => Promise<unknown>, success: string) => {
    try {
      await action();
      message.success(success);
      await load();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'The change failed'));
    }
  };

  // The projects a grant can target: this instance's, or a Site's.
  const loadProjects = async (siteId: string) => {
    setProjects([]);
    try {
      const response = IS_CONTROL_PLANE
        ? await projectApi.getProjectsOnSite(siteId)
        : await projectApi.getProjects();
      const list = response.projects.map((p) => ({ id: p.id, name: p.name }));
      setProjects(list);
      setProjectNames((prev) => ({
        ...prev,
        ...Object.fromEntries(list.map((p) => [p.id, p.name])),
      }));
    } catch {
      setProjects([]);
    }
  };

  const openGrant = (team: Team) => {
    const siteId = IS_CONTROL_PLANE ? (org.sites[0] ?? '') : '';
    setGrant({ team, siteId, projectId: null, role: 'VIEWER' });
    if (!IS_CONTROL_PLANE || siteId) {
      loadProjects(siteId);
    }
  };

  useEffect(() => {
    load();
    if (!IS_CONTROL_PLANE) {
      loadProjects('');
    }
  }, [org.id]);

  if (isLoading) {
    return <Spin indicator={<LoadingOutlined spin />} />;
  }

  const describeGrant = (g: Team['grants'][number]) => {
    const where = IS_CONTROL_PLANE ? g.siteId : 'this instance';
    const project = g.projectId
      ? (projectNames[g.projectId] ?? g.projectId.slice(0, 8))
      : 'all projects';
    return `${roleLabel(g.role)} on ${project}${IS_CONTROL_PLANE ? ` · ${where}` : ''}`;
  };

  const chip = (
    key: string,
    text: string,
    tone: 'ok' | 'neutral',
    onRemove?: () => void,
    removeLabel?: string,
  ) => (
    <span key={key} className={`${pillClass(tone)} max-w-full py-1`}>
      <span className="truncate">{text}</span>
      {onRemove && (
        <button
          type="button"
          aria-label={removeLabel}
          onClick={onRemove}
          className="-mr-0.5 cursor-pointer border-0 bg-transparent p-0 leading-none text-gray-500 hover:text-[var(--v2-red)] dark:text-gray-400"
        >
          <CloseOutlined className="text-[10px]" />
        </button>
      )}
    </span>
  );

  return (
    <div className="flex flex-col gap-4">
      <div className={NOTE_CLASS}>
        A team&apos;s roles apply to every member; a user gets the strongest of their own and their
        teams&apos; roles.
      </div>
      {canManage && (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="New team name"
            value={newTeam}
            onChange={(e) => setNewTeam(e.target.value)}
            className="min-w-0 flex-[1_1_220px]"
            style={{ maxWidth: 300 }}
          />
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={!newTeam.trim()}
            onClick={() =>
              run(async () => {
                await organizationsApi.createTeam(org.id, newTeam.trim());
                setNewTeam('');
              }, 'Team created')
            }
          >
            Create team
          </Button>
        </div>
      )}
      {teams.length === 0 && (
        <div className="text-sm text-gray-500 dark:text-gray-400">No teams yet</div>
      )}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(300px,1fr))] gap-4">
        {teams.map((team) => (
          <Card
            key={team.id}
            title={team.name}
            subtitle={`${team.members.length} ${team.members.length === 1 ? 'person' : 'people'}`}
            extra={
              canManage && (
                <Popconfirm
                  title={`Delete ${team.name}? Its members lose its roles.`}
                  okText="Delete"
                  okButtonProps={{ danger: true }}
                  onConfirm={() =>
                    run(() => organizationsApi.deleteTeam(org.id, team.id), 'Team deleted')
                  }
                >
                  <Tooltip title="Delete team">
                    <Button
                      size="small"
                      type="text"
                      danger
                      icon={<DeleteOutlined />}
                      aria-label={`Delete ${team.name}`}
                    />
                  </Tooltip>
                </Popconfirm>
              )
            }
            bodyClassName="flex flex-col gap-3"
          >
            {team.members.length > 0 && (
              <div className="flex -space-x-1.5" aria-hidden="true">
                {team.members.slice(0, 6).map((m) => (
                  <span
                    key={m.userId}
                    title={m.email}
                    className={`${AVATAR_CLASS} ring-2 ring-white dark:ring-gray-800`}
                  >
                    {initialsOf(m.name, m.email)}
                  </span>
                ))}
                {team.members.length > 6 && (
                  <span className="flex h-8 w-8 items-center justify-center rounded-full bg-gray-100 text-xs text-gray-600 ring-2 ring-white dark:bg-gray-700 dark:text-gray-300 dark:ring-gray-800">
                    +{team.members.length - 6}
                  </span>
                )}
              </div>
            )}
            <div className="flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
              <div className="text-xs font-medium text-gray-500 dark:text-gray-400">Members</div>
              <div className="flex flex-wrap gap-1.5">
                {team.members.map((m) =>
                  chip(
                    m.userId,
                    m.email,
                    'neutral',
                    canManage
                      ? () =>
                          run(
                            () => organizationsApi.removeTeamMember(org.id, team.id, m.userId),
                            `${m.email} removed from ${team.name}`,
                          )
                      : undefined,
                    `Remove ${m.email} from ${team.name}`,
                  ),
                )}
                {team.members.length === 0 && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">none</span>
                )}
              </div>
              {canManage && (
                <div className="flex gap-2">
                  <Input
                    size="small"
                    placeholder="Add by email"
                    value={memberEmail[team.id] ?? ''}
                    onChange={(e) => setMemberEmail({ ...memberEmail, [team.id]: e.target.value })}
                    className="min-w-0 flex-1"
                  />
                  <Button
                    size="small"
                    disabled={!memberEmail[team.id]?.trim()}
                    onClick={() =>
                      run(async () => {
                        await organizationsApi.addTeamMember(
                          org.id,
                          team.id,
                          memberEmail[team.id].trim(),
                        );
                        setMemberEmail({ ...memberEmail, [team.id]: '' });
                      }, 'Member added')
                    }
                  >
                    Add
                  </Button>
                </div>
              )}
            </div>
            <div className="flex flex-col gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
              <div className="text-xs font-medium text-gray-500 dark:text-gray-400">Roles</div>
              <div className="flex flex-wrap items-center gap-1.5">
                {team.grants.map((g) =>
                  chip(
                    g.id,
                    describeGrant(g),
                    'ok',
                    canManage
                      ? () =>
                          run(
                            () => organizationsApi.removeGrant(org.id, team.id, g.id),
                            'Role removed',
                          )
                      : undefined,
                    `Remove role ${describeGrant(g)}`,
                  ),
                )}
                {team.grants.length === 0 && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">none</span>
                )}
                {canManage && (
                  <Button size="small" icon={<PlusOutlined />} onClick={() => openGrant(team)}>
                    Add role
                  </Button>
                )}
              </div>
            </div>
          </Card>
        ))}
      </div>
      {grant && (
        <Modal
          open
          title={`Give ${grant.team.name} a role`}
          onCancel={() => setGrant(null)}
          okText="Add"
          okButtonProps={{ disabled: IS_CONTROL_PLANE && !grant.siteId }}
          onOk={() =>
            run(async () => {
              await organizationsApi.addGrant(org.id, grant.team.id, {
                siteId: grant.siteId,
                projectId: grant.projectId,
                role: grant.role,
              });
              setGrant(null);
            }, 'Role added')
          }
        >
          <div className="flex flex-col gap-3 pt-2">
            {IS_CONTROL_PLANE &&
              (org.sites.length === 0 ? (
                <div className={WARN_CLASS} role="alert">
                  This organization has no Sites yet (Obzly settings → Sites).
                </div>
              ) : (
                <Select
                  aria-label="Site"
                  value={grant.siteId}
                  onChange={(siteId) => {
                    setGrant({ ...grant, siteId, projectId: null });
                    loadProjects(siteId);
                  }}
                  style={{ width: '100%' }}
                  options={org.sites.map((s) => ({ value: s, label: s }))}
                />
              ))}
            <Select
              aria-label="Project"
              value={grant.projectId ?? ''}
              onChange={(value) =>
                setGrant({
                  ...grant,
                  projectId: value || null,
                  role: value ? 'PROJECT_VIEWER' : 'VIEWER',
                })
              }
              style={{ width: '100%' }}
              options={[
                { value: '', label: 'Every project' },
                ...projects.map((p) => ({ value: p.id, label: p.name })),
              ]}
            />
            <Select
              aria-label="Role"
              value={grant.role}
              onChange={(role) => setGrant({ ...grant, role })}
              style={{ width: '100%' }}
              options={(grant.projectId
                ? PROJECT_ROLES
                : IS_CONTROL_PLANE
                  ? SITE_ROLES
                  : LOCAL_ROLES
              ).map((role) => ({ value: role, label: roleLabel(role) }))}
            />
          </div>
        </Modal>
      )}
    </div>
  );
};
