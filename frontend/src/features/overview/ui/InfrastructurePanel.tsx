import {
  ApiOutlined,
  ClusterOutlined,
  ContainerOutlined,
  DatabaseOutlined,
  DesktopOutlined,
  ExpandOutlined,
  FunctionOutlined,
  InboxOutlined,
} from '@ant-design/icons';
import { Tooltip } from 'antd';
import type { ReactNode } from 'react';

import type { AssetKind, InventorySummary } from '../../../entity/inventory';
import { useTheme } from '../../../shared/theme';
import { Card } from '../../../shared/ui/v2';
import { infraRows } from '../lib/overview';
import type { Loaded } from '../lib/useLoad';
import { Muted, PanelBody, PanelLink } from './parts';

const KIND_TITLE: Record<AssetKind, string> = {
  cluster: 'Clusters',
  node: 'Nodes',
  host: 'Hosts / VMs',
  namespace: 'Namespaces',
  workload: 'Workloads',
  pod: 'Pods',
  container: 'Containers',
  docker_container: 'Docker containers',
  service: 'Services',
  load_balancer: 'Load balancers',
  database: 'Databases',
  bucket: 'Buckets',
  function: 'Functions',
  scaling_group: 'Scaling groups',
};

// The Kubernetes icons the Infrastructure page uses (credited in NOTICE); kinds that may be AWS,
// Azure or neither get a neutral icon.
const KIND_ICON: Partial<Record<AssetKind, string>> = {
  cluster: 'k8s-cluster',
  node: 'k8s-node',
  namespace: 'k8s-namespace',
  workload: 'k8s-deployment',
  pod: 'k8s-pod',
};

const GENERIC_ICON: Partial<Record<AssetKind, ReactNode>> = {
  host: <DesktopOutlined />,
  container: <ContainerOutlined />,
  docker_container: <ContainerOutlined />,
  service: <ApiOutlined />,
  load_balancer: <ClusterOutlined />,
  database: <DatabaseOutlined />,
  bucket: <InboxOutlined />,
  function: <FunctionOutlined />,
  scaling_group: <ExpandOutlined />,
};

export const InfrastructurePanel = ({
  state,
  multiSite,
  onOpen,
}: {
  state: Loaded<InventorySummary>;
  multiSite: boolean;
  onOpen: () => void;
}) => {
  const { status } = useTheme();
  const legend = [
    { label: 'Healthy', color: status.ok },
    { label: 'Warning', color: status.warn },
    { label: 'Critical', color: status.bad },
    { label: 'Inactive / unknown', color: status.noData },
  ];
  return (
    <Card
      title="Infrastructure"
      subtitle={multiSite ? 'Assets on this Site, by kind and health' : 'Assets by kind and health'}
      extra={<PanelLink onClick={onOpen}>Inventory</PanelLink>}
    >
      <PanelBody state={state} minHeight={240}>
        {(summary) => {
          const rows = infraRows(summary);
          if (rows.length === 0) return <Muted>No assets discovered yet</Muted>;
          return (
            <div className="flex flex-col gap-3.5">
              {rows.map((row) => {
                const attention = row.warning + row.critical;
                const icon = KIND_ICON[row.kind];
                const parts = [
                  { key: 'healthy', value: row.healthy, color: status.ok },
                  { key: 'warning', value: row.warning, color: status.warn },
                  { key: 'critical', value: row.critical, color: status.bad },
                  { key: 'other', value: row.other, color: status.noData },
                ].filter((part) => part.value > 0);
                return (
                  <button
                    key={row.kind}
                    type="button"
                    onClick={onOpen}
                    className="flex cursor-pointer flex-col gap-1.5 border-0 bg-transparent p-0 text-left"
                  >
                    <span className="flex items-center justify-between gap-3 text-sm">
                      <span className="flex min-w-0 items-center gap-2 text-gray-800 dark:text-gray-100">
                        {icon ? (
                          <img src={`/icons/infra/${icon}.svg`} width={20} height={20} alt="" />
                        ) : (
                          <span
                            aria-hidden
                            className="flex h-5 w-5 items-center justify-center text-gray-500 dark:text-gray-400"
                          >
                            {GENERIC_ICON[row.kind]}
                          </span>
                        )}
                        <span className="truncate">{KIND_TITLE[row.kind]}</span>
                      </span>
                      <span className="text-xs whitespace-nowrap text-gray-500 dark:text-gray-400">
                        <strong className="text-sm font-semibold text-gray-900 dark:text-gray-50">
                          {row.total.toLocaleString()}
                        </strong>
                        {' · '}
                        {attention
                          ? `${attention.toLocaleString()} need a look`
                          : 'none need a look'}
                      </span>
                    </span>
                    <Tooltip
                      title={`${row.healthy} healthy, ${row.warning} warning, ${row.critical} critical, ${row.other} inactive or unknown`}
                    >
                      <span
                        className="flex h-2 gap-0.5 overflow-hidden rounded-full"
                        role="img"
                        aria-label={`${KIND_TITLE[row.kind]}: ${row.healthy} healthy, ${row.warning} warning, ${row.critical} critical`}
                      >
                        {parts.map((part) => (
                          <span
                            key={part.key}
                            style={{ flexGrow: part.value, background: part.color }}
                          />
                        ))}
                      </span>
                    </Tooltip>
                  </button>
                );
              })}
              <span className="flex flex-wrap gap-3 pt-1 text-xs text-gray-500 dark:text-gray-400">
                {legend.map((item) => (
                  <span key={item.label} className="flex items-center gap-1.5">
                    <span className="h-2 w-2 rounded-full" style={{ background: item.color }} />
                    {item.label}
                  </span>
                ))}
              </span>
            </div>
          );
        }}
      </PanelBody>
    </Card>
  );
};
