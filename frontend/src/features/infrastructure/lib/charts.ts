import type { SiteAsset } from '../../../entity/inventory';

export interface ChartLine {
  query: string;
  legend: (labels: Record<string, string>) => string;
}

export interface ChartQuery {
  key: string;
  title: string;
  subtitle: string;
  lines: ChartLine[];
  format?: (value: number) => string;
}

const percentFormat = (value: number) => `${Math.round(value)}%`;
const coresFormat = (value: number) => (value >= 1 ? value.toFixed(1) : value.toFixed(2));
const bytesFormat = (value: number) =>
  value >= 1 << 30
    ? `${(value / (1 << 30)).toFixed(1)} GiB`
    : `${Math.round(value / (1 << 20))} MiB`;
const rateFormat = (value: number) => `${value.toFixed(2)} MB/s`;

/** A PromQL label value, quoted. */
const q = (value: string) => JSON.stringify(value);

const matchers = (pairs: Record<string, string | undefined>, extra = '') => {
  const parts = Object.entries(pairs)
    .filter(([, value]) => value)
    .map(([name, value]) => `${name}=${q(value!)}`);
  if (extra) parts.push(extra);
  return `{${parts.join(', ')}}`;
};

const named = (name: string) => () => name;
const direction = (labels: Record<string, string>) => labels.direction || 'total';

/** Same PromQL the inventory's values come from, so the charts match them. */
export const chartQueries = (asset: SiteAsset): ChartQuery[] => {
  const cluster = asset.cluster || undefined;
  switch (asset.kind) {
    case 'node': {
      const m = matchers({ k8s_cluster_name: cluster, host_name: asset.node });
      return [
        {
          key: 'cpu',
          title: 'CPU',
          subtitle: '% of the node',
          format: percentFormat,
          lines: [
            {
              query: `100 * sum({__name__=~"k8s_node_cpu_usage|k8s_node_cpu_utilization", ${m.slice(1, -1)}}) / sum(system_cpu_logical_count${matchers({ host_name: asset.node })})`,
              legend: named('CPU'),
            },
          ],
        },
        {
          key: 'memory',
          title: 'Memory',
          subtitle: '% of the node (working set)',
          format: percentFormat,
          lines: [
            {
              query: `100 * sum(k8s_node_memory_working_set${m}) / (sum(k8s_node_memory_working_set${m}) + sum(k8s_node_memory_available${m}))`,
              legend: named('Memory'),
            },
          ],
        },
        network('k8s_node_network_io', m),
      ];
    }
    case 'host': {
      const m = matchers({ host_name: asset.node });
      const idle = matchers({ host_name: asset.node, state: 'idle' });
      const used = matchers({ host_name: asset.node, state: 'used' });
      return [
        {
          key: 'cpu',
          title: 'CPU',
          subtitle: '% busy',
          format: percentFormat,
          lines: [
            {
              query: `100 * (1 - sum(rate(system_cpu_time${idle}[5m])) / sum(rate(system_cpu_time${m}[5m])))`,
              legend: named('CPU'),
            },
          ],
        },
        {
          key: 'memory',
          title: 'Memory',
          subtitle: '% used',
          format: percentFormat,
          lines: [
            {
              query: `100 * sum(system_memory_usage${used}) / sum(system_memory_usage${m})`,
              legend: named('Memory'),
            },
          ],
        },
        network('system_network_io', m),
      ];
    }
    case 'pod': {
      const m = matchers({
        k8s_cluster_name: cluster,
        k8s_namespace_name: asset.namespace,
        k8s_pod_name: asset.name,
      });
      return usage(
        m,
        'k8s_pod_cpu_usage|k8s_pod_cpu_utilization',
        'k8s_pod_memory_working_set',
        asset,
      ).concat(network('k8s_pod_network_io', m));
    }
    case 'container': {
      const m = matchers({
        k8s_cluster_name: cluster,
        k8s_namespace_name: asset.namespace,
        k8s_pod_name: asset.attributes.k8s_pod_name,
        k8s_container_name: asset.name,
      });
      return usage(
        m,
        'container_cpu_usage|container_cpu_utilization',
        'container_memory_working_set',
        asset,
      );
    }
    case 'workload':
    case 'namespace': {
      const m =
        asset.kind === 'workload'
          ? matchers(
              { k8s_cluster_name: cluster, k8s_namespace_name: asset.namespace },
              `k8s_pod_name=~${q(`${escapeRegex(asset.name)}-.+`)}`,
            )
          : matchers({ k8s_cluster_name: cluster, k8s_namespace_name: asset.namespace });
      return [
        {
          key: 'cpu',
          title: 'CPU',
          subtitle: 'Cores, all its pods',
          format: coresFormat,
          lines: [
            {
              query: `sum({__name__=~"k8s_pod_cpu_usage|k8s_pod_cpu_utilization", ${m.slice(1, -1)}})`,
              legend: named('CPU'),
            },
          ],
        },
        {
          key: 'memory',
          title: 'Memory',
          subtitle: 'Working set, all its pods',
          format: bytesFormat,
          lines: [{ query: `sum(k8s_pod_memory_working_set${m})`, legend: named('Memory') }],
        },
        network('k8s_pod_network_io', m),
      ];
    }
    case 'cluster': {
      const m = matchers({ k8s_cluster_name: cluster });
      return [
        {
          key: 'cpu',
          title: 'CPU',
          subtitle: '% of all nodes',
          format: percentFormat,
          lines: [
            {
              query: `100 * sum({__name__=~"k8s_node_cpu_usage|k8s_node_cpu_utilization"${cluster ? `, k8s_cluster_name=${q(cluster)}` : ''}}) / sum(system_cpu_logical_count${m})`,
              legend: named('CPU'),
            },
          ],
        },
        {
          key: 'memory',
          title: 'Memory',
          subtitle: '% of all nodes',
          format: percentFormat,
          lines: [
            {
              query: `100 * sum(k8s_node_memory_working_set${m}) / (sum(k8s_node_memory_working_set${m}) + sum(k8s_node_memory_available${m}))`,
              legend: named('Memory'),
            },
          ],
        },
        network('k8s_node_network_io', m),
      ];
    }
    case 'docker_container': {
      const m = matchers({ host_name: asset.node || undefined, container_name: asset.name });
      return [
        {
          key: 'cpu',
          title: 'CPU',
          subtitle: '% of the host',
          format: percentFormat,
          lines: [{ query: `avg(container_cpu_utilization${m})`, legend: named('CPU') }],
        },
        {
          key: 'memory',
          title: 'Memory',
          subtitle: 'Used',
          format: bytesFormat,
          lines: [{ query: `sum(container_memory_usage_total${m})`, legend: named('Memory') }],
        },
      ];
    }
    default:
      return [];
  }
};

const usage = (m: string, cpuNames: string, memoryName: string, asset: SiteAsset): ChartQuery[] => [
  {
    key: 'cpu',
    title: 'CPU',
    subtitle: 'Cores',
    format: coresFormat,
    lines: [
      { query: `sum({__name__=~${q(cpuNames)}, ${m.slice(1, -1)}})`, legend: named(asset.name) },
    ],
  },
  {
    key: 'memory',
    title: 'Memory',
    subtitle: 'Working set',
    format: bytesFormat,
    lines: [{ query: `sum(${memoryName}${m})`, legend: named(asset.name) }],
  },
];

const network = (metric: string, m: string): ChartQuery => ({
  key: 'network',
  title: 'Network',
  subtitle: 'Receive and transmit, MB/s',
  format: rateFormat,
  lines: [{ query: `sum by (direction) (rate(${metric}${m}[5m])) / 1e6`, legend: direction }],
});

const escapeRegex = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
