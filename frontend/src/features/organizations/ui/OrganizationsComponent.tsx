import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Spin } from 'antd';
import { type KeyboardEvent, type ReactNode, useEffect, useRef, useState } from 'react';

import { IS_CONTROL_PLANE } from '../../../constants';
import { type OrgDetail, type Organization, organizationsApi } from '../../../entity/organizations';
import { PageHeader } from '../../../shared/ui/v2';
import { MembersComponent } from './MembersComponent';
import { PlanComponent } from './PlanComponent';
import { SCIMComponent } from './SCIMComponent';
import { SitesTabComponent } from './SitesTabComponent';
import { TeamsComponent } from './TeamsComponent';
import { NOTE_CLASS, errorMessage } from './lib';

interface Props {
  contentHeight: number;
  isAdmin: boolean;
}

type View = 'members' | 'teams' | 'scim' | 'plan' | 'sites';

const orgTile = (name: string, large = false) => (
  <span
    aria-hidden="true"
    className={`flex shrink-0 items-center justify-center rounded-[10px] bg-[var(--v2-ok-soft)] font-semibold text-[var(--v2-accent)] ${large ? 'h-11 w-11 text-lg' : 'h-8 w-8 text-sm'}`}
  >
    {(name.trim()[0] ?? '?').toUpperCase()}
  </span>
);

export const OrganizationsComponent = ({ contentHeight, isAdmin }: Props) => {
  const { message } = App.useApp();
  const [orgs, setOrgs] = useState<Organization[]>([]);
  const [selected, setSelected] = useState<OrgDetail | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [creating, setCreating] = useState<{ name: string; slug: string } | null>(null);
  const [view, setView] = useState<View>('members');
  const selectedIdRef = useRef<string | null>(null);
  const tabRefs = useRef<Partial<Record<View, HTMLButtonElement | null>>>({});

  const select = async (orgId: string) => {
    try {
      const org = await organizationsApi.getOrg(orgId);
      // Another organization opens on its first view; a refresh keeps the current one.
      if (selectedIdRef.current !== org.id) setView('members');
      selectedIdRef.current = org.id;
      setSelected(org);
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

  const views: { key: View; label: string; render: (org: OrgDetail) => ReactNode }[] = selected
    ? [
        {
          key: 'members',
          label: 'Members',
          render: (org) => (
            <MembersComponent
              org={org}
              canManage={canManage}
              canMakeOwners={isAdmin || org.myRole === 'ORG_OWNER'}
            />
          ),
        },
        {
          key: 'teams',
          label: 'Teams',
          render: (org) => <TeamsComponent org={org} canManage={canManage} />,
        },
        ...(isAdmin || selected.myRole === 'ORG_OWNER'
          ? [
              {
                key: 'scim' as const,
                label: 'SCIM',
                render: (org: OrgDetail) => <SCIMComponent org={org} />,
              },
            ]
          : []),
        {
          key: 'plan',
          label: 'Plan',
          render: (org) => <PlanComponent org={org} canEdit={isAdmin} />,
        },
        ...(IS_CONTROL_PLANE
          ? [
              {
                key: 'sites' as const,
                label: 'Sites',
                render: (org: OrgDetail) => (
                  <SitesTabComponent org={org} isAdmin={isAdmin} onChanged={() => select(org.id)} />
                ),
              },
            ]
          : []),
      ]
    : [];
  const active = views.find((v) => v.key === view) ?? views[0];

  // Arrow, Home and End keys move between tabs, as in a standard tablist.
  const onTabKey = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const last = views.length - 1;
    const target =
      e.key === 'ArrowRight'
        ? index === last
          ? 0
          : index + 1
        : e.key === 'ArrowLeft'
          ? index === 0
            ? last
            : index - 1
          : e.key === 'Home'
            ? 0
            : e.key === 'End'
              ? last
              : -1;
    if (target < 0) return;
    e.preventDefault();
    setView(views[target].key);
    tabRefs.current[views[target].key]?.focus();
  };

  return (
    <div className="flex grow">
      <div
        className="w-full overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
        style={{ height: contentHeight }}
      >
        <div className="flex flex-col gap-5">
          <PageHeader
            title="Organizations"
            subtitle="Groups of people with a plan, teams and the Sites they can use"
            actions={
              isAdmin &&
              !unavailable && (
                <Button
                  type="primary"
                  icon={<PlusOutlined />}
                  onClick={() => setCreating({ name: '', slug: '' })}
                  className="border-accent-600 bg-accent-600 hover:border-accent-700 hover:bg-accent-700"
                >
                  New organization
                </Button>
              )
            }
          />
          {isLoading ? (
            <Spin indicator={<LoadingOutlined spin />} />
          ) : unavailable ? (
            <div className={NOTE_CLASS}>
              Organizations are managed on the control plane this Site is enrolled in.
            </div>
          ) : orgs.length === 0 ? (
            <div className={NOTE_CLASS}>You aren&apos;t in any organization.</div>
          ) : (
            <div className="flex flex-wrap items-start gap-4">
              <nav
                aria-label="Organizations"
                className="flex max-w-full min-w-0 flex-[1_1_200px] flex-col gap-0.5 rounded-2xl border border-gray-200 bg-white p-2.5 md:max-w-[260px] dark:border-gray-700 dark:bg-gray-800"
              >
                {orgs.map((org) => {
                  const current = selected?.id === org.id;
                  return (
                    <button
                      key={org.id}
                      type="button"
                      onClick={() => select(org.id)}
                      aria-current={current ? 'true' : undefined}
                      className={`flex w-full cursor-pointer items-center gap-2.5 rounded-[10px] border-0 px-2.5 py-2 text-left ${current ? 'bg-[var(--v2-ok-soft)]' : 'bg-transparent hover:bg-gray-50 dark:hover:bg-gray-700/40'}`}
                    >
                      {orgTile(org.name)}
                      <span className="flex min-w-0 flex-col">
                        <span
                          className={`truncate font-medium ${current ? 'text-[var(--v2-accent)]' : 'text-gray-800 dark:text-gray-100'}`}
                        >
                          {org.name}
                        </span>
                        <span className="text-xs text-gray-500 dark:text-gray-400">
                          {org.myRole ? org.myRole.replace('ORG_', '').toLowerCase() : 'admin'}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </nav>
              {selected && active && (
                <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div className="flex min-w-0 items-center gap-3">
                      {orgTile(selected.name, true)}
                      <div className="min-w-0">
                        <h2 className="m-0 truncate text-xl font-semibold text-gray-900 dark:text-gray-50">
                          {selected.name}
                        </h2>
                        <span className="font-mono text-xs text-gray-500 dark:text-gray-400">
                          {selected.slug}
                        </span>
                      </div>
                    </div>
                    <div
                      role="tablist"
                      aria-label="Organization views"
                      className="flex flex-wrap gap-1"
                    >
                      {views.map((v, index) => {
                        const on = v.key === active.key;
                        return (
                          <button
                            key={v.key}
                            ref={(el) => {
                              tabRefs.current[v.key] = el;
                            }}
                            type="button"
                            role="tab"
                            id={`org-tab-${v.key}`}
                            aria-selected={on}
                            aria-controls="org-tabpanel"
                            tabIndex={on ? 0 : -1}
                            onClick={() => setView(v.key)}
                            onKeyDown={(e) => onTabKey(e, index)}
                            className={`cursor-pointer rounded-[10px] border-0 px-3.5 py-2 text-sm ${on ? 'bg-[var(--v2-ok-soft)] font-semibold text-[var(--v2-accent)]' : 'bg-transparent text-gray-500 hover:bg-gray-50 dark:text-gray-400 dark:hover:bg-gray-700/40'}`}
                          >
                            {v.label}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                  <div
                    role="tabpanel"
                    id="org-tabpanel"
                    aria-labelledby={`org-tab-${active.key}`}
                    key={`${selected.id}:${active.key}`}
                  >
                    {active.render(selected)}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
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
          <div className="flex flex-col gap-3 pt-2">
            <label className="flex flex-col gap-1">
              <span className="text-sm text-gray-600 dark:text-gray-300">Name</span>
              <Input
                placeholder="Name"
                value={creating.name}
                onChange={(e) => setCreating({ ...creating, name: e.target.value })}
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-sm text-gray-600 dark:text-gray-300">Slug</span>
              <Input
                className="!font-mono"
                placeholder="slug (lowercase letters, digits, dashes)"
                value={creating.slug}
                onChange={(e) => setCreating({ ...creating, slug: e.target.value })}
              />
            </label>
          </div>
        </Modal>
      )}
    </div>
  );
};
