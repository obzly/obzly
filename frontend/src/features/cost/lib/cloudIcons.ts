// Which provider mark or service icon a cost or posture value gets. Only the app's own icons
// (public/icons/infra) are used; a service without one gets its provider's mark, and a value
// whose cloud can't be told gets nothing rather than a guess.
import type { CostDimension } from '../../../entity/cost';

export type Provider = 'aws' | 'azure';

export interface CloudMatch {
  provider?: Provider;
  /** A path under /icons/infra, when the service has its own icon. */
  icon?: string;
  /** What the icon shows, for alt and title. */
  label?: string;
}

const ICON_LABEL: Record<string, string> = {
  'aws-ec2': 'Amazon EC2',
  'aws-rds': 'Amazon RDS',
  'aws-s3': 'Amazon S3',
  'aws-lambda': 'AWS Lambda',
  'aws-elb': 'Elastic Load Balancing',
  'aws-eks': 'Amazon EKS',
  'aws-autoscaling': 'Amazon EC2 Auto Scaling',
  'azure-vm': 'Azure Virtual Machines',
  'azure-aks': 'Azure Kubernetes Service',
  'azure-sql': 'Azure SQL',
  'azure-storage': 'Azure Storage',
  'azure-functions': 'Azure Functions',
  'azure-appgw': 'Azure Application Gateway',
  'azure-lb': 'Azure Load Balancer',
  'azure-vmss': 'Azure Virtual Machine Scale Sets',
  'azure-postgresql': 'Azure Database for PostgreSQL',
};

export const PROVIDER_NAME: Record<Provider, string> = { aws: 'AWS', azure: 'Microsoft Azure' };

// Checked in order: the more specific service first (scale sets before virtual machines).
const RULES: Record<Provider, [RegExp, string][]> = {
  aws: [
    [/\bauto ?scaling\b/, 'aws-autoscaling'],
    [/\b(eks|kubernetes)\b/, 'aws-eks'],
    [/\b(elb|elbv2|alb|nlb|elasticloadbalancing|load ?balanc\w*)\b/, 'aws-elb'],
    [/\b(rds|relational database|aurora)\b/, 'aws-rds'],
    [/\b(s3|simple storage)\b/, 'aws-s3'],
    [/\blambda\b/, 'aws-lambda'],
    [/\b(ec2|elastic compute)\b/, 'aws-ec2'],
  ],
  azure: [
    [/\b(vmss|scale ?sets?)\b/, 'azure-vmss'],
    [/\b(aks|kubernetes|managed ?clusters?)\b/, 'azure-aks'],
    [/\bpostgre\w*\b/, 'azure-postgresql'],
    [/\bsql\b/, 'azure-sql'],
    [/\bstorage\b/, 'azure-storage'],
    [/\b(functions?|function ?apps?)\b/, 'azure-functions'],
    [/\b(application ?gateways?|app ?gw)\b/, 'azure-appgw'],
    [/\bload ?balancers?\b/, 'azure-lb'],
    [/\b(virtual ?machines?|vms?)\b/, 'azure-vm'],
  ],
};

const GUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const AWS_REGION = /^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-\d+$/;

/** "Ec2 SecurityGroup" or "aws_rds_instance" as lower-case words: "ec2 security group". */
const words = (text: string): string =>
  ` ${text
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean)
    .join(' ')} `;

export const asProvider = (value?: string): Provider | undefined => {
  const lower = value?.trim().toLowerCase();
  return lower === 'aws' ? 'aws' : lower === 'azure' ? 'azure' : undefined;
};

/** The cloud a value belongs to, from its shape (ARN, resource ID, account ID, name). */
export const guessProvider = (value?: string): Provider | undefined => {
  const text = value?.trim() ?? '';
  const lower = text.toLowerCase();
  if (!text) return undefined;
  if (asProvider(lower)) return asProvider(lower);
  if (lower.startsWith('arn:aws')) return 'aws';
  if (lower.startsWith('/subscriptions/') || lower.startsWith('microsoft.')) return 'azure';
  if (/^\d{12}$/.test(text)) return 'aws';
  if (GUID.test(text)) return 'azure';
  if (AWS_REGION.test(lower)) return 'aws';
  if (/^(amazon|aws|ec2|elastic|elb)\b/.test(lower) || /^(aws[_-]|Aws[A-Z])/.test(text))
    return 'aws';
  if (/^(azure|microsoft)\b/.test(lower) || /^(azure[_-]|Azure[A-Z])/.test(text)) return 'azure';
  return undefined;
};

/** "us-east-1" is AWS, "westeurope" Azure; "global" and blanks are neither. */
export const regionProvider = (region?: string): Provider | undefined => {
  const lower = region?.trim().toLowerCase() ?? '';
  if (AWS_REGION.test(lower)) return 'aws';
  if (/^[a-z]{4,}\d?$/.test(lower) && lower !== 'global') return 'azure';
  return undefined;
};

/** The part of an ARN or Azure resource ID that names its type, not the resource's own name. */
const typeText = (key: string): string => {
  if (key.startsWith('arn:')) {
    const [, , service = '', , , resource = ''] = key.split(':');
    return `${service} ${resource.split(/[/:]/)[0]}`;
  }
  const azure = /\/providers\/([^/]+)\/([^/]+)/i.exec(key);
  return azure ? `${azure[1]} ${azure[2]}` : key;
};

const match = (provider: Provider, text: string): CloudMatch => {
  const spaced = words(text);
  const icon = RULES[provider].find(([pattern]) => pattern.test(spaced))?.[1];
  return icon
    ? { provider, icon: `/icons/infra/${icon}.svg`, label: ICON_LABEL[icon] }
    : { provider, label: PROVIDER_NAME[provider] };
};

/**
 * A service, resource type or resource ID's icon. With no provider given it's told from the
 * text; an unknown one only matches an Azure service name (AWS names start with Amazon or AWS).
 */
export const cloudIcon = (text?: string, provider?: string): CloudMatch => {
  if (!text) {
    const known = asProvider(provider);
    return known ? { provider: known, label: PROVIDER_NAME[known] } : {};
  }
  const subject = typeText(text);
  const known = asProvider(provider) ?? guessProvider(text);
  if (known) return match(known, subject);
  const azure = match('azure', subject);
  return azure.icon ? azure : {};
};

/** A cost dimension value's icon: provider marks for accounts and regions, services by name. */
export const dimensionIcon = (dimension: CostDimension, value: string): CloudMatch => {
  if (!value) return {};
  switch (dimension) {
    case 'provider':
    case 'account': {
      const provider = guessProvider(value);
      return provider ? { provider, label: PROVIDER_NAME[provider] } : {};
    }
    case 'region': {
      const provider = regionProvider(value);
      return provider ? { provider, label: PROVIDER_NAME[provider] } : {};
    }
    case 'service':
    case 'resource':
      return cloudIcon(value);
    default:
      return {};
  }
};

/** A posture standard's provider, from its id or name ("cis-aws", "MCSB"). */
export const standardProvider = (standard: string, name = ''): Provider | undefined => {
  const spaced = words(`${standard} ${name}`);
  if (/\baws\b|\bamazon\b/.test(spaced)) return 'aws';
  if (/\bazure\b|\bmcsb\b|\bmicrosoft\b/.test(spaced)) return 'azure';
  return undefined;
};
