import type { AssetHealth, AssetKind, AssetState, SiteAsset } from '../../../entity/inventory';

/** The Synthetics colours: green, amber, red, blue inactive, grey unknown. */
export const HEALTH_COLOR: Record<AssetHealth, string> = {
  healthy: '#10b981',
  warning: '#f59e0b',
  critical: '#ef4444',
  inactive: '#3b82f6',
  unknown: '#9ca3af',
};

export const HEALTH_LABEL: Record<AssetHealth, string> = {
  healthy: 'Healthy',
  warning: 'Warning',
  critical: 'Critical',
  inactive: 'Inactive',
  unknown: 'Unknown',
};

/** Problems first. */
export const HEALTH_ORDER: Record<AssetHealth, number> = {
  critical: 0,
  warning: 1,
  healthy: 2,
  unknown: 3,
  inactive: 4,
};

export const STATE_LABEL: Record<AssetState, string> = {
  new: 'New',
  active: 'Active',
  not_reporting: 'Not reporting',
  terminated: 'Terminated',
};

export const KIND_LABEL: Record<AssetKind, string> = {
  cluster: 'Cluster',
  node: 'Node',
  host: 'Host',
  namespace: 'Namespace',
  workload: 'Workload',
  pod: 'Pod',
  container: 'Container',
  docker_container: 'Docker container',
  service: 'Service',
  load_balancer: 'Load balancer',
  database: 'Database',
  bucket: 'Bucket',
  function: 'Function',
  scaling_group: 'Scaling group',
};

/** The kind filter chips, in hierarchy order. */
export const KIND_FILTERS: { value: AssetKind; label: string }[] = [
  { value: 'cluster', label: 'Clusters' },
  { value: 'node', label: 'Nodes' },
  { value: 'host', label: 'Hosts / VMs' },
  { value: 'namespace', label: 'Namespaces' },
  { value: 'workload', label: 'Workloads' },
  { value: 'pod', label: 'Pods' },
  { value: 'container', label: 'Containers' },
  { value: 'docker_container', label: 'Docker' },
  { value: 'service', label: 'Services' },
  { value: 'load_balancer', label: 'Load balancers' },
  { value: 'database', label: 'Databases' },
  { value: 'bucket', label: 'Buckets' },
  { value: 'function', label: 'Functions' },
  { value: 'scaling_group', label: 'Scaling groups' },
];

const CLOUD_ICON: Record<string, string> = {
  aws_ec2_instance: 'aws-ec2',
  aws_eks_cluster: 'aws-eks',
  aws_load_balancer: 'aws-elb',
  aws_rds_instance: 'aws-rds',
  aws_s3_bucket: 'aws-s3',
  aws_lambda_function: 'aws-lambda',
  aws_autoscaling_group: 'aws-autoscaling',
  azure_virtual_machine: 'azure-vm',
  azure_aks_cluster: 'azure-aks',
  azure_load_balancer: 'azure-lb',
  azure_application_gateway: 'azure-appgw',
  azure_sql_database: 'azure-sql',
  azure_postgresql_server: 'azure-postgresql',
  azure_storage_account: 'azure-storage',
  azure_function_app: 'azure-functions',
  azure_vm_scale_set: 'azure-vmss',
};

const WORKLOAD_ICON: Record<string, string> = {
  Deployment: 'k8s-deployment',
  StatefulSet: 'k8s-statefulset',
  DaemonSet: 'k8s-daemonset',
  Job: 'k8s-job',
  CronJob: 'k8s-cronjob',
  ReplicaSet: 'k8s-replicaset',
};

/** Icons come from the Kubernetes, AWS and Azure sets credited in NOTICE; undefined means generic. */
export const iconFor = (asset: Pick<SiteAsset, 'kind' | 'subtype' | 'attributes'>) => {
  const platform = asset.attributes?.cloud_platform;
  const cloudType = asset.attributes?.cloud_resource_type;
  let name: string | undefined = cloudType ? CLOUD_ICON[cloudType] : undefined;
  switch (name ? '' : asset.kind) {
    case 'cluster':
      name =
        platform === 'aws_eks' ? 'aws-eks' : platform === 'azure_aks' ? 'azure-aks' : 'k8s-cluster';
      break;
    case 'host':
      name = platform === 'aws_ec2' ? 'aws-ec2' : platform === 'azure_vm' ? 'azure-vm' : undefined;
      break;
    case 'workload':
      name = WORKLOAD_ICON[asset.subtype];
      break;
    case 'node':
      name = 'k8s-node';
      break;
    case 'namespace':
      name = 'k8s-namespace';
      break;
    case 'pod':
      name = 'k8s-pod';
      break;
  }
  return name ? `/icons/infra/${name}.svg` : undefined;
};

// Kinds whose subtype says more than the kind (EC2 instance, RDS, Lambda...).
const SUBTYPED: AssetKind[] = [
  'workload',
  'host',
  'load_balancer',
  'database',
  'bucket',
  'function',
  'scaling_group',
];

export const kindLabel = (asset: Pick<SiteAsset, 'kind' | 'subtype'>) =>
  SUBTYPED.includes(asset.kind) && asset.subtype ? asset.subtype : KIND_LABEL[asset.kind];

/** One row per asset key across Sites and projects. */
export const assetUid = (asset: Pick<SiteAsset, 'siteId' | 'projectId' | 'key'>) =>
  `${asset.siteId}|${asset.projectId}|${asset.key}`;

export interface AssetNode extends SiteAsset {
  uid: string;
  children?: AssetNode[];
}

/** An asset whose parent isn't in the list becomes a root. */
export const buildTree = (assets: SiteAsset[]): AssetNode[] => {
  const nodes = new Map<string, AssetNode>();
  for (const asset of assets) nodes.set(assetUid(asset), { ...asset, uid: assetUid(asset) });
  const roots: AssetNode[] = [];
  for (const node of nodes.values()) {
    const parent = node.parentKey
      ? nodes.get(assetUid({ ...node, key: node.parentKey }))
      : undefined;
    if (parent) {
      (parent.children ??= []).push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
};

/** "12 min", "3 h", "40 d" since iso. */
export const since = (iso: string, now = Date.now()) => {
  const minutes = Math.max(0, (now - new Date(iso).getTime()) / 60_000);
  if (minutes < 1) return 'now';
  if (minutes < 60) return `${Math.round(minutes)} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / (24 * 60))} d`;
};

/** The search text that opens this asset's logs. */
export const logsSearchFor = (asset: SiteAsset): string | undefined => {
  const quote = (value: string) => (/^[\w.-]+$/.test(value) ? value : JSON.stringify(value));
  const parts: string[] = [];
  const add = (field: string, value?: string) => value && parts.push(`${field}=${quote(value)}`);
  switch (asset.kind) {
    case 'pod':
      add('k8s_namespace_name', asset.namespace);
      add('k8s_pod_name', asset.name);
      break;
    case 'container':
      add('k8s_pod_name', asset.attributes.k8s_pod_name);
      add('k8s_container_name', asset.name);
      break;
    case 'namespace':
      add('k8s_namespace_name', asset.namespace);
      break;
    case 'workload':
      add('k8s_namespace_name', asset.namespace);
      add(`k8s_${asset.subtype.toLowerCase()}_name`, asset.name);
      break;
    case 'node':
      add('k8s_node_name', asset.attributes.k8s_node_name || asset.node);
      break;
    case 'host':
      add('host_name', asset.node);
      break;
    case 'cluster':
      add('k8s_cluster_name', asset.cluster);
      break;
    case 'service':
      add('service_name', asset.name);
      break;
    case 'docker_container':
      add('container_name', asset.name);
      break;
  }
  return parts.length > 0 ? parts.join(' AND ') : undefined;
};
