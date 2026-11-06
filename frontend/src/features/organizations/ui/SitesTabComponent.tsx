import { App, Button, Select, Tag } from 'antd';
import { useEffect, useState } from 'react';

import { type OrgDetail, organizationsApi } from '../../../entity/organizations';
import { type SiteAdmin, sitesApi } from '../../../entity/sites';
import { errorMessage } from './lib';

interface Props {
  org: OrgDetail;
  isAdmin: boolean;
  onChanged: () => void;
}

export const SitesTabComponent = ({ org, isAdmin, onChanged }: Props) => {
  const { message } = App.useApp();
  const [sites, setSites] = useState<SiteAdmin[]>([]);
  const [siteId, setSiteId] = useState<string>();

  useEffect(() => {
    if (isAdmin) {
      sitesApi
        .listSitesAdmin()
        .then(setSites)
        .catch(() => setSites([]));
    }
  }, [isAdmin]);

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

  return (
    <div>
      <div className="mb-3 text-sm text-gray-500 dark:text-gray-300">
        The organization&apos;s teams can be given roles on these Sites. A Site belongs to one
        organization; moving it removes the old one&apos;s team roles there.
      </div>
      <div className="mb-4 flex flex-wrap gap-1">
        {org.sites.map((s) => (
          <Tag key={s}>{s}</Tag>
        ))}
        {org.sites.length === 0 && <span className="text-sm text-gray-400">No Sites</span>}
      </div>
      {isAdmin && (
        <div className="flex gap-2">
          <Select
            placeholder="Site"
            value={siteId}
            onChange={setSiteId}
            style={{ width: 260 }}
            options={sites
              .filter((s) => !org.sites.includes(s.id))
              .map((s) => ({ value: s.id, label: s.displayName || s.id }))}
          />
          <Button disabled={!siteId} onClick={assign}>
            Assign to {org.name}
          </Button>
        </div>
      )}
    </div>
  );
};
