// What the cloud connection wizard asks the user to run, as a template or a
// CLI script. Every permission is read-only.
import type { CloudAccess, CloudProvider, CloudScope, CloudUse } from '../../../entity/cloud';
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
  cost: {
    managed: [],
    actions: [
      'ce:GetCostAndUsage',
      'ce:GetCostForecast',
      'ce:GetDimensionValues',
      'ce:GetAnomalies',
      'budgets:ViewBudget',
      'cur:DescribeReportDefinitions',
    ],
  },
  posture: {
    managed: ['SecurityAudit'],
    actions: ['securityhub:Get*', 'securityhub:List*', 'guardduty:Get*', 'guardduty:List*'],
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

const awsUseParameters = (input: TemplateInput) => [
  `Inventory=${has(input, 'inventory')}`,
  `Cost=${has(input, 'cost')}`,
  `Posture=${has(input, 'posture')}`,
];

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
  const statements = input.uses
    .filter((use) => AWS_PERMISSIONS[use].actions.length > 0)
    .map((use) => ({
      Sid: use,
      Effect: 'Allow',
      Action: AWS_PERMISSIONS[use].actions,
      Resource: '*',
    }));
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
    ...(has(input, 'inventory') ? AWS_INVENTORY_ACTIONS : []),
    ...input.uses
      .filter((use) => use !== 'inventory')
      .flatMap((use) => AWS_PERMISSIONS[use].actions),
  ];
  const role = or(input.probeRoleName, '<the probe group IAM role name>');
  if (format === 'cli') {
    const document = {
      Version: '2012-10-17',
      Statement: [{ Sid: 'obzly', Effect: 'Allow', Action: actions, Resource: '*' }],
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
  for (const use of input.uses) {
    lines.push(
      `az role assignment create --assignee-object-id "$PRINCIPAL" --assignee-principal-type ServicePrincipal \\`,
      `  --role "${AZURE_ROLES[use].name}" --scope "$SCOPE"`,
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
