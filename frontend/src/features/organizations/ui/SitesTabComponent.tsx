import { App, Button, Select, Table, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { type OrgDetail, type Team, organizationsApi } from '../../../entity/organizations';
import { type SiteAdmin, sitesApi } from '../../../entity/sites';
import { type SystemUsage, systemApi } from '../../../entity/system';
import { Card } from '../../../shared/ui/v2';
import { errorMessage, formatBytes, pillClass } from './lib';

interface Props {
  org: OrgDetail;
  isAdmin: boolean;
  onChanged: () => void;
}

interface SiteRow {
  siteId: string;
  access: string[];
}

const roleLabel = (role: string) => role.replace('PROJECT_', '').toLowerCase();

// What each team's grants give on siteId: a Site-wide role, or roles on N projects.
const accessOn = (teams: Team[], siteId: string): string[] =>
  teams.flatMap((team) => {
    const grants = team.grants.filter((g) => g.siteId === siteId);
    if (grants.length === 0) return [];
    const siteWide = grants.find((g) => g.projectId === null);
    const projects = grants.filter((g) => g.projectId !== null).length;
    const parts = [
      ...(siteWide ? [`${roleLabel(siteWide.role)} on every project`] : []),
      ...(projects > 0 ? [`${projects} project${projects === 1 ? '' : 's'}`] : []),
    ];
    return [`${team.name}: ${parts.join(', ')}`];
  });

const NA = <span className="text-gray-400 dark:text-gray-500">n/a</span>;

export const SitesTabComponent = ({ org, isAdmin, onChanged }: Props) => {
  const { message } = App.useApp();
  const [sites, setSites] = useState<SiteAdmin[]>([]);
  const [siteId, setSiteId] = useState<string>();
  // null while loading or when the teams can't be read.
  const [teams, setTeams] = useState<Team[] | null>(null);
  // Per Site: the same /system/usage the Overview's Site cards read (Site
  // admins only, cached a minute on the Site); null when it didn't load.
  const [usage, setUsage] = useState<Record<string, SystemUsage | null>>({});

  useEffect(() => {
    if (isAdmin) {
      sitesApi
        .listSitesAdmin()
        .then(setSites)
        .catch(() => setSites([]));
    }
  }, [isAdmin]);

  useEffect(() => {
    organizationsApi
      .listTeams(org.id)
      .then(setTeams)
      .catch(() => setTeams(null));
  }, [org.id]);

  useEffect(() => {
    if (!isAdmin) return;
    let cancelled = false;
    for (const id of org.sites) {
      systemApi
        .getUsage(`site:${id}`)
        .catch(() => null)
        .then((value) => {
          if (!cancelled) setUsage((prev) => ({ ...prev, [id]: value }));
        });
    }
    return () => {
      cancelled = true;
    };
  }, [isAdmin, org.sites.join(',')]);

  const assign = async () => {
    if (!siteId) return;
    try {
      await organizationsApi.assignSite(org.id, siteId);
      message.success(`${siteId} now belongs to ${org.name}`);
      setSiteId(undefined);
      onChanged();
    } catch (error: unknown) {
      message.error(errorMessage(error, 'Failed to assign the Site'));
    }
  };

  const siteName = (id: string) => {
    const site = sites.find((s) => s.id === id);
    return site?.displayName || site?.name || id;
  };

  const rows: SiteRow[] = org.sites.map((id) => ({
    siteId: id,
    access: teams ? accessOn(teams, id) : [],
  }));

  return (
    <Card
      title="Sites"
      subtitle={
        <>
          The organization&apos;s teams can be given roles on these Sites. A Site belongs to one
          organization; moving it removes the old one&apos;s team roles there.
        </>
      }
      bodyClassName="flex flex-col gap-4"
    >
      <div className="flex flex-wrap gap-1.5">
        {org.sites.map((s) => (
          <span key={s} className={`${pillClass('neutral')} py-1 font-mono`}>
            {s}
          </span>
        ))}
        {org.sites.length === 0 && (
          <span className="text-sm text-gray-500 dark:text-gray-400">No Sites</span>
        )}
      </div>
      {org.sites.length > 0 && (
        <Table<SiteRow>
          rowKey="siteId"
          size="small"
          dataSource={rows}
          pagination={false}
          scroll={{ x: 640 }}
          columns={[
            {
              title: 'Site',
              render: (_, row) => (
                <span className="font-semibold text-gray-900 dark:text-gray-100">
                  {siteName(row.siteId)}
                </span>
              ),
            },
            {
              title: 'Access',
              render: (_, row) =>
                teams === null ? (
                  NA
                ) : row.access.length === 0 ? (
                  <span className="text-gray-500 dark:text-gray-400">No team roles</span>
                ) : (
                  <span className="flex flex-wrap gap-1">
                    {row.access.map((text) => (
                      <span key={text} className={pillClass('neutral')}>
                        {text}
                      </span>
                    ))}
                  </span>
                ),
            },
            {
              title: 'Projects',
              width: 100,
              render: (_, row) => {
                const projects = usage[row.siteId]?.projects;
                return projects === undefined ? NA : projects.toLocaleString();
              },
            },
            {
              title: 'Ingested, 24 h',
              width: 140,
              render: (_, row) => {
                const value = usage[row.siteId];
                if (!value) return NA;
                const bytes = value.hours.reduce((total, hour) => total + hour.rawBytes, 0);
                return (
                  <Tooltip title="Uncompressed logs, metrics and traces received">
                    <span>{formatBytes(bytes)}</span>
                  </Tooltip>
                );
              },
            },
          ]}
        />
      )}
      {!isAdmin && org.sites.length > 0 && (
        <p className="m-0 text-xs text-gray-500 dark:text-gray-400">
          Projects and ingest are shown to Obzly admins.
        </p>
      )}
      {isAdmin && (
        <div className="flex flex-wrap gap-2 border-t border-gray-200 pt-3 dark:border-gray-700">
          <Select
            placeholder="Site"
            aria-label="Site"
            value={siteId}
            onChange={setSiteId}
            style={{ width: 260, maxWidth: '100%' }}
            options={sites
              .filter((s) => !org.sites.includes(s.id))
              .map((s) => ({ value: s.id, label: s.displayName || s.id }))}
          />
          <Button type="primary" disabled={!siteId} onClick={assign}>
            Assign to {org.name}
          </Button>
        </div>
      )}
    </Card>
  );
};
