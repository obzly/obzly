import { Segmented, Table, Tooltip } from 'antd';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';

import { postureApi } from '../../../entity/posture';
import { type CostSite, SiteErrors, type SiteOutcome, fanOut } from '../../cost';
import { regionLabel } from '../lib/format';
import { type SiteCoverageRow, mergeCoverage } from '../lib/merge';
import { CoverageTag } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
}

const STANDARD_SHORT: Record<string, string> = {
  'aws-fsbp': 'FSBP',
  'cis-aws': 'CIS AWS',
  mcsb: 'MCSB',
  'cis-azure': 'CIS Azure',
  'obzly-baseline': 'Baseline',
};

const RESULT_TEXT: Record<string, string> = {
  ok: 'read',
  not_enabled: 'not enabled',
  needs_permission: 'needs permission',
  error: 'failed',
};

export const CoverageTab = ({ sites, refresh }: Props) => {
  const [data, setData] = useState<{ rows: SiteCoverageRow[]; outcomes: SiteOutcome<unknown>[] }>();
  const [show, setShow] = useState<'all' | 'gaps'>('all');

  useEffect(() => {
    const controller = new AbortController();
    void fanOut(sites, (siteId) => postureApi.coverage(siteId, controller.signal)).then(
      (outcomes) => {
        if (!controller.signal.aborted) setData({ rows: mergeCoverage(outcomes), outcomes });
      },
    );
    return () => controller.abort();
  }, [sites, refresh]);

  const rows = (data?.rows ?? []).filter((row) => show === 'all' || row.status !== 'native');

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <Segmented
          size="small"
          value={show}
          onChange={(value) => setShow(value as 'all' | 'gaps')}
          options={[
            { value: 'all', label: 'All scopes' },
            { value: 'gaps', label: 'Not native' },
          ]}
        />
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Each scope reads from one source. Fallback means the native service isn&apos;t on or
          readable there, so Obzly&apos;s baseline checks run instead.
        </span>
      </div>
      {data && <SiteErrors outcomes={data.outcomes} />}
      <Table<SiteCoverageRow>
        size="small"
        loading={!data}
        rowKey={(row) => `${row.siteId}/${row.connectionId}/${row.account}/${row.region}`}
        dataSource={rows}
        pagination={rows.length > 50 ? { pageSize: 50, showSizeChanger: false } : false}
        scroll={{ x: 'max-content' }}
        locale={{
          emptyText: show === 'gaps' ? 'Every scope is covered natively' : 'No scope read yet',
        }}
        columns={[
          { title: 'Connection', dataIndex: 'connectionName' },
          ...(sites.length > 1 ? [{ title: 'Site', dataIndex: 'siteName' }] : []),
          {
            title: 'Account',
            dataIndex: 'account',
            render: (account: string) => <span className="font-mono text-xs">{account}</span>,
          },
          { title: 'Region', dataIndex: 'region', render: regionLabel },
          {
            title: 'Coverage',
            key: 'status',
            render: (_, row) => (
              <span className="flex flex-col items-start gap-0.5">
                <CoverageTag status={row.status} />
                {row.reason && (
                  <span className="text-xs text-gray-500 dark:text-gray-400">{row.reason}</span>
                )}
              </span>
            ),
          },
          {
            title: 'Reads',
            key: 'runs',
            render: (_, row) => (
              <span className="flex flex-wrap gap-1">
                {row.runs.map((run) => (
                  <Tooltip
                    key={`${run.standard}/${run.source}`}
                    title={run.reason || `${run.passed} passed, ${run.failed} failed controls`}
                  >
                    <span
                      className={`rounded border px-1.5 py-px text-[11px] ${run.result === 'ok' ? 'border-gray-200 text-gray-700 dark:border-gray-600 dark:text-gray-300' : 'border-amber-300 text-amber-700 dark:border-amber-700 dark:text-amber-300'}`}
                    >
                      {STANDARD_SHORT[run.standard] ?? run.standard} ·{' '}
                      {run.source === 'native' ? 'native' : 'baseline'}{' '}
                      {RESULT_TEXT[run.result] ?? run.result}
                    </span>
                  </Tooltip>
                ))}
              </span>
            ),
          },
          {
            title: 'Last read',
            key: 'scanned',
            render: (_, row) => (
              <Tooltip title={dayjs(row.scannedAt).format('YYYY-MM-DD HH:mm')}>
                {dayjs(row.scannedAt).fromNow()}
              </Tooltip>
            ),
          },
        ]}
      />
    </div>
  );
};
