import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Spin, Tabs } from 'antd';
import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import { type OrgDetail, type Organization, organizationsApi } from '../../../entity/organizations';
import { MembersComponent } from './MembersComponent';
import { PlanComponent } from './PlanComponent';
import { SCIMComponent } from './SCIMComponent';
import { SitesTabComponent } from './SitesTabComponent';
import { TeamsComponent } from './TeamsComponent';
import { errorMessage } from './lib';

interface Props {
  contentHeight: number;
  isAdmin: boolean;
}

export const OrganizationsComponent = ({ contentHeight, isAdmin }: Props) => {
  const { message } = App.useApp();
  const [orgs, setOrgs] = useState<Organization[]>([]);
  const [selected, setSelected] = useState<OrgDetail | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [creating, setCreating] = useState<{ name: string; slug: string } | null>(null);

  const select = async (orgId: string) => {
    try {
      setSelected(await organizationsApi.getOrg(orgId));
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to load the organization'));
    }
  };

  const load = async () => {
    setIsLoading(true);
    try {
      const loaded = await organizationsApi.listOrgs();
      setOrgs(loaded);
      if (loaded.length > 0 && !selected) {
        await select(loaded[0].id);
      }
    } catch {
      // A Site enrolled in a control plane has its organizations there.
      setUnavailable(true);
    } finally {
      setIsLoading(false);
    }
  };

  const create = async () => {
    if (!creating) return;
    try {
      const org = await organizationsApi.createOrg(creating.name, creating.slug);
      setOrgs((prev) => [...prev, org]);
      setCreating(null);
      await select(org.id);
      message.success(`Created ${org.name}`);
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to create the organization'));
    }
  };

  useEffect(() => {
    load();
  }, []);

  const canManage = isAdmin || selected?.myRole === 'ORG_OWNER' || selected?.myRole === 'ORG_ADMIN';

  return (
    <div className="flex grow pl-3">
      <div
        className="w-full overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <div className="mb-4 flex items-center justify-between">
          <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">Organizations</h1>
          {isAdmin && !unavailable && (
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => setCreating({ name: '', slug: '' })}
              className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
            >
              New organization
            </Button>
          )}
        </div>
        {isLoading ? (
          <Spin indicator={<LoadingOutlined spin />} />
        ) : unavailable ? (
          <div className="text-gray-500 dark:text-gray-300">
            Organizations are managed on the control plane this Site is enrolled in.
          </div>
        ) : orgs.length === 0 ? (
          <div className="text-gray-500 dark:text-gray-300">
            You aren&apos;t in any organization.
          </div>
        ) : (
          <div className="flex gap-6">
            <div className="w-56 shrink-0">
              {orgs.map((org) => (
                <div
                  key={org.id}
                  onClick={() => select(org.id)}
                  className={`mb-1 cursor-pointer rounded px-3 py-2 ${selected?.id === org.id ? 'bg-emerald-50 text-emerald-700 dark:bg-gray-700 dark:text-emerald-300' : 'text-gray-800 hover:bg-gray-50 dark:text-gray-200 dark:hover:bg-gray-700'}`}
                >
                  <div className="font-medium">{org.name}</div>
                  <div className="text-xs text-gray-500 dark:text-gray-400">
                    {org.myRole ? org.myRole.replace('ORG_', '').toLowerCase() : 'admin'}
                  </div>
                </div>
              ))}
            </div>
            {selected && (
              <div className="min-w-0 grow">
                <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-100">
                  {selected.name}{' '}
                  <span className="text-sm font-normal text-gray-500 dark:text-gray-400">
                    {selected.slug}
                  </span>
                </h2>
                <Tabs
                  key={selected.id}
                  destroyInactiveTabPane
                  items={[
                    {
                      key: 'members',
                      label: 'Members',
                      children: (
                        <MembersComponent
                          org={selected}
                          canManage={canManage}
                          canMakeOwners={isAdmin || selected.myRole === 'ORG_OWNER'}
                        />
                      ),
                    },
                    {
                      key: 'teams',
                      label: 'Teams',
                      children: <TeamsComponent org={selected} canManage={canManage} />,
                    },
                    ...(isAdmin || selected.myRole === 'ORG_OWNER'
                      ? [
                          {
                            key: 'scim',
                            label: 'SCIM',
                            children: <SCIMComponent org={selected} />,
                          },
                        ]
                      : []),
                    {
                      key: 'plan',
                      label: 'Plan',
                      children: <PlanComponent org={selected} canEdit={isAdmin} />,
                    },
                    ...(IS_CONTROL_PLANE
                      ? [
                          {
                            key: 'sites',
                            label: 'Sites',
                            children: (
                              <SitesTabComponent
                                org={selected}
                                isAdmin={isAdmin}
                                onChanged={() => select(selected.id)}
                              />
                            ),
                          },
                        ]
                      : []),
                  ]}
                />
              </div>
            )}
          </div>
        )}
      </div>
      {creating && (
        <Modal
          open
          title="New organization"
          onCancel={() => setCreating(null)}
          onOk={create}
          okText="Create"
          okButtonProps={{ disabled: !creating.name.trim() || !creating.slug.trim() }}
        >
          <div className="space-y-3">
            <Input
              placeholder="Name"
              value={creating.name}
              onChange={(e) => setCreating({ ...creating, name: e.target.value })}
            />
            <Input
              placeholder="slug (lowercase letters, digits, dashes)"
              value={creating.slug}
              onChange={(e) => setCreating({ ...creating, slug: e.target.value })}
            />
          </div>
        </Modal>
      )}
    </div>
  );
};
