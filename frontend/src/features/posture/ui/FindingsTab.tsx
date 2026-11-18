import { Input, Segmented, Select, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';

import {
  type FindingStatusFilter,
  type PostureFacets,
  type PostureFilters,
  postureApi,
} from '../../../entity/posture';
import { Card } from '../../../shared/ui/v2';
import {
  type CostSite,
  IconLabel,
  ProviderMark,
  RegionMark,
  ServiceIcon,
  SiteErrors,
  type SiteOutcome,
  StandardMark,
  fanOut,
  guessProvider,
} from '../../cost';
import { regionLabel, shortResource } from '../lib/format';
import { type SiteFinding, mergeFacets, mergeFindings } from '../lib/merge';
import { FindingDrawer } from './FindingDrawer';
import { SeverityTag, SourceTag } from './parts';

interface Props {
  sites: CostSite[];
  refresh: number;
  filters: PostureFilters;
  onChange: (filters: PostureFilters) => void;
}

const PAGE = 50;

// The filters a user picks from, with how they read.
const DIMENSIONS: { key: keyof PostureFilters; label: string }[] = [
  { key: 'severity', label: 'Severity' },
  { key: 'standard', label: 'Standard' },
  { key: 'provider', label: 'Provider' },
  { key: 'account', label: 'Account' },
  { key: 'region', label: 'Region' },
  { key: 'source', label: 'Source' },
  { key: 'connection', label: 'Connection' },
];

const VALUE_LABEL: Record<string, string> = {
  aws: 'AWS',
  azure: 'Azure',
  native: 'Native',
  baseline: 'Obzly baseline',
  global: 'Account-wide',
};

/** Provider marks beside the filter values that name a cloud, account, region or standard. */
const facetIcon = (key: keyof PostureFilters, value: string, label: string) => {
  switch (key) {
    case 'provider':
      return <ProviderMark provider={value} size={14} />;
    case 'account':
      return <ProviderMark provider={guessProvider(value)} size={14} />;
    case 'region':
      return <RegionMark region={value} size={14} />;
    case 'standard':
      return <StandardMark standard={value} name={label} size={14} />;
    default:
      return null;
  }
};

export const FindingsTab = ({ sites, refresh, filters, onChange }: Props) => {
  const [page, setPage] = useState(1);
  const [result, setResult] = useState<{
    total: number;
    findings: SiteFinding[];
    outcomes: SiteOutcome<unknown>[];
  }>();
  const [facets, setFacets] = useState<PostureFacets>({ dimensions: {} });
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState(filters.search ?? '');
  const [open, setOpen] = useState<SiteFinding>();
  const [changed, setChanged] = useState(0);

  useEffect(() => setPage(1), [filters]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    // Each Site's first page*PAGE findings, so the merged page is exact.
    const request = { filters, limit: page * PAGE };
    void Promise.all([
      fanOut(sites, (siteId) => postureApi.findings(siteId, request, controller.signal)),
      fanOut(sites, (siteId) => postureApi.facets(siteId, filters, controller.signal)),
    ]).then(([findingOutcomes, facetOutcomes]) => {
      if (controller.signal.aborted) return;
      const merged = mergeFindings(findingOutcomes);
      setResult({
        total: merged.total,
        findings: merged.findings.slice((page - 1) * PAGE, page * PAGE),
        outcomes: findingOutcomes,
      });
      setFacets(mergeFacets(facetOutcomes));
      setLoading(false);
    });
    return () => controller.abort();
  }, [sites, filters, page, refresh, changed]);

  const set = (patch: Partial<PostureFilters>) => onChange({ ...filters, ...patch });

  const columns: ColumnsType<SiteFinding> = useMemo(
    () => [
      {
        title: 'Severity',
        key: 'severity',
        width: 110,
        render: (_, finding) => <SeverityTag severity={finding.severity} />,
      },
      {
        title: 'Finding',
        key: 'title',
        render: (_, finding) => (
          <div className="min-w-0">
            <div className="text-gray-900 dark:text-gray-100">{finding.title}</div>
            <div className="text-xs text-gray-500 dark:text-gray-400">
              {finding.control}
              {finding.suppressed && ' · suppressed'}
              {finding.status === 'resolved' && ' · resolved'}
            </div>
          </div>
        ),
      },
      {
        title: 'Resource',
        key: 'resource',
        ellipsis: true,
        render: (_, finding) => (
          <Tooltip title={finding.resourceKey}>
            <span className="inline-flex items-center gap-1.5">
              <ServiceIcon
                text={finding.resourceType || finding.resourceKey}
                provider={finding.provider}
              />
              <span className="font-mono text-xs">{shortResource(finding.resourceKey)}</span>
            </span>
          </Tooltip>
        ),
      },
      {
        title: 'Where',
        key: 'where',
        width: 200,
        render: (_, finding) => (
          <span className="text-xs">
            <IconLabel icon={<ProviderMark provider={finding.provider} size={14} />}>
              {finding.account}
            </IconLabel>
            <span className="block text-gray-500 dark:text-gray-400">
              {regionLabel(finding.region)}
              {sites.length > 1 && ` · ${finding.siteName}`}
            </span>
          </span>
        ),
      },
      {
        title: 'Source',
        key: 'source',
        width: 130,
        render: (_, finding) => <SourceTag source={finding.source} />,
      },
      {
        title: 'First seen',
        key: 'firstSeen',
        width: 120,
        render: (_, finding) => (
          <Tooltip title={dayjs(finding.firstSeen).format('YYYY-MM-DD HH:mm')}>
            {dayjs(finding.firstSeen).fromNow()}
          </Tooltip>
        ),
      },
    ],
    [sites.length],
  );

  return (
    <div className="flex flex-col gap-4">
      <Card bodyClassName="flex flex-col gap-3">
        <div
          role="group"
          aria-label="Findings filters"
          className="flex flex-wrap items-center gap-2"
        >
          <Segmented
            size="small"
            aria-label="Status"
            value={filters.status ?? 'open'}
            onChange={(value) => set({ status: value as FindingStatusFilter })}
            options={[
              { value: 'open', label: 'Open' },
              { value: 'suppressed', label: 'Suppressed' },
              { value: 'resolved', label: 'Resolved' },
              { value: 'all', label: 'All' },
            ]}
          />
          <Input.Search
            size="small"
            allowClear
            style={{ width: 280 }}
            placeholder="Title, control or resource"
            aria-label="Search findings"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            onSearch={(value) => set({ search: value.trim() || undefined })}
          />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {DIMENSIONS.map((dimension) => {
            const values = facets.dimensions[dimension.key] ?? [];
            const selected = (filters[dimension.key] as string[] | undefined) ?? [];
            return (
              <Select
                key={dimension.key}
                size="small"
                mode="multiple"
                allowClear
                maxTagCount={1}
                className="min-w-36"
                placeholder={dimension.label}
                value={selected}
                onChange={(next: string[]) =>
                  set({ [dimension.key]: next.length ? next : undefined })
                }
                options={[
                  ...values.map((value) => ({
                    value: value.value,
                    label: `${value.label || VALUE_LABEL[value.value] || value.value} (${value.count})`,
                  })),
                  ...selected
                    .filter((value) => !values.some((item) => item.value === value))
                    .map((value) => ({ value, label: VALUE_LABEL[value] || value })),
                ]}
                notFoundContent="No values with the other filters"
                aria-label={dimension.label}
                optionRender={(option) => (
                  <IconLabel
                    icon={facetIcon(dimension.key, String(option.value), String(option.label))}
                  >
                    {option.label}
                  </IconLabel>
                )}
              />
            );
          })}
        </div>
      </Card>

      {result && <SiteErrors outcomes={result.outcomes} />}
      <Card
        title="Findings"
        subtitle={result ? `${result.total} findings, most severe first` : undefined}
      >
        <Table<SiteFinding>
          size="small"
          loading={loading}
          rowKey={(finding) => `${finding.siteId}/${finding.id}`}
          dataSource={result?.findings ?? []}
          columns={columns}
          scroll={{ x: 'max-content' }}
          locale={{ emptyText: 'No findings match' }}
          pagination={{
            current: page,
            pageSize: PAGE,
            total: result?.total ?? 0,
            showSizeChanger: false,
            onChange: setPage,
            showTotal: (total) => `${total} findings`,
          }}
          onRow={(finding) => ({ onClick: () => setOpen(finding), className: 'cursor-pointer' })}
        />
      </Card>
      {open && (
        <FindingDrawer
          siteId={open.siteId}
          findingId={open.id}
          onClose={() => setOpen(undefined)}
          onChanged={() => setChanged((value) => value + 1)}
        />
      )}
    </div>
  );
};
