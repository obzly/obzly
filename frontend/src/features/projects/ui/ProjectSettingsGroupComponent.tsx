import { ApiOutlined, KeyOutlined, SettingOutlined, TeamOutlined } from '@ant-design/icons';
import { type JSX, type ReactNode, useEffect, useState } from 'react';

import type { ProjectResponse } from '../../../entity/projects';
import type { UserProfile } from '../../../entity/users';
import { PageHeader } from '../../../shared/ui/v2';
import { ProjectApiKeysComponent } from './ProjectApiKeysComponent';
import { ProjectMembershipComponent } from './ProjectMembershipComponent';
import { ProjectSettingsComponent } from './ProjectSettingsComponent';
import { DataSourcesComponent } from './dataSources/DataSourcesComponent';

/** Same keys as the sidebar tabs these sections replace. */
export type ProjectSettingsSection = 'settings' | 'members' | 'api-keys' | 'data-sources';

const SECTIONS: { key: ProjectSettingsSection; label: string; icon: ReactNode }[] = [
  { key: 'settings', label: 'Project settings', icon: <SettingOutlined /> },
  { key: 'members', label: 'Members', icon: <TeamOutlined /> },
  { key: 'api-keys', label: 'API keys', icon: <KeyOutlined /> },
  { key: 'data-sources', label: 'Data sources', icon: <ApiOutlined /> },
];

interface Props {
  projectResponse: ProjectResponse;
  user: UserProfile;
  contentHeight: number;
  initialSection?: ProjectSettingsSection;
  onSectionChange?: (section: ProjectSettingsSection) => void;
}

/** One "Settings" page with a section list: project settings, members, API keys, data sources. */
export const ProjectSettingsGroupComponent = ({
  projectResponse,
  user,
  contentHeight,
  initialSection = 'settings',
  onSectionChange,
}: Props): JSX.Element => {
  const [section, setSection] = useState<ProjectSettingsSection>(initialSection);

  const pick = (next: ProjectSettingsSection) => {
    setSection(next);
    onSectionChange?.(next);
  };

  // A caller that deep-links (e.g. "open API keys") changes initialSection.
  useEffect(() => {
    setSection(initialSection);
  }, [initialSection]);

  const shared = { projectResponse, contentHeight, embedded: true } as const;

  return (
    <div className="flex grow">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="flex flex-col gap-5">
            <PageHeader title="Settings" subtitle={projectResponse.name} />

            <div className="flex flex-wrap items-start gap-4">
              <nav
                aria-label="Settings sections"
                className="flex max-w-full flex-[1_1_200px] flex-col gap-0.5 rounded-2xl border border-gray-200 bg-white p-2.5 lg:sticky lg:top-0 lg:max-w-[240px] dark:border-gray-700 dark:bg-gray-800"
              >
                {SECTIONS.map((item) => {
                  const current = item.key === section;
                  return (
                    <button
                      key={item.key}
                      type="button"
                      onClick={() => pick(item.key)}
                      aria-current={current ? 'page' : undefined}
                      className={`flex cursor-pointer items-center gap-2.5 rounded-[10px] border-0 px-3 py-2.5 text-left text-sm ${
                        current
                          ? 'bg-[var(--v2-ok-soft)] font-semibold text-[var(--v2-accent)]'
                          : 'bg-transparent text-gray-600 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-gray-700/40'
                      }`}
                    >
                      {item.icon}
                      {item.label}
                    </button>
                  );
                })}
              </nav>

              <div className="flex min-w-0 flex-[999_1_640px] flex-col gap-4">
                {section === 'settings' && <ProjectSettingsComponent {...shared} user={user} />}
                {section === 'members' && <ProjectMembershipComponent {...shared} user={user} />}
                {section === 'api-keys' && <ProjectApiKeysComponent {...shared} user={user} />}
                {section === 'data-sources' && <DataSourcesComponent {...shared} />}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};
