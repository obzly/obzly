import { DeleteOutlined, EditOutlined, LoadingOutlined, TeamOutlined } from '@ant-design/icons';
import { App, Button, Input, Modal, Select, Spin, Switch, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { IS_CONTROL_PLANE, getOAuthRedirectUri } from '../../../constants';
import { mfaApi, userApi } from '../../../entity/users';
import { ssoApi } from '../../../entity/users/api/ssoApi';
import type { CreateSSOConnectorRequest } from '../../../entity/users/model/CreateSSOConnectorRequest';
import type { SSOConnector } from '../../../entity/users/model/SSOConnector';
import { SSOProviderType } from '../../../entity/users/model/SSOProviderType';
import type { UpdateSSOConnectorRequest } from '../../../entity/users/model/UpdateSSOConnectorRequest';
import { MFAConfirmModal } from '../../../shared/ui/MFAConfirmModal';
import { SSOGroupMappingsModal } from './SSOGroupMappingsModal';
import { SSOSiteGroupMappingsModal } from './SSOSiteGroupMappingsModal';

const PROVIDER_LABELS: Record<SSOProviderType, string> = {
  [SSOProviderType.COGNITO]: 'AWS Cognito',
  [SSOProviderType.GOOGLE_WORKSPACE]: 'Google Workspace',
  [SSOProviderType.ENTRA_ID]: 'Microsoft Entra ID',
  [SSOProviderType.GENERIC_OIDC]: 'Generic OIDC',
};

interface FormValues {
  name: string;
  providerType: SSOProviderType;
  issuerUrl: string;
  clientId: string;
  clientSecret: string;
  scopes: string;
  groupsClaim: string;
  requireVerifiedEmail: boolean;
  trustIdpMfa: boolean;
  buttonLabel: string;
  enabled: boolean;
}

const DEFAULT_GROUPS_CLAIM: Partial<Record<SSOProviderType, string>> = {
  [SSOProviderType.COGNITO]: 'cognito:groups',
  [SSOProviderType.ENTRA_ID]: 'groups',
};

const initialFormValues: FormValues = {
  name: '',
  providerType: SSOProviderType.GENERIC_OIDC,
  issuerUrl: '',
  clientId: '',
  clientSecret: '',
  scopes: 'openid email profile',
  groupsClaim: '',
  requireVerifiedEmail: true,
  trustIdpMfa: false,
  buttonLabel: '',
  enabled: true,
};

export const SSOSettingsComponent = () => {
  const { message } = App.useApp();

  const [connectors, setConnectors] = useState<SSOConnector[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [isDeleteModalVisible, setIsDeleteModalVisible] = useState(false);
  const [editingConnector, setEditingConnector] = useState<SSOConnector | null>(null);
  const [deletingConnectorId, setDeletingConnectorId] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const [formValues, setFormValues] = useState<FormValues>(initialFormValues);
  const [mappingsConnector, setMappingsConnector] = useState<SSOConnector | null>(null);
  // Deleting a connector is root-admin only and MFA step-up protected.
  const [isRootAdmin, setIsRootAdmin] = useState(false);
  const [mfaEnabled, setMfaEnabled] = useState(false);

  useEffect(() => {
    loadConnectors();
    userApi
      .getCurrentUser()
      .then((me) => setIsRootAdmin(!!me.isRootAdmin))
      .catch(() => undefined);
    mfaApi
      .getStatus()
      .then((status) => setMfaEnabled(status.enabled))
      .catch(() => undefined);
  }, []);

  const loadConnectors = async () => {
    setIsLoading(true);
    try {
      const fetchedConnectors = await ssoApi.listConnectors();
      setConnectors(fetchedConnectors);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load connectors';
      message.error(errorMessage);
    } finally {
      setIsLoading(false);
    }
  };

  const updateFormValue = <K extends keyof FormValues>(key: K, value: FormValues[K]) => {
    setFormValues((prev) => ({ ...prev, [key]: value }));
  };

  const handleCreate = () => {
    setEditingConnector(null);
    setFormValues(initialFormValues);
    setIsModalVisible(true);
  };

  const handleEdit = (connector: SSOConnector) => {
    setEditingConnector(connector);
    setFormValues({
      name: connector.name,
      providerType: connector.providerType,
      issuerUrl: connector.issuerUrl,
      clientId: connector.clientId,
      clientSecret: '',
      scopes: connector.scopes,
      groupsClaim: connector.groupsClaim,
      requireVerifiedEmail: connector.requireVerifiedEmail,
      trustIdpMfa: connector.trustIdpMfa,
      buttonLabel: connector.buttonLabel,
      enabled: connector.enabled,
    });
    setIsModalVisible(true);
  };

  const handleDelete = (connectorId: string) => {
    setDeletingConnectorId(connectorId);
    setIsDeleteModalVisible(true);
  };

  // Without MFA: plain confirmation. With MFA the code modal calls
  // deleteWithCode, which rethrows so the modal shows the error inline.
  const confirmDelete = async () => {
    if (!deletingConnectorId) return;

    setIsSaving(true);
    try {
      await ssoApi.deleteConnector(deletingConnectorId);
      message.success('Connector deleted successfully');
      setIsDeleteModalVisible(false);
      setDeletingConnectorId(null);
      await loadConnectors();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to delete connector';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const deleteWithCode = async (code: string) => {
    if (!deletingConnectorId) return;
    await ssoApi.deleteConnector(deletingConnectorId, code);
    message.success('Connector deleted successfully');
    setIsDeleteModalVisible(false);
    setDeletingConnectorId(null);
    await loadConnectors();
  };

  const handleModalOk = async () => {
    if (!formValues.name.trim()) {
      message.error('Name is required');
      return;
    }
    if (!formValues.issuerUrl.trim()) {
      message.error('Issuer URL is required');
      return;
    }
    if (!formValues.clientId.trim()) {
      message.error('Client ID is required');
      return;
    }
    if (!editingConnector && !formValues.clientSecret.trim()) {
      message.error('Client secret is required');
      return;
    }
    if (!formValues.buttonLabel.trim()) {
      message.error('Button label is required');
      return;
    }

    setIsSaving(true);
    try {
      if (editingConnector) {
        const requestData: UpdateSSOConnectorRequest = {
          name: formValues.name,
          providerType: formValues.providerType,
          issuerUrl: formValues.issuerUrl,
          clientId: formValues.clientId,
          clientSecret: formValues.clientSecret || undefined,
          scopes: formValues.scopes,
          groupsClaim: formValues.groupsClaim,
          requireVerifiedEmail: formValues.requireVerifiedEmail,
          trustIdpMfa: formValues.trustIdpMfa,
          buttonLabel: formValues.buttonLabel,
          enabled: formValues.enabled,
        };
        await ssoApi.updateConnector(editingConnector.id, requestData);
        message.success('Connector updated successfully');
      } else {
        const requestData: CreateSSOConnectorRequest = {
          name: formValues.name,
          providerType: formValues.providerType,
          issuerUrl: formValues.issuerUrl,
          clientId: formValues.clientId,
          clientSecret: formValues.clientSecret,
          scopes: formValues.scopes,
          groupsClaim: formValues.groupsClaim,
          requireVerifiedEmail: formValues.requireVerifiedEmail,
          trustIdpMfa: formValues.trustIdpMfa,
          buttonLabel: formValues.buttonLabel,
          enabled: formValues.enabled,
        };
        await ssoApi.createConnector(requestData);
        message.success('Connector created successfully');
      }

      setIsModalVisible(false);
      setFormValues(initialFormValues);
      await loadConnectors();
    } catch (error: unknown) {
      if (error instanceof Error && error.message) {
        message.error(error.message);
      }
    } finally {
      setIsSaving(false);
    }
  };

  const handleModalCancel = () => {
    setIsModalVisible(false);
    setFormValues(initialFormValues);
  };

  const columns: ColumnsType<SSOConnector> = [
    {
      title: 'Name',
      dataIndex: 'name',
      key: 'name',
    },
    {
      title: 'Provider',
      dataIndex: 'providerType',
      key: 'providerType',
      render: (providerType: SSOProviderType) => (
        <Tag color="blue">{PROVIDER_LABELS[providerType] || providerType}</Tag>
      ),
    },
    {
      title: 'Issuer URL',
      dataIndex: 'issuerUrl',
      key: 'issuerUrl',
      ellipsis: true,
    },
    {
      title: 'Enabled',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (enabled: boolean) => (
        <Tag color={enabled ? 'green' : 'default'}>{enabled ? 'Yes' : 'No'}</Tag>
      ),
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 120,
      render: (_: unknown, record: SSOConnector) => (
        <div className="flex items-center gap-2">
          <Tooltip title="Edit">
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              aria-label={`Edit ${record.name}`}
              onClick={() => handleEdit(record)}
            />
          </Tooltip>
          <Tooltip title="Group mappings">
            <Button
              type="text"
              size="small"
              icon={<TeamOutlined />}
              aria-label={`Group mappings for ${record.name}`}
              onClick={() => setMappingsConnector(record)}
            />
          </Tooltip>
          <Tooltip title={isRootAdmin ? 'Delete' : 'Only the root admin can delete connectors'}>
            <Button
              type="text"
              size="small"
              danger
              disabled={!isRootAdmin}
              icon={<DeleteOutlined />}
              aria-label={`Delete ${record.name}`}
              onClick={() => handleDelete(record.id)}
            />
          </Tooltip>
        </div>
      ),
    },
  ];

  const deletingConnector = connectors.find((c) => c.id === deletingConnectorId);
  const redirectUri = getOAuthRedirectUri();

  return (
    <div className="max-w-4xl">
      <div className="mb-1 flex items-center justify-between">
        <h2 className="text-xl font-bold text-gray-900 dark:text-gray-100">Single Sign-On</h2>
        <Button
          type="primary"
          onClick={handleCreate}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          Add Connector
        </Button>
      </div>
      <div className="mb-4 text-sm text-gray-500 dark:text-gray-300">
        Configure OIDC-based single sign-on (AWS Cognito, Google Workspace, Microsoft Entra ID, or
        any other standard OpenID Connect provider). Once created and enabled, a matching button
        appears on the sign-in page.
      </div>

      {isLoading ? (
        <div className="flex items-center py-4">
          <Spin indicator={<LoadingOutlined spin />} />
          <span className="ml-2 text-sm text-gray-500 dark:text-gray-300">
            Loading connectors...
          </span>
        </div>
      ) : (
        <Table
          dataSource={connectors}
          columns={columns}
          rowKey="id"
          pagination={false}
          size="small"
        />
      )}

      <Modal
        title={editingConnector ? 'Edit SSO Connector' : 'Add SSO Connector'}
        open={isModalVisible}
        onOk={handleModalOk}
        onCancel={handleModalCancel}
        confirmLoading={isSaving}
        width={600}
        okText={editingConnector ? 'Update' : 'Create'}
        okButtonProps={{
          className:
            'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700',
        }}
      >
        <div className="mt-4 space-y-3">
          <div>
            <label className="mb-1 block text-sm font-medium">
              Name <span className="text-red-500">*</span>
            </label>
            <Input
              placeholder="e.g. Company Cognito"
              value={formValues.name}
              onChange={(e) => updateFormValue('name', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Provider Type <span className="text-red-500">*</span>
            </label>
            <Select
              value={formValues.providerType}
              onChange={(value: SSOProviderType) => {
                updateFormValue('providerType', value);
                const defaultClaim = DEFAULT_GROUPS_CLAIM[value];
                if (defaultClaim && !formValues.groupsClaim) {
                  updateFormValue('groupsClaim', defaultClaim);
                }
              }}
              className="w-full"
            >
              {Object.values(SSOProviderType).map((type) => (
                <Select.Option key={type} value={type}>
                  {PROVIDER_LABELS[type]}
                </Select.Option>
              ))}
            </Select>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Issuer URL <span className="text-red-500">*</span>
            </label>
            <Input
              placeholder="https://cognito-idp.<region>.amazonaws.com/<pool-id>"
              value={formValues.issuerUrl}
              onChange={(e) => updateFormValue('issuerUrl', e.target.value)}
            />
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              Must publish a /.well-known/openid-configuration discovery document.
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Client ID <span className="text-red-500">*</span>
            </label>
            <Input
              value={formValues.clientId}
              onChange={(e) => updateFormValue('clientId', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Client Secret {!editingConnector && <span className="text-red-500">*</span>}
            </label>
            <Input.Password
              placeholder={editingConnector ? 'Leave blank to keep existing secret' : ''}
              value={formValues.clientSecret}
              onChange={(e) => updateFormValue('clientSecret', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Scopes</label>
            <Input
              value={formValues.scopes}
              onChange={(e) => updateFormValue('scopes', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Groups claim (optional)</label>
            <Input
              placeholder="cognito:groups"
              value={formValues.groupsClaim}
              onChange={(e) => updateFormValue('groupsClaim', e.target.value)}
            />
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              Name of the ID-token claim listing the user&apos;s groups. Set it to map groups to
              project roles (AWS Cognito: <code>cognito:groups</code>; Microsoft Entra ID:{' '}
              <code>groups</code>, which carries group Object IDs and needs a groups claim under the
              app registration&apos;s Token configuration), then use the connector&apos;s
              &quot;Group mappings&quot; button. Leave blank to manage access by hand.
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Button Label <span className="text-red-500">*</span>
            </label>
            <Input
              placeholder="Continue with Cognito"
              value={formValues.buttonLabel}
              onChange={(e) => updateFormValue('buttonLabel', e.target.value)}
            />
          </div>

          <div>
            <div className="flex items-center space-x-2">
              <Switch
                checked={formValues.requireVerifiedEmail}
                onChange={(checked) => updateFormValue('requireVerifiedEmail', checked)}
                style={{ backgroundColor: formValues.requireVerifiedEmail ? '#059669' : undefined }}
              />
              <span className="text-sm">Require verified email (recommended)</span>
            </div>
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              A sign-in is matched to an existing account, or creates one, by email, so the provider
              must confirm the address (<code>email_verified</code>). Users already linked to this
              provider are not affected. Cognito sends <code>false</code> for users created without
              &quot;Mark email as verified&quot; and needs the app client to be allowed to read that
              attribute. Entra ID never sends it (only the optional <code>xms_edov</code>
              claim). Turn this off only if you fully control who can register at the provider.
            </div>
          </div>

          <div>
            <div className="flex items-center space-x-2">
              <Switch
                checked={formValues.trustIdpMfa}
                onChange={(checked) => updateFormValue('trustIdpMfa', checked)}
                style={{ backgroundColor: formValues.trustIdpMfa ? '#059669' : undefined }}
              />
              <span className="text-sm">Provider enforces MFA</span>
            </div>
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              When on, a sign-in whose ID token shows the provider used a second factor (an{' '}
              <code>amr</code> claim with <code>mfa</code>, <code>otp</code>, <code>hwk</code>,{' '}
              <code>swk</code>, <code>sms</code> or <code>fido</code>) is not asked for an Obzly
              authenticator code, and admins are not asked to set one up. Sign-ins without such a
              claim still need Obzly MFA - many providers (Entra ID v2 tokens, Cognito, Google) only
              send it when configured to. Password sign-ins to the same account always need Obzly
              MFA. Off by default.
            </div>
          </div>

          <div className="flex items-center space-x-2">
            <Switch
              checked={formValues.enabled}
              onChange={(checked) => updateFormValue('enabled', checked)}
              style={{ backgroundColor: formValues.enabled ? '#059669' : undefined }}
            />
            <span className="text-sm">Enabled</span>
          </div>

          <div className="rounded border border-gray-200 bg-gray-50 p-3 text-xs text-gray-600 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-300">
            Redirect URI to register with your identity provider:
            <div className="mt-1 font-mono break-all">{redirectUri}</div>
          </div>
        </div>
      </Modal>

      {/* A control plane has no projects of its own, so groups map to Sites. */}
      {mappingsConnector &&
        (IS_CONTROL_PLANE ? (
          <SSOSiteGroupMappingsModal
            connector={mappingsConnector}
            onClose={() => setMappingsConnector(null)}
          />
        ) : (
          <SSOGroupMappingsModal
            connector={mappingsConnector}
            onClose={() => setMappingsConnector(null)}
          />
        ))}

      {isDeleteModalVisible && mfaEnabled && (
        <MFAConfirmModal
          open
          title="Delete Connector"
          description={`Enter your current 6-digit authenticator code to delete the connector "${deletingConnector?.name ?? ''}". Users linked to it keep their accounts but lose this sign-in method. This cannot be undone.`}
          confirmText="Delete connector"
          onCancel={() => {
            setIsDeleteModalVisible(false);
            setDeletingConnectorId(null);
          }}
          onConfirm={deleteWithCode}
        />
      )}

      <Modal
        title="Delete Connector"
        open={isDeleteModalVisible && !mfaEnabled}
        onOk={confirmDelete}
        onCancel={() => {
          setIsDeleteModalVisible(false);
          setDeletingConnectorId(null);
        }}
        confirmLoading={isSaving}
        okText="Delete"
        okButtonProps={{ danger: true }}
      >
        <p>
          Are you sure you want to delete the connector <strong>{deletingConnector?.name}</strong>?
          This action cannot be undone.
        </p>
      </Modal>
    </div>
  );
};
