// What the cloud connection wizard asks the user to run, as a template or a
// CLI script. Every permission is read-only.
import type { CloudAccess, CloudProvider, CloudScope, CloudUse } from '../../../entity/cloud';
import azureCostExportTemplate from '../templates/obzly-cost-export.bicep?raw';
import azureIdentityTemplate from '../templates/obzly-identity.bicep?raw';
import azureReaderTemplate from '../templates/obzly-reader.bicep?raw';
import awsRoleTemplate from '../templates/obzly-readonly-role.yaml?raw';

export type TemplateFormat = 'template' | 'cli';

export interface TemplateInput {
  provider: CloudProvider;
  access: CloudAccess;
  scope: CloudScope;
  scopeId: string;
  uses: CloudUse[];
  /** The control plane URL, the issuer of federated tokens. */
  issuer: string;
  /** The probe group's ID, which federated tokens name. */
  probeGroup: string;
  externalId: string;
  /** AWS role access: the IAM role the probe group runs with. */
  probeRoleArn: string;
  /** AWS probe access: that role's name. */
  probeRoleName: string;
  /** Azure probe access: the managed identity's principal (object) ID. */
  principalId: string;
  /** Azure workload / federated: where the new identity goes. */
  resourceGroup: string;
  /** Azure workload: the cluster's OIDC issuer and the probe's service account. */
  clusterIssuer: string;
  serviceAccount: string;
  /** Where the FOCUS cost export lands, when cost reads one: read access to it only. */
  costExport?: {
    bucket?: string;
    prefix?: string;
    storageAccount?: string;
    /** Azure: the storage account's resource group (the template's scope, not stored). */
    resourceGroup?: string;
  };
}

export interface GeneratedTemplate {
  /** What to run, with this connection's values. */
  script: string;
  /** Files the script deploys, offered as downloads. */
  files: { name: string; content: string }[];
}

export const AWS_PERMISSIONS: Record<CloudUse, { managed: string[]; actions: string[] }> = {
  inventory: {
    managed: ['ViewOnlyAccess'],
    actions: [
      'elasticloadbalancing:DescribeTargetHealth',
      'tag:GetResources',
      'eks:DescribeCluster',
      'ec2:DescribeInstanceStatus',
    ],
  },
  // Kept equal to the cost statement of obzly-readonly-role.yaml: billing, recommendations
  // (Cost Explorer, Compute Optimizer, Obzly's idle checks) and the export's definition.
  cost: {
    managed: [],
    actions: [
      'ce:GetCostAndUsage',
      'ce:GetCostForecast',
      'ce:GetDimensionValues',
      'ce:GetTags',
      'ce:GetSavingsPlansPurchaseRecommendation',
      'ce:GetReservationPurchaseRecommendation',
      'ce:GetSavingsPlansCoverage',
      'ce:GetSavingsPlansUtilizationDetails',
      'ce:GetReservationCoverage',
      'ce:GetReservationUtilization',
      'compute-optimizer:GetEnrollmentStatus',
      'compute-optimizer:GetEC2InstanceRecommendations',
      'compute-optimizer:GetEBSVolumeRecommendations',
      'compute-optimizer:GetIdleRecommendations',
      'ec2:DescribeRegions',
      'ec2:DescribeInstances',
      'ec2:DescribeVolumes',
      'ec2:DescribeAddresses',
      'bcm-data-exports:GetExport',
      'bcm-data-exports:ListExports',
    ],
  },
  // SecurityAudit covers the baseline checks' reads; Security Hub's findings need these three.
  posture: {
    managed: ['SecurityAudit'],
    actions: [
      'securityhub:DescribeHub',
      'securityhub:GetEnabledStandards',
      'securityhub:GetFindings',
    ],
  },
};

// The calls inventory discovery makes, for a policy on the probe's own role
// (CloudFormation can't attach AWS managed policies to an existing role).
export const AWS_INVENTORY_ACTIONS = [
  'ec2:DescribeRegions',
  'ec2:DescribeInstances',
  'ec2:DescribeInstanceStatus',
  'elasticloadbalancing:DescribeLoadBalancers',
  'elasticloadbalancing:DescribeTargetGroups',
  'elasticloadbalancing:DescribeTargetHealth',
  'eks:ListClusters',
  'eks:DescribeCluster',
  'rds:DescribeDBInstances',
  'autoscaling:DescribeAutoScalingGroups',
  'lambda:ListFunctions',
  's3:ListAllMyBuckets',
  'tag:GetResources',
];

// The calls posture makes (Security Hub, then the baseline checks), for a policy on the probe's
// own role, which can't take the SecurityAudit managed policy through CloudFormation.
export const AWS_POSTURE_ACTIONS = [
  'securityhub:DescribeHub',
  'securityhub:GetEnabledStandards',
  'securityhub:GetFindings',
  'ec2:DescribeRegions',
  'ec2:DescribeSecurityGroups',
  'ec2:DescribeInstances',
  'ec2:GetEbsEncryptionByDefault',
  'rds:DescribeDBInstances',
  'iam:GetAccountSummary',
  'iam:GetAccountPasswordPolicy',
  'cloudtrail:DescribeTrails',
  'cloudtrail:GetTrailStatus',
  's3:ListAllMyBuckets',
  's3:GetBucketPublicAccessBlock',
  's3:GetBucketLogging',
];

export const AZURE_ROLES: Record<CloudUse, { name: string; id: string }> = {
  inventory: { name: 'Reader', id: 'acdd72a7-3385-48ef-bd42-f606fba81ae7' },
  cost: { name: 'Cost Management Reader', id: '72fafb9e-0641-4937-9268-a91bfd8191a3' },
  posture: { name: 'Security Reader', id: '39bc4728-0917-49c7-9d2c-d95423bc2eb4' },
};

const AWS_SESSION_AUDIENCE = 'sts.amazonaws.com';
const AZURE_AUDIENCE = 'api://AzureADTokenExchange';

/** probe:<group ID>, the subject the control plane's tokens carry. */
export const probeSubject = (probeGroup: string) => `probe:${probeGroup}`;

export const issuerHost = (issuer: string) => {
  try {
    return new URL(issuer).host;
  } catch {
    return issuer.replace(/^https?:\/\//, '').replace(/\/.*$/, '');
  }
};

const has = (input: TemplateInput, use: CloudUse) => input.uses.includes(use);

/** The export bucket and prefix when cost reads an AWS export. */
const awsExport = (input: TemplateInput) => {
  const bucket = input.costExport?.bucket?.trim();
  return has(input, 'cost') && bucket
    ? { bucket, prefix: input.costExport?.prefix?.trim() ?? '' }
    : undefined;
};

/** Read access to the export's objects only: GetObject under the prefix, ListBucket of it. */
export const awsExportStatements = (bucket: string, prefix: string) => [
  {
    Sid: 'costexport',
    Effect: 'Allow',
    Action: 's3:GetObject',
    Resource: `arn:aws:s3:::${bucket}/${prefix}*`,
  },
  {
    Sid: 'costexportlist',
    Effect: 'Allow',
    Action: 's3:ListBucket',
    Resource: `arn:aws:s3:::${bucket}`,
    Condition: { StringLike: { 's3:prefix': `${prefix}*` } },
  },
];

const azureExport = (input: TemplateInput) => {
  const account = input.costExport?.storageAccount?.trim();
  return has(input, 'cost') && account
    ? { account, resourceGroup: or(input.costExport?.resourceGroup ?? '', '<its resource group>') }
    : undefined;
};

const STORAGE_BLOB_DATA_READER = 'Storage Blob Data Reader';
const or = (value: string, placeholder: string) => value.trim() || placeholder;
const shellQuote = (value: string) => `'${value.replace(/'/g, `'\\''`)}'`;

export const generateTemplate = (
  input: TemplateInput,
  format: TemplateFormat,
): GeneratedTemplate => {
  if (input.provider === 'aws') {
    if (input.access === 'probe') return awsProbePolicy(input, format);
    return format === 'cli' ? awsCLI(input) : awsCloudFormation(input);
  }
  if (format === 'cli' || input.scope === 'management_group') return azureCLI(input);
  return azureBicep(input);
};

const awsTrustParameters = (input: TemplateInput) =>
  input.access === 'role'
    ? [
        'TrustMode=AssumeRole',
        `TrustedPrincipalArn=${or(input.probeRoleArn, '<the probe group IAM role ARN>')}`,
        `ExternalId=${input.externalId}`,
      ]
    : [
        'TrustMode=Federated',
        `IssuerHost=${issuerHost(input.issuer)}`,
        `ProbeSubject=${probeSubject(input.probeGroup)}`,
      ];

const awsUseParameters = (input: TemplateInput) => {
  const parameters = [
    `Inventory=${has(input, 'inventory')}`,
    `Cost=${has(input, 'cost')}`,
    `Posture=${has(input, 'posture')}`,
  ];
  const exported = awsExport(input);
  if (exported) {
    parameters.push(`ExportBucket=${exported.bucket}`);
    if (exported.prefix) parameters.push(`ExportPrefix=${exported.prefix}`);
  }
  return parameters;
};

const awsCloudFormation = (input: TemplateInput): GeneratedTemplate => {
  const parameters = [...awsTrustParameters(input), ...awsUseParameters(input)];
  const lines = [
    '#!/usr/bin/env bash',
    `# Obzly read-only access: ${input.uses.join(', ')}`,
    'set -euo pipefail',
    '',
  ];
  if (input.scope === 'organization') {
    lines.push(
      '# In the management account. Organizations StackSets skip the management account,',
      '# so deploy the stack there too: Obzly lists the accounts with it.',
      'aws cloudformation deploy --stack-name obzly-readonly \\',
      '  --template-file obzly-readonly-role.yaml --capabilities CAPABILITY_NAMED_IAM \\',
      `  --parameter-overrides ${parameters.join(' ')}`,
      '',
      'aws cloudformation create-stack-set --stack-set-name obzly-readonly \\',
      '  --template-body file://obzly-readonly-role.yaml --capabilities CAPABILITY_NAMED_IAM \\',
      '  --permission-model SERVICE_MANAGED --auto-deployment Enabled=true,RetainStacksOnAccountRemoval=false \\',
      `  --parameters ${parameters
        .concat(input.access === 'federated' ? ['CreateOidcProvider=true'] : [])
        .map((parameter) => {
          const [key, ...value] = parameter.split('=');
          return `ParameterKey=${key},ParameterValue=${value.join('=')}`;
        })
        .join(' ')}`,
      '',
      'ROOT=$(aws organizations list-roots --query "Roots[0].Id" --output text)',
      'aws cloudformation create-stack-instances --stack-set-name obzly-readonly \\',
      '  --deployment-targets OrganizationalUnitIds=$ROOT --regions us-east-1',
    );
  } else {
    lines.push(
      `# In account ${or(input.scopeId, '<account ID>')}.`,
      'aws cloudformation deploy --stack-name obzly-readonly \\',
      '  --template-file obzly-readonly-role.yaml --capabilities CAPABILITY_NAMED_IAM \\',
      `  --parameter-overrides ${parameters.join(' ')}`,
    );
  }
  lines.push(
    '',
    '# The role ARN for the next step:',
    'aws cloudformation describe-stacks --stack-name obzly-readonly \\',
    '  --query "Stacks[0].Outputs[?OutputKey==\'RoleArn\'].OutputValue" --output text',
  );
  return {
    script: `${lines.join('\n')}\n`,
    files: [{ name: 'obzly-readonly-role.yaml', content: awsRoleTemplate }],
  };
};

const awsCLI = (input: TemplateInput): GeneratedTemplate => {
  const host = issuerHost(input.issuer);
  const trust =
    input.access === 'role'
      ? {
          Version: '2012-10-17',
          Statement: [
            {
              Effect: 'Allow',
              Principal: { AWS: or(input.probeRoleArn, '<the probe group IAM role ARN>') },
              Action: 'sts:AssumeRole',
              Condition: { StringEquals: { 'sts:ExternalId': input.externalId } },
            },
          ],
        }
      : {
          Version: '2012-10-17',
          Statement: [
            {
              Effect: 'Allow',
              Principal: { Federated: `arn:aws:iam::$ACCOUNT:oidc-provider/${host}` },
              Action: 'sts:AssumeRoleWithWebIdentity',
              Condition: {
                StringEquals: {
                  [`${host}:sub`]: probeSubject(input.probeGroup),
                  [`${host}:aud`]: AWS_SESSION_AUDIENCE,
                },
              },
            },
          ],
        };
  const lines = [
    '#!/usr/bin/env bash',
    `# Obzly read-only access: ${input.uses.join(', ')}`,
    input.scope === 'organization'
      ? '# Run in every account of the Organization (or use the CloudFormation StackSet).'
      : `# Run in account ${or(input.scopeId, '<account ID>')}.`,
    'set -euo pipefail',
    'ROLE=obzly-readonly',
    'ACCOUNT=$(aws sts get-caller-identity --query Account --output text)',
    '',
  ];
  if (input.access === 'federated') {
    lines.push(
      `aws iam create-open-id-connect-provider --url https://${host} --client-id-list ${AWS_SESSION_AUDIENCE}`,
    );
  }
  // Double quotes so $ACCOUNT expands in the federated principal.
  lines.push(
    `aws iam create-role --role-name "$ROLE" --max-session-duration 3600 \\`,
    `  --assume-role-policy-document "${JSON.stringify(trust).replace(/"/g, '\\"')}"`,
  );
  for (const use of input.uses) {
    for (const policy of AWS_PERMISSIONS[use].managed) {
      const path = policy === 'ViewOnlyAccess' ? 'job-function/' : '';
      lines.push(
        `aws iam attach-role-policy --role-name "$ROLE" --policy-arn arn:aws:iam::aws:policy/${path}${policy}`,
      );
    }
  }
  const exported = awsExport(input);
  const statements: object[] = [
    ...input.uses
      .filter((use) => AWS_PERMISSIONS[use].actions.length > 0)
      .map((use) => ({
        Sid: use,
        Effect: 'Allow',
        Action: AWS_PERMISSIONS[use].actions,
        Resource: '*',
      })),
    ...(exported ? awsExportStatements(exported.bucket, exported.prefix) : []),
  ];
  if (statements.length > 0) {
    lines.push(
      'aws iam put-role-policy --role-name "$ROLE" --policy-name obzly-readonly-extra \\',
      `  --policy-document ${shellQuote(JSON.stringify({ Version: '2012-10-17', Statement: statements }))}`,
    );
  }
  lines.push(
    '',
    '# The role ARN for the next step:',
    'aws iam get-role --role-name "$ROLE" --query Role.Arn --output text',
  );
  return { script: `${lines.join('\n')}\n`, files: [] };
};

const awsProbePolicy = (input: TemplateInput, format: TemplateFormat): GeneratedTemplate => {
  const actions = [
    ...new Set([
      ...(has(input, 'inventory') ? AWS_INVENTORY_ACTIONS : []),
      ...(has(input, 'cost') ? AWS_PERMISSIONS.cost.actions : []),
      ...(has(input, 'posture') ? AWS_POSTURE_ACTIONS : []),
    ]),
  ];
  const role = or(input.probeRoleName, '<the probe group IAM role name>');
  const exported = awsExport(input);
  if (format === 'cli') {
    const document = {
      Version: '2012-10-17',
      Statement: [
        { Sid: 'obzly', Effect: 'Allow', Action: actions, Resource: '*' },
        ...(exported ? awsExportStatements(exported.bucket, exported.prefix) : []),
      ],
    };
    return {
      script: [
        '#!/usr/bin/env bash',
        `# Obzly read-only access for the probe group's own IAM role: ${input.uses.join(', ')}`,
        'set -euo pipefail',
        `aws iam put-role-policy --role-name ${shellQuote(role)} --policy-name obzly-readonly \\`,
        `  --policy-document ${shellQuote(JSON.stringify(document))}`,
        '',
      ].join('\n'),
      files: [],
    };
  }
  const template = [
    "AWSTemplateFormatVersion: '2010-09-09'",
    `Description: Obzly read-only access for the probe group's own role (${input.uses.join(', ')})`,
    'Parameters:',
    '  ProbeRoleName:',
    '    Type: String',
    `    Default: ${role}`,
    'Resources:',
    '  ObzlyReadOnly:',
    '    Type: AWS::IAM::ManagedPolicy',
    '    Properties:',
    '      Roles: [!Ref ProbeRoleName]',
    '      PolicyDocument:',
    "        Version: '2012-10-17'",
    '        Statement:',
    '          - Effect: Allow',
    '            Action:',
    ...actions.map((action) => `              - ${action}`),
    "            Resource: '*'",
    ...(exported
      ? [
          '          - Effect: Allow',
          '            Action: s3:GetObject',
          `            Resource: 'arn:aws:s3:::${exported.bucket}/${exported.prefix}*'`,
          '          - Effect: Allow',
          '            Action: s3:ListBucket',
          `            Resource: 'arn:aws:s3:::${exported.bucket}'`,
          '            Condition:',
          '              StringLike:',
          `                's3:prefix': '${exported.prefix}*'`,
        ]
      : []),
    '',
  ].join('\n');
  return {
    script: [
      '#!/usr/bin/env bash',
      'set -euo pipefail',
      'aws cloudformation deploy --stack-name obzly-readonly-probe \\',
      '  --template-file obzly-readonly-probe-policy.yaml --capabilities CAPABILITY_IAM',
      '',
    ].join('\n'),
    files: [{ name: 'obzly-readonly-probe-policy.yaml', content: template }],
  };
};

const azureSubject = (input: TemplateInput) =>
  input.access === 'federated'
    ? probeSubject(input.probeGroup)
    : `system:serviceaccount:${or(input.serviceAccount, 'obzly-probe:obzly-probe')}`;

const azureIssuer = (input: TemplateInput) =>
  input.access === 'federated'
    ? input.issuer
    : or(input.clusterIssuer, '<your cluster OIDC issuer URL>');

const azureBicep = (input: TemplateInput): GeneratedTemplate => {
  const parameters = [`accessMode=${input.access}`];
  if (input.access === 'probe') {
    parameters.push(`principalId=${or(input.principalId, '<managed identity principal ID>')}`);
  } else {
    parameters.push(
      `resourceGroupName=${or(input.resourceGroup, 'obzly')}`,
      `issuer=${azureIssuer(input)}`,
      `subject=${azureSubject(input)}`,
    );
  }
  parameters.push(
    `inventory=${has(input, 'inventory')}`,
    `cost=${has(input, 'cost')}`,
    `posture=${has(input, 'posture')}`,
  );
  const exported = azureExport(input);
  if (exported) {
    parameters.push(
      `costExportStorageAccount=${exported.account}`,
      `costExportResourceGroup=${exported.resourceGroup}`,
    );
  }
  const lines = [
    '#!/usr/bin/env bash',
    `# Obzly read-only access: ${input.uses.join(', ')}`,
    'set -euo pipefail',
    `az account set --subscription ${or(input.scopeId, '<subscription ID>')}`,
  ];
  if (input.access !== 'probe') {
    lines.push(
      `az group create --name ${or(input.resourceGroup, 'obzly')} --location <region> --output none`,
    );
  }
  lines.push(
    'az deployment sub create --name obzly-readonly --location <region> \\',
    '  --template-file obzly-reader.bicep \\',
    `  --parameters ${parameters.join(' ')}`,
  );
  if (has(input, 'cost')) {
    lines.push(
      '',
      '# Optional: existing reservations and savings plans need a tenant-level reader role, which a',
      '# Global Administrator (with access elevated) or a reservation owner can assign:',
      "# az role assignment create --assignee-object-id <the identity's principal ID> --assignee-principal-type ServicePrincipal \\",
      '#   --role "Reservations Reader" --scope /providers/Microsoft.Capacity',
    );
  }
  if (input.access !== 'probe') {
    lines.push(
      '',
      '# The client ID for the next step:',
      'az deployment sub show --name obzly-readonly --query properties.outputs.clientId.value --output tsv',
    );
  }
  return {
    script: `${lines.join('\n')}\n`,
    files: [
      { name: 'obzly-reader.bicep', content: azureReaderTemplate },
      { name: 'obzly-identity.bicep', content: azureIdentityTemplate },
      // obzly-reader.bicep references it even when no export is given.
      { name: 'obzly-cost-export.bicep', content: azureCostExportTemplate },
    ],
  };
};

const azureCLI = (input: TemplateInput): GeneratedTemplate => {
  const scope =
    input.scope === 'management_group'
      ? `/providers/Microsoft.Management/managementGroups/${or(input.scopeId, '<management group ID>')}`
      : `/subscriptions/${or(input.scopeId, '<subscription ID>')}`;
  const lines = [
    '#!/usr/bin/env bash',
    `# Obzly read-only access: ${input.uses.join(', ')}`,
    'set -euo pipefail',
    `SCOPE=${scope}`,
  ];
  if (input.access === 'probe') {
    lines.push(`PRINCIPAL=${or(input.principalId, '<managed identity principal ID>')}`);
  } else {
    const group = or(input.resourceGroup, 'obzly');
    lines.push(
      `GROUP=${group}`,
      'az group create --name "$GROUP" --location <region> --output none',
      'az identity create --name obzly-readonly --resource-group "$GROUP" --output none',
      'PRINCIPAL=$(az identity show --name obzly-readonly --resource-group "$GROUP" --query principalId -o tsv)',
      'az identity federated-credential create --name obzly-probe --identity-name obzly-readonly --resource-group "$GROUP" \\',
      `  --issuer ${azureIssuer(input)} --subject ${azureSubject(input)} --audiences ${AZURE_AUDIENCE}`,
    );
  }
  // Posture's baseline checks read resources' settings, which takes Reader too.
  const roles = new Set(input.uses.map((use) => AZURE_ROLES[use].name));
  if (has(input, 'posture')) roles.add(AZURE_ROLES.inventory.name);
  for (const role of roles) {
    lines.push(
      `az role assignment create --assignee-object-id "$PRINCIPAL" --assignee-principal-type ServicePrincipal \\`,
      `  --role "${role}" --scope "$SCOPE"`,
    );
  }
  const exported = azureExport(input);
  if (exported) {
    lines.push(
      `EXPORT=$(az storage account show --name ${exported.account} --resource-group ${exported.resourceGroup} --query id -o tsv)`,
      `az role assignment create --assignee-object-id "$PRINCIPAL" --assignee-principal-type ServicePrincipal \\`,
      `  --role "${STORAGE_BLOB_DATA_READER}" --scope "$EXPORT"`,
    );
  }
  if (has(input, 'cost')) {
    lines.push(
      '',
      '# Optional: existing reservations and savings plans need a tenant-level reader role, which a',
      '# Global Administrator (with access elevated) or a reservation owner can assign:',
      '# az role assignment create --assignee-object-id "$PRINCIPAL" --assignee-principal-type ServicePrincipal \\',
      '#   --role "Reservations Reader" --scope /providers/Microsoft.Capacity',
    );
  }
  if (input.access !== 'probe') {
    lines.push(
      '',
      '# The client ID for the next step:',
      'az identity show --name obzly-readonly --resource-group "$GROUP" --query clientId -o tsv',
    );
  }
  return { script: `${lines.join('\n')}\n`, files: [] };
};
