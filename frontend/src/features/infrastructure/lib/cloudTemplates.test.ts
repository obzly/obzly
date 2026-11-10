import { describe, expect, it } from 'vitest';

import { type TemplateInput, generateTemplate } from './cloudTemplates';

const input = (overrides: Partial<TemplateInput>): TemplateInput => ({
  provider: 'aws',
  access: 'role',
  scope: 'account',
  scopeId: '123456789012',
  uses: ['inventory'],
  issuer: 'https://obzly.example.com',
  probeGroup: '7d4f2a10-5c3e-4b8a-9f61-2e0d8c4b1a37',
  externalId: 'obz-0123456789abcdef0123456789abcdef',
  probeRoleArn: 'arn:aws:iam::111122223333:role/obzly-probe',
  probeRoleName: 'obzly-probe',
  principalId: '',
  resourceGroup: 'obzly',
  clusterIssuer: 'https://oidc.example.com/abc',
  serviceAccount: 'obzly-probe:obzly-probe',
  ...overrides,
});

describe('generateTemplate', () => {
  it('fills the CloudFormation command with the role, external ID and uses', () => {
    const { script, files } = generateTemplate(input({}), 'template');
    expect(script).toContain('TrustMode=AssumeRole');
    expect(script).toContain('TrustedPrincipalArn=arn:aws:iam::111122223333:role/obzly-probe');
    expect(script).toContain('ExternalId=obz-0123456789abcdef0123456789abcdef');
    expect(script).toContain('Inventory=true Cost=false Posture=false');
    expect(files[0].name).toBe('obzly-readonly-role.yaml');
    expect(files[0].content).toContain('AWS::IAM::Role');
  });

  it('federates through the control plane host with the probe group subject', () => {
    const { script } = generateTemplate(input({ access: 'federated' }), 'cli');
    expect(script).toContain('create-open-id-connect-provider --url https://obzly.example.com');
    expect(script).toContain('obzly.example.com:sub');
    expect(script).toContain('probe:7d4f2a10-5c3e-4b8a-9f61-2e0d8c4b1a37');
  });

  it('deploys an Organization as a StackSet and in the management account', () => {
    const { script } = generateTemplate(
      input({ scope: 'organization', scopeId: 'o-a1b2c3d4e5' }),
      'template',
    );
    expect(script).toContain('create-stack-set');
    expect(script).toContain('ParameterKey=ExternalId,ParameterValue=obz-');
    expect(script).toContain('aws cloudformation deploy');
  });

  it("gives the probe's own role exactly the calls discovery makes", () => {
    const { files } = generateTemplate(input({ access: 'probe' }), 'template');
    expect(files[0].content).toContain('ec2:DescribeInstances');
    expect(files[0].content).not.toContain('ViewOnlyAccess');
  });

  it('assigns Azure roles to a federated identity with the Bicep files', () => {
    const azure = input({
      provider: 'azure',
      access: 'federated',
      scope: 'subscription',
      scopeId: '33333333-3333-3333-3333-333333333333',
      uses: ['inventory'],
    });
    const bicep = generateTemplate(azure, 'template');
    expect(bicep.script).toContain('subject=probe:7d4f2a10-5c3e-4b8a-9f61-2e0d8c4b1a37');
    expect(bicep.script).toContain('issuer=https://obzly.example.com');
    expect(bicep.files.map((file) => file.name)).toEqual([
      'obzly-reader.bicep',
      'obzly-identity.bicep',
      'obzly-cost-export.bicep',
    ]);
    const cli = generateTemplate({ ...azure, access: 'workload' }, 'cli');
    expect(cli.script).toContain('--subject system:serviceaccount:obzly-probe:obzly-probe');
    expect(cli.script).toContain('--role "Reader"');
  });

  it('reads only the cost export: its prefix in S3, its storage account in Azure', () => {
    const exported = input({
      uses: ['cost'],
      costExport: { bucket: 'contoso-billing', prefix: 'focus/' },
    });
    expect(generateTemplate(exported, 'template').script).toContain(
      'Cost=true Posture=false ExportBucket=contoso-billing ExportPrefix=focus/',
    );
    const cli = generateTemplate(exported, 'cli').script;
    expect(cli).toContain('ce:GetCostAndUsage');
    expect(cli).toContain('arn:aws:s3:::contoso-billing/focus/*');
    const probe = generateTemplate({ ...exported, access: 'probe' }, 'template').files[0].content;
    expect(probe).toContain("s3:prefix': 'focus/*'");
    expect(generateTemplate({ ...exported, uses: ['inventory'] }, 'template').script).not.toContain(
      'ExportBucket',
    );

    const azure = input({
      provider: 'azure',
      access: 'federated',
      scope: 'management_group',
      scopeId: 'mg-contoso',
      uses: ['cost'],
      costExport: { storageAccount: 'contosobilling', resourceGroup: 'billing' },
    });
    const azureCli = generateTemplate(azure, 'cli').script;
    expect(azureCli).toContain('--role "Cost Management Reader"');
    expect(azureCli).toContain(
      'az storage account show --name contosobilling --resource-group billing',
    );
    expect(azureCli).toContain('--role "Storage Blob Data Reader" --scope "$EXPORT"');
    const bicep = generateTemplate({ ...azure, scope: 'subscription' }, 'template').script;
    expect(bicep).toContain(
      'costExportStorageAccount=contosobilling costExportResourceGroup=billing',
    );
  });
});

describe('posture permissions', () => {
  it('reads Security Hub and the baseline, with Reader for Azure resources', () => {
    const aws = input({ uses: ['posture'] });
    expect(generateTemplate(aws, 'template').script).toContain('Posture=true');
    const probe = generateTemplate({ ...aws, access: 'probe' }, 'template').files[0].content;
    expect(probe).toContain('securityhub:GetFindings');
    expect(probe).toContain('s3:GetBucketPublicAccessBlock');
    expect(probe).not.toContain('guardduty');

    const azure = generateTemplate(
      input({ provider: 'azure', access: 'federated', scope: 'subscription', uses: ['posture'] }),
      'cli',
    ).script;
    expect(azure).toContain('--role "Security Reader"');
    expect(azure).toContain('--role "Reader"');
  });
});
