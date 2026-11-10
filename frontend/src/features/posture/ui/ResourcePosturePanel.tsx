// One resource's open findings in the Infrastructure asset drawer, matched by its cloud ID.
import { LoadingOutlined } from '@ant-design/icons';
import { Spin } from 'antd';
import { useEffect, useState } from 'react';

import { type Finding, postureApi } from '../../../entity/posture';
import { Panel } from '../../synthetics';
import { FindingDrawer } from './FindingDrawer';
import { SeverityTag, SourceTag } from './parts';

export const ResourcePosturePanel = ({ siteId, cloudId }: { siteId: string; cloudId: string }) => {
  const [findings, setFindings] = useState<Finding[]>();
  const [error, setError] = useState<string>();
  const [open, setOpen] = useState<string>();
  const [changed, setChanged] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setFindings(undefined);
    postureApi
      .resource(siteId, cloudId, controller.signal)
      .then(setFindings)
      .catch((reason: Error) => {
        if (!controller.signal.aborted) setError(reason.message);
      });
    return () => controller.abort();
  }, [siteId, cloudId, changed]);

  return (
    <Panel title="Posture" subtitle="Open findings on this resource">
      {error ? (
        <div className="text-xs text-gray-500 dark:text-gray-400">Posture unavailable: {error}</div>
      ) : !findings ? (
        <div className="flex justify-center py-4">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : findings.length === 0 ? (
        <div className="text-xs text-gray-500 dark:text-gray-400">
          No open findings. A resource whose scope isn&apos;t covered shows none either; see Posture
          → Coverage.
        </div>
      ) : (
        <ul className="m-0 flex list-none flex-col gap-2 p-0">
          {findings.map((finding) => (
            <li key={finding.id}>
              <button
                type="button"
                onClick={() => setOpen(finding.id)}
                className="flex w-full cursor-pointer items-start gap-2 rounded p-1 text-left hover:bg-gray-50 dark:hover:bg-gray-700"
              >
                <SeverityTag severity={finding.severity} />
                <span className="min-w-0 flex-1 text-sm text-gray-800 dark:text-gray-200">
                  {finding.title}
                  <span className="block text-xs text-gray-500 dark:text-gray-400">
                    {finding.control}
                    {finding.suppressed && ' · suppressed'}
                  </span>
                </span>
                <SourceTag source={finding.source} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {open && (
        <FindingDrawer
          siteId={siteId}
          findingId={open}
          onClose={() => setOpen(undefined)}
          onChanged={() => setChanged((value) => value + 1)}
        />
      )}
    </Panel>
  );
};
