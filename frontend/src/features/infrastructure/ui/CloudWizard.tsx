// Provider first; every later step shows only that provider's options.
import { CheckOutlined, CopyOutlined, DownloadOutlined, LoadingOutlined } from '@ant-design/icons';
import {
  Alert,
  App,
  Button,
  Checkbox,
  Drawer,
  Input,
  Radio,
  Segmented,
  Select,
  Spin,
  Steps,
} from 'antd';
import { useEffect, useMemo, useRef, useState } from 'react';

import {
  type CloudAccess,
  type CloudConnection,
  type CloudConnectionRequest,
  type CloudProvider,
  type CloudScope,
  type CloudSetup,
  type CloudTest,
  type CloudUse,
  type PostureSettings,
  cloudConnectionsApi,
} from '../../../entity/cloud';
import type { Destination, Probe } from '../../../entity/synthetics';
import { copyToClipboard, downloadText } from '../../../shared/lib';
import {
  AWS_PERMISSIONS,
  AZURE_ROLES,
  type TemplateFormat,
  generateTemplate,
  probeSubject,
} from '../lib/cloudTemplates';
import { type CostDraft, costDraftFrom, costDraftReady, costSettingsFrom } from '../lib/costChoice';
import { CloudCostStep } from './CloudCostStep';
import { CloudPostureStep } from './CloudPostureStep';
import { ProviderLogo } from './ProviderLogo';

interface Props {
  connection?: CloudConnection;
  /** Every connection, to pick the one a member reads its cost through. */
  connections: CloudConnection[];
  probes: Probe[];
  destinations: Destination[];
  onClose: () => void;
  onSaved: () => void;
}

type StepKey =
  'provider' | 'uses' | 'scope' | 'cost' | 'posture' | 'access' | 'template' | 'settings';

const STEP_LABEL: Record<StepKey, string> = {
  provider: 'Provider',
  uses: 'Scan types',
  scope: 'Scope',
  cost: 'Cost',
  posture: 'Posture',
  access: 'Access',
  template: 'Run template',
  settings: 'Settings and test',
};

// The cost and posture steps only when ticked: cost's export location shapes the template.
const stepsFor = (uses: CloudUse[]): StepKey[] => [
  'provider',
  'uses',
  'scope',
  ...(uses.includes('cost') ? (['cost'] as const) : []),
  ...(uses.includes('posture') ? (['posture'] as const) : []),
  'access',
  'template',
  'settings',
];
const TEST_POLL_MS = 2000;
const TEST_TIMEOUT_MS = 120_000;

const USES: { value: CloudUse; label: string; detail: string; since?: string }[] = [
  {
    value: 'inventory',
    label: 'Infrastructure inventory',
    detail: 'Resources with no agent: load balancers, databases, buckets, functions',
    since: 'v0.56.0',
  },
  {
    value: 'cost',
    label: 'Cost and billing',
    detail: 'Spend by service, account, region and tag; forecast; budgets and anomalies',
    since: 'v1.1.0',
  },
  {
    value: 'posture',
    label: 'Security posture',
    detail:
      'Misconfiguration findings against CIS and provider benchmarks, native or Obzly baseline',
    since: 'v1.1.0',
  },
];

interface ProviderInfo {
  name: string;
  scopes: {
    value: CloudScope;
    label: string;
    detail: string;
    idLabel: string;
    placeholder: string;
  }[];
  access: { value: CloudAccess; label: string; detail: string }[];
  formats: { value: TemplateFormat; label: string }[];
  output: { label: string; placeholder: string; hint: string };
  regionsLabel: string;
  regions: string[];
  types: { value: string; label: string }[];
}

const PROVIDERS: Record<CloudProvider, ProviderInfo> = {
  aws: {
    name: 'Amazon Web Services',
    scopes: [
      {
        value: 'account',
        label: 'One AWS account',
        detail: 'Discover a single account.',
        idLabel: 'Account ID',
        placeholder: '123456789012',
      },
      {
        value: 'organization',
        label: 'Whole AWS Organization',
        detail:
          'Deploy as a StackSet from the management account; every member account gets the read-only role.',
        idLabel: 'Organization ID',
        placeholder: 'o-a1b2c3d4e5',
      },
    ],
    access: [
      {
        value: 'probe',
        label: "The probe's own IAM identity",
        detail:
          'EC2 instance profile, IRSA or EKS Pod Identity of the probe group. Nothing to store.',
      },
      {
        value: 'role',
        label: 'A read-only role in the target account',
        detail: 'Only this probe group can assume it, with an external ID Obzly generates.',
      },
      {
        value: 'federated',
        label: 'Federated through the Obzly control plane',
        detail:
          'For probes with no AWS identity. AWS trusts the control plane as an OIDC provider; its URL must be reachable from AWS over HTTPS.',
      },
    ],
    formats: [
      { value: 'template', label: 'CloudFormation' },
      { value: 'cli', label: 'AWS CLI script' },
    ],
    output: {
      label: 'Role ARN',
      placeholder: 'arn:aws:iam::123456789012:role/obzly-readonly',
      hint: "The stack's RoleArn output, printed at the end of the script",
    },
    regionsLabel: 'Regions (empty: every enabled region)',
    regions: [
      'us-east-1',
      'us-east-2',
      'us-west-2',
      'eu-west-1',
      'eu-central-1',
      'ap-south-1',
      'ap-southeast-1',
      'ap-southeast-2',
      'ap-northeast-1',
    ],
    types: [
      { value: 'ec2_instance', label: 'EC2 instances' },
      { value: 'load_balancer', label: 'Load balancers' },
      { value: 'eks_cluster', label: 'EKS clusters' },
      { value: 'rds_instance', label: 'RDS databases' },
      { value: 'autoscaling_group', label: 'Auto Scaling groups' },
      { value: 'lambda_function', label: 'Lambda functions' },
      { value: 's3_bucket', label: 'S3 buckets' },
    ],
  },
  azure: {
    name: 'Microsoft Azure',
    scopes: [
      {
        value: 'subscription',
        label: 'One subscription',
        detail: 'Discover a single subscription.',
        idLabel: 'Subscription ID',
        placeholder: '0b9f1c2d-0000-0000-0000-000000000000',
      },
      {
        value: 'management_group',
        label: 'Management group',
        detail: 'Every subscription under the group, through one role assignment at the group.',
        idLabel: 'Management group ID',
        placeholder: 'mg-production',
      },
    ],
    access: [
      {
        value: 'probe',
        label: "The probe's managed identity",
        detail:
          'The probe group runs on Azure VMs or AKS with a managed identity. Nothing to store.',
      },
      {
        value: 'workload',
        label: 'A user-assigned identity with a federated credential',
        detail:
          "Trusts the probe's Kubernetes service account (AKS or EKS workload identity); no secret.",
      },
      {
        value: 'federated',
        label: 'Federated through the Obzly control plane',
        detail:
          "For probes outside Azure. The identity trusts the control plane's issuer URL, which must be reachable from Azure over HTTPS.",
      },
    ],
    formats: [
      { value: 'template', label: 'Bicep' },
      { value: 'cli', label: 'az CLI script' },
    ],
    output: {
      label: 'Client ID',
      placeholder: '5f0c1d7e-2b9a-4e61-8c3f-000000000000',
      hint: "The identity's client ID, printed at the end of the script",
    },
    regionsLabel: 'Locations (empty: all)',
    regions: [
      'eastus',
      'eastus2',
      'westus2',
      'westeurope',
      'northeurope',
      'uksouth',
      'centralindia',
      'southeastasia',
      'australiaeast',
    ],
    types: [
      { value: 'virtual_machine', label: 'Virtual machines' },
      { value: 'vm_scale_set', label: 'Scale sets' },
      { value: 'aks_cluster', label: 'AKS clusters' },
      { value: 'load_balancer', label: 'Load balancers' },
      { value: 'application_gateway', label: 'Application gateways' },
      { value: 'sql_database', label: 'SQL databases' },
      { value: 'postgresql_server', label: 'PostgreSQL servers' },
      { value: 'storage_account', label: 'Storage accounts' },
      { value: 'function_app', label: 'Function apps' },
    ],
  },
};

// Cloud APIs throttle per account and region: full discoveries stay rare, and health scans in
// between re-read only what the last one found.
const INVENTORY_INTERVALS = [
  { value: 1800, label: '30 min' },
  { value: 3600, label: '1 h' },
  { value: 7200, label: '2 h' },
  { value: 14400, label: '4 h (default)' },
  { value: 28800, label: '8 h' },
  { value: 43200, label: '12 h' },
  { value: 86400, label: '24 h' },
];

const HEALTH_INTERVALS = [
  { value: 300, label: '5 min' },
  { value: 600, label: '10 min' },
  { value: 900, label: '15 min (default)' },
  { value: 1800, label: '30 min' },
  { value: 3600, label: '1 h' },
];

const defaultScope = (provider: CloudProvider): CloudScope =>
  provider === 'aws' ? 'account' : 'subscription';

export const CloudWizard = ({
  connection,
  connections,
  probes,
  destinations,
  onClose,
  onSaved,
}: Props) => {
  const { message } = App.useApp();
  const editing = !!connection;
  const [step, setStep] = useState(editing ? stepsFor(connection.scanTypes).length - 1 : 0);
  const [setup, setSetup] = useState<CloudSetup>();
  const [provider, setProvider] = useState<CloudProvider | undefined>(connection?.provider);
  const [uses, setUses] = useState<CloudUse[]>(connection?.scanTypes ?? ['inventory']);
  const [scope, setScope] = useState<CloudScope>(connection?.scope ?? 'account');
  const [scopeId, setScopeId] = useState(connection?.scopeId ?? '');
  const [tenantId, setTenantId] = useState(connection?.tenantId ?? '');
  const [name, setName] = useState(connection?.name ?? '');
  const [probeId, setProbeId] = useState(connection?.probe.id ?? '');
  const [destinationId, setDestinationId] = useState(
    connection?.destination.id ?? (destinations.length === 1 ? destinations[0].id : ''),
  );
  const [access, setAccess] = useState<CloudAccess>(connection?.access ?? 'probe');
  const [identity, setIdentity] = useState(connection?.identity ?? '');
  const [probeRoleArn, setProbeRoleArn] = useState('');
  const [principalId, setPrincipalId] = useState('');
  const [resourceGroup, setResourceGroup] = useState('obzly');
  const [clusterIssuer, setClusterIssuer] = useState('');
  const [serviceAccount, setServiceAccount] = useState('obzly-probe:obzly-probe');
  const [format, setFormat] = useState<TemplateFormat>('template');
  const [ran, setRan] = useState(editing);
  const [regions, setRegions] = useState<string[]>(connection?.regions ?? []);
  const [types, setTypes] = useState<string[]>(connection?.resourceTypes ?? []);
  const [interval, setInterval] = useState(connection?.intervalSeconds ?? 14400);
  const [healthInterval, setHealthInterval] = useState(connection?.healthIntervalSeconds ?? 900);
  const [cost, setCost] = useState<CostDraft>(costDraftFrom(connection?.cost));
  const [posture, setPosture] = useState<PostureSettings>(
    connection?.posture ?? { source: 'auto', refreshHours: 12 },
  );
  const [test, setTest] = useState<CloudTest | { error: string }>();
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const stopPolling = useRef(false);

  useEffect(() => {
    cloudConnectionsApi
      .setup()
      .then(setSetup)
      .catch((error: Error) => message.error(error.message));
    return () => {
      stopPolling.current = true;
    };
  }, [message]);

  const steps = stepsFor(uses);
  const stepKey = steps[Math.min(step, steps.length - 1)];
  const info = provider ? PROVIDERS[provider] : undefined;
  // A member read through a payer needs no cost permissions of its own.
  const templateUses = cost.scope === 'via' ? uses.filter((use) => use !== 'cost') : uses;
  const probe = probes.find((item) => item.id === probeId);
  const externalId = connection?.externalId || setup?.externalId || '';
  const needsIdentity = access === 'role' || access === 'federated' || access === 'workload';

  const pickProvider = (next: CloudProvider) => {
    if (next !== provider) {
      setScope(defaultScope(next));
      setAccess('probe');
      setFormat('template');
      setRan(false);
      setTest(undefined);
      setRegions([]);
      setTypes([]);
      setIdentity('');
      setCost(costDraftFrom());
    }
    setProvider(next);
  };

  const generated = useMemo(
    () =>
      provider
        ? generateTemplate(
            {
              provider,
              access,
              scope,
              scopeId,
              uses: templateUses,
              issuer: setup?.issuer ?? '',
              probeGroup: probe?.id ?? '<probe group ID>',
              externalId,
              probeRoleArn,
              probeRoleName: probeRoleArn.split('/').pop() ?? '',
              principalId,
              resourceGroup,
              clusterIssuer,
              serviceAccount,
              costExport:
                cost.source === 'api'
                  ? undefined
                  : {
                      bucket: cost.bucket,
                      prefix: cost.prefix,
                      storageAccount: cost.storageAccount,
                      resourceGroup: cost.resourceGroup,
                    },
            },
            format,
          )
        : undefined,
    [
      provider,
      access,
      scope,
      scopeId,
      templateUses,
      cost,
      setup,
      probe,
      externalId,
      probeRoleArn,
      principalId,
      resourceGroup,
      clusterIssuer,
      serviceAccount,
      format,
    ],
  );

  const request = (): CloudConnectionRequest => ({
    name: name.trim(),
    provider: provider!,
    scope,
    scopeId: scopeId.trim(),
    access,
    identity: identity.trim(),
    externalId: access === 'role' ? externalId : '',
    tenantId: tenantId.trim(),
    probeId,
    destinationId,
    scanTypes: uses,
    regions,
    resourceTypes: types,
    intervalSeconds: interval,
    healthIntervalSeconds: healthInterval,
    cost: uses.includes('cost') && provider ? costSettingsFrom(cost, provider) : undefined,
    posture: uses.includes('posture') ? posture : undefined,
    enabled: connection?.enabled ?? true,
    version: connection?.version,
  });

  const canNext = (() => {
    switch (stepKey) {
      case 'provider':
        return !!provider;
      case 'uses':
        return uses.length > 0;
      case 'scope':
        return (
          !!scopeId.trim() &&
          !!name.trim() &&
          !!probeId &&
          !!destinationId &&
          (provider !== 'azure' || !!tenantId.trim())
        );
      case 'cost':
        return !!provider && costDraftReady(cost, provider);
      case 'access':
        return access !== 'role' || !!probeRoleArn.trim() || editing;
      case 'template':
        return ran && (!needsIdentity || !!identity.trim());
      default:
        return true;
    }
  })();

  const runTest = async () => {
    setTesting(true);
    setTest(undefined);
    stopPolling.current = false;
    try {
      let current = await cloudConnectionsApi.test(request());
      const deadline = Date.now() + TEST_TIMEOUT_MS;
      while (current.status !== 'done' && Date.now() < deadline && !stopPolling.current) {
        await new Promise((resolve) => setTimeout(resolve, TEST_POLL_MS));
        current = await cloudConnectionsApi.getTest(current.id);
      }
      setTest(
        current.status === 'done'
          ? current
          : { error: `No answer from probe group ${current.probe} within two minutes.` },
      );
    } catch (error) {
      setTest({ error: (error as Error).message });
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      if (connection) {
        await cloudConnectionsApi.update(connection.id, request());
        message.success(`Saved "${name}"; the probe group picks it up within 3 minutes`);
      } else {
        await cloudConnectionsApi.create(request());
        message.success(
          `Cloud connection "${name}" saved; the first scan runs within a few minutes`,
        );
      }
      onSaved();
    } catch (error) {
      message.error((error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const copy = async (text: string) => {
    try {
      await copyToClipboard(text);
      message.success('Copied');
    } catch {
      message.error("Couldn't copy; select the text instead");
    }
  };

  const body = () => {
    if (stepKey === 'provider') {
      return (
        <Section
          title="Which cloud?"
          detail="One connection is read-only access to one AWS account or Organization, or one Azure subscription or management group."
        >
          <div role="radiogroup" aria-label="Provider" className="grid gap-3">
            {(Object.keys(PROVIDERS) as CloudProvider[]).map((key) => (
              <button
                key={key}
                type="button"
                role="radio"
                aria-checked={provider === key}
                onClick={() => pickProvider(key)}
                className={`flex cursor-pointer items-center gap-4 rounded-lg border bg-white p-4 text-left text-base font-medium dark:bg-gray-900 ${provider === key ? 'border-emerald-600 ring-2 ring-emerald-600/30' : 'border-gray-200 hover:border-gray-400 dark:border-gray-700'}`}
              >
                <ProviderLogo provider={key} size={28} />
                {PROVIDERS[key].name}
                {provider === key && <CheckOutlined className="ml-auto text-emerald-600" />}
              </button>
            ))}
          </div>
        </Section>
      );
    }
    if (!info || !provider) return null;
    switch (stepKey) {
      case 'uses':
        return (
          <Section
            title="What should this connection scan?"
            detail="Each scan type adds only the read-only permissions it needs. Adding one later gives you an updated template to run."
          >
            <Checkbox.Group
              className="flex flex-col gap-3"
              value={uses}
              onChange={(values) => setUses(values as CloudUse[])}
            >
              {USES.map((use) => (
                <Checkbox key={use.value} value={use.value} disabled={!use.since}>
                  <span className="font-medium">{use.label}</span>
                  <span className="ml-2 rounded bg-gray-100 px-1.5 text-[11px] text-gray-500 dark:bg-gray-700 dark:text-gray-300">
                    {use.since ?? 'later'}
                  </span>
                  <span className="block text-xs text-gray-500 dark:text-gray-400">
                    {use.detail}
                  </span>
                </Checkbox>
              ))}
            </Checkbox.Group>
            <div className="rounded-md border border-gray-200 p-3 dark:border-gray-700">
              <div className="mb-1 text-[11px] tracking-wide text-gray-500 uppercase dark:text-gray-400">
                Read-only {provider === 'aws' ? 'AWS' : 'Azure'} permissions this needs
              </div>
              {templateUses.map((use) => (
                <div key={use} className="font-mono text-xs text-gray-700 dark:text-gray-300">
                  {provider === 'aws'
                    ? [
                        ...AWS_PERMISSIONS[use].managed.map((policy) => `${policy} (AWS managed)`),
                        ...AWS_PERMISSIONS[use].actions,
                      ].join(' · ')
                    : `${AZURE_ROLES[use].name} role${use === 'posture' ? ' and Reader (the baseline checks read resource settings)' : ''}`}
                </div>
              ))}
            </div>
          </Section>
        );
      case 'scope': {
        const scopeInfo = info.scopes.find((item) => item.value === scope) ?? info.scopes[0];
        return (
          <Section title={`Which ${provider === 'aws' ? 'account' : 'subscription'}?`}>
            <Radio.Group
              className="flex flex-col gap-2"
              value={scope}
              onChange={(event) => setScope(event.target.value as CloudScope)}
              options={info.scopes.map((item) => ({
                value: item.value,
                label: (
                  <span>
                    <span className="font-medium">{item.label}</span>
                    <span className="block text-xs text-gray-500 dark:text-gray-400">
                      {item.detail}
                    </span>
                  </span>
                ),
              }))}
            />
            <Field label={scopeInfo.idLabel}>
              <Input
                className="font-mono"
                value={scopeId}
                placeholder={scopeInfo.placeholder}
                onChange={(event) => setScopeId(event.target.value)}
              />
            </Field>
            {provider === 'azure' && (
              <Field label="Microsoft Entra tenant ID">
                <Input
                  className="font-mono"
                  value={tenantId}
                  placeholder="11111111-2222-3333-4444-555555555555"
                  onChange={(event) => setTenantId(event.target.value)}
                />
              </Field>
            )}
            <Field label="Connection name">
              <Input
                value={name}
                placeholder={provider === 'aws' ? 'aws-production' : 'azure-production'}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field label="Probe group that runs the scans">
              <Select
                value={probeId || undefined}
                placeholder="Choose a probe group"
                onChange={setProbeId}
                options={probes.map((item) => ({
                  value: item.id,
                  label: `${item.name} (${item.location}${item.members?.length ? `, ${item.members.filter((member) => member.live).length} live` : ''})`,
                }))}
                notFoundContent="Add a probe group in Synthetics first"
              />
            </Field>
            <Field label="Results go to">
              <Select
                value={destinationId || undefined}
                placeholder="Choose a destination"
                onChange={setDestinationId}
                options={destinations.map((item) => ({
                  value: item.id,
                  label: `${item.name} (${item.siteId})`,
                }))}
                notFoundContent="Add a destination in Synthetics first"
              />
            </Field>
          </Section>
        );
      }
      case 'cost':
        return (
          <Section
            title="Cost and billing"
            detail="Where the cost data comes from. Each figure in the Cost tab shows its source, and you can change it later without losing history."
          >
            <CloudCostStep
              provider={provider}
              draft={cost}
              onChange={setCost}
              connections={connections}
              selfId={connection?.id}
            />
          </Section>
        );
      case 'posture':
        return (
          <Section
            title="Security posture"
            detail="Where findings come from. Each finding and standard score shows its source, and the Coverage view says why a region falls back."
          >
            <CloudPostureStep
              provider={provider}
              settings={posture}
              onChange={setPosture}
              lastRefresh={connection?.status.posture?.posture}
            />
          </Section>
        );
      case 'access':
        return (
          <Section
            title="How does the probe group get read-only access?"
            detail={`Obzly stores only identifiers (${provider === 'aws' ? 'account ID, role ARN, external ID' : 'subscription and tenant ID, client ID'}), never keys or secrets.`}
          >
            <Radio.Group
              className="flex flex-col gap-2"
              value={access}
              onChange={(event) => {
                setAccess(event.target.value as CloudAccess);
                setRan(false);
              }}
              options={info.access.map((item) => ({
                value: item.value,
                label: (
                  <span>
                    <span className="font-medium">{item.label}</span>
                    <span className="block text-xs text-gray-500 dark:text-gray-400">
                      {item.detail}
                    </span>
                  </span>
                ),
              }))}
            />
            {provider === 'aws' && access === 'role' && (
              <Field
                label="The probe group's IAM role ARN"
                hint={`The role the read-only role trusts, with external ID ${externalId}`}
              >
                <Input
                  className="font-mono"
                  value={probeRoleArn}
                  placeholder="arn:aws:iam::111122223333:role/obzly-probe"
                  onChange={(event) => setProbeRoleArn(event.target.value)}
                />
              </Field>
            )}
            {provider === 'aws' && access === 'probe' && (
              <Field label="The probe group's IAM role ARN (for the policy)">
                <Input
                  className="font-mono"
                  value={probeRoleArn}
                  placeholder="arn:aws:iam::111122223333:role/obzly-probe"
                  onChange={(event) => setProbeRoleArn(event.target.value)}
                />
              </Field>
            )}
            {provider === 'azure' && access === 'probe' && (
              <>
                <Field
                  label="Managed identity principal (object) ID"
                  hint="Gets the role assignments"
                >
                  <Input
                    className="font-mono"
                    value={principalId}
                    onChange={(event) => setPrincipalId(event.target.value)}
                  />
                </Field>
                <Field
                  label="Its client ID (user-assigned identities only)"
                  hint="Leave empty for a system-assigned identity"
                >
                  <Input
                    className="font-mono"
                    value={identity}
                    onChange={(event) => setIdentity(event.target.value)}
                  />
                </Field>
              </>
            )}
            {provider === 'azure' && access !== 'probe' && (
              <Field label="Resource group for the new identity">
                <Input
                  value={resourceGroup}
                  onChange={(event) => setResourceGroup(event.target.value)}
                />
              </Field>
            )}
            {provider === 'azure' && access === 'workload' && (
              <>
                <Field label="Your cluster's OIDC issuer URL">
                  <Input
                    className="font-mono"
                    value={clusterIssuer}
                    placeholder="https://westeurope.oic.prod-aks.azure.com/<tenant>/<guid>/"
                    onChange={(event) => setClusterIssuer(event.target.value)}
                  />
                </Field>
                <Field
                  label="Probe service account (namespace:name)"
                  hint="Label the probe pods azure.workload.identity/use=true"
                >
                  <Input
                    className="font-mono"
                    value={serviceAccount}
                    onChange={(event) => setServiceAccount(event.target.value)}
                  />
                </Field>
              </>
            )}
            {access === 'federated' && (
              <Alert
                type="info"
                showIcon
                title={`The cloud will trust ${setup?.issuer || 'this control plane'} for subject ${probeSubject(probe?.id ?? '<probe group ID>')}`}
                description="It reads the issuer's /.well-known/openid-configuration and JWKS over HTTPS, so the control plane's URL must be reachable from the cloud."
              />
            )}
          </Section>
        );
      case 'template':
        return (
          <Section
            title={`Run this ${provider === 'aws' ? (scope === 'organization' ? 'in the management account' : 'in the target account') : scope === 'management_group' ? 'for the management group' : 'in the subscription'}`}
          >
            <Segmented<TemplateFormat> value={format} onChange={setFormat} options={info.formats} />
            {provider === 'azure' && scope === 'management_group' && format === 'template' && (
              <span className="text-xs text-gray-500 dark:text-gray-400">
                A management group uses the az CLI script.
              </span>
            )}
            <pre className="m-0 max-h-80 overflow-auto rounded bg-gray-50 p-3 font-mono text-xs whitespace-pre dark:bg-gray-900">
              {generated?.script}
            </pre>
            <div className="flex flex-wrap gap-2">
              <Button
                icon={<CopyOutlined />}
                onClick={() => generated && void copy(generated.script)}
              >
                Copy
              </Button>
              <Button
                icon={<DownloadOutlined />}
                onClick={() =>
                  generated &&
                  downloadText('obzly-readonly.sh', generated.script, 'text/x-shellscript')
                }
              >
                Download script
              </Button>
              {generated?.files.map((file) => (
                <Button
                  key={file.name}
                  icon={<DownloadOutlined />}
                  onClick={() => downloadText(file.name, file.content, 'text/plain')}
                >
                  {file.name}
                </Button>
              ))}
            </div>
            {needsIdentity && (
              <Field label={info.output.label} hint={info.output.hint}>
                <Input
                  className="font-mono"
                  value={identity}
                  placeholder={info.output.placeholder}
                  onChange={(event) => setIdentity(event.target.value)}
                />
              </Field>
            )}
            <Checkbox checked={ran} onChange={(event) => setRan(event.target.checked)}>
              I&apos;ve run it
            </Checkbox>
          </Section>
        );
      default:
        return (
          <Section title={uses.includes('inventory') ? 'Inventory settings' : 'Test access'}>
            {uses.includes('inventory') && (
              <>
                <Field label={info.regionsLabel}>
                  <Select
                    mode="tags"
                    value={regions}
                    placeholder="All"
                    onChange={setRegions}
                    options={info.regions.map((region) => ({ value: region, label: region }))}
                  />
                </Field>
                <Field label="Resource types (none ticked: all)">
                  <Checkbox.Group
                    className="grid grid-cols-2 gap-1"
                    value={types}
                    onChange={(values) => setTypes(values as string[])}
                    options={info.types}
                  />
                </Field>
                <div className="flex flex-wrap gap-4">
                  <Field label="Full discovery every">
                    <Select
                      className="w-44"
                      value={interval}
                      onChange={(value: number) => {
                        setInterval(value);
                        setHealthInterval((current) => Math.min(current, value));
                      }}
                      options={INVENTORY_INTERVALS}
                    />
                  </Field>
                  <Field label="Health scan every">
                    <Select
                      className="w-44"
                      value={healthInterval}
                      onChange={setHealthInterval}
                      options={HEALTH_INTERVALS.filter((option) => option.value <= interval)}
                    />
                  </Field>
                </div>
                <span className="text-xs text-gray-500 dark:text-gray-400">
                  Discovery lists everything, so new resources appear after the next one. Health
                  scans re-read the state, status checks and load balancer targets of what it found.
                  Calls are paced per account and region, and the probe waits whenever AWS or Azure
                  asks it to slow down.
                </span>
              </>
            )}
            {uses.includes('inventory') && <div className="mt-2 font-semibold">Test access</div>}
            <span className="text-xs text-gray-500 dark:text-gray-400">
              One dry scan per use on a live member of {probe?.name ?? 'the probe group'}, of the
              first account and up to two regions so it answers quickly; nothing is sent to a Site.
            </span>
            <TestResult test={test} testing={testing} />
          </Section>
        );
    }
  };

  const last = step >= steps.length - 1;
  return (
    <Drawer
      open
      onClose={onClose}
      size={Math.min(640, window.innerWidth)}
      title={
        <span className="inline-flex items-center gap-2">
          {provider && <ProviderLogo provider={provider} />}
          {editing ? `Edit ${connection.name}` : 'New cloud connection'}
        </span>
      }
      footer={
        <div className="flex justify-between">
          <Button
            onClick={() => setStep(step - 1)}
            disabled={step === 0 || (editing && step === 1)}
            style={{ visibility: step === 0 ? 'hidden' : 'visible' }}
          >
            Back
          </Button>
          <span className="flex gap-2">
            {last && (
              <Button onClick={runTest} loading={testing} disabled={!probeId}>
                Test access
              </Button>
            )}
            <Button
              type="primary"
              disabled={!canNext}
              loading={saving}
              onClick={() => (last ? void save() : setStep(step + 1))}
            >
              {last ? 'Save connection' : 'Next'}
            </Button>
          </span>
        </div>
      }
    >
      {!setup ? (
        <div className="flex justify-center py-16">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <div className="flex flex-col gap-5">
          <Steps
            size="small"
            current={step}
            items={steps.map((key, index) => ({
              title: STEP_LABEL[key],
              disabled: index > step || (editing && index === 0),
            }))}
            onChange={(next) => next < step && !(editing && next === 0) && setStep(next)}
            titlePlacement="vertical"
          />
          {body()}
        </div>
      )}
    </Drawer>
  );
};

const Section = ({
  title,
  detail,
  children,
}: {
  title: string;
  detail?: string;
  children: React.ReactNode;
}) => (
  <div className="flex flex-col gap-3">
    <div>
      <div className="text-base font-semibold text-gray-900 dark:text-gray-100">{title}</div>
      {detail && <div className="text-sm text-gray-500 dark:text-gray-400">{detail}</div>}
    </div>
    {children}
  </div>
);

const Field = ({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) => (
  <label className="flex flex-col gap-1">
    <span className="text-xs font-medium text-gray-600 dark:text-gray-300">{label}</span>
    {children}
    {hint && <span className="text-[11px] text-gray-500 dark:text-gray-400">{hint}</span>}
  </label>
);

const USE_NAME: Record<string, string> = {
  inventory: 'Inventory',
  cost: 'Cost',
  posture: 'Posture',
};

const STATE_STYLE: Record<string, { type: 'success' | 'warning' | 'error'; label: string }> = {
  ok: { type: 'success', label: 'Works' },
  needs_permission: { type: 'warning', label: 'Needs permission' },
  error: { type: 'error', label: 'Failed' },
};

const TestResult = ({
  test,
  testing,
}: {
  test?: CloudTest | { error: string };
  testing: boolean;
}) => {
  if (testing) {
    return (
      <div className="flex items-center gap-2 text-sm text-gray-500">
        <Spin size="small" indicator={<LoadingOutlined spin />} /> Waiting for the probe group…
      </div>
    );
  }
  if (!test) return <span className="text-sm text-gray-500 dark:text-gray-400">Not run yet.</span>;
  if ('error' in test) return <Alert type="error" showIcon title={test.error} />;
  const scan = test.result!;
  return (
    <div className="flex flex-col gap-2">
      {Object.entries(scan.uses).map(([use, result]) => {
        const style = STATE_STYLE[result.state] ?? STATE_STYLE.error;
        return (
          <Alert
            key={use}
            type={style.type}
            showIcon
            title={`${USE_NAME[use] ?? use}: ${style.label}${use === 'inventory' && result.state !== 'error' ? `, ${scan.resources} resources${scan.regions?.length ? ` in ${scan.regions.length} regions` : ''}` : ''}`}
            description={
              result.message || result.missing?.length ? (
                <span className="text-xs">
                  {result.message}
                  {result.missing?.length ? ` Missing: ${result.missing.join(', ')}` : ''}
                </span>
              ) : undefined
            }
          />
        );
      })}
      <span className="text-xs text-gray-500 dark:text-gray-400">
        Ran on {scan.probe || 'the probe group'} in {scan.durationSeconds.toFixed(1)} s
        {scan.throttled ? `; throttled ${scan.throttled} times` : ''}
        {scan.errors?.length ? `; ${scan.errors.length} calls failed: ${scan.errors[0]}` : ''}
      </span>
    </div>
  );
};
