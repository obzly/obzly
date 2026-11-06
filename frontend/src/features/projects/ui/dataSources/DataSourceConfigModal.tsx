import { CopyOutlined } from '@ant-design/icons';
import { Alert, Button, Checkbox, Input, Modal, Select } from 'antd';
import React, { useEffect, useState } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { tomorrow } from 'react-syntax-highlighter/dist/esm/styles/prism';

import { IS_CONTROL_PLANE, getCurrentSiteId } from '../../../../constants';
import { type ApiKey, apiKeyApi } from '../../../../entity/api-keys';
import { ApiKeyStatus } from '../../../../entity/api-keys/model/ApiKeyStatus';
import { copyToClipboard } from '../../../../shared/lib';
import { DataSourceIcon } from './DataSourceIcon';
import type { DataSourceBuildContext, DataSourceDefinition } from './types';

// On a control plane, telemetry must go to the Site's own public URL, never
// window.origin; remember what was used per Site instead.
const siteEndpointKey = () => `obzly_ingest_endpoint:site:${getCurrentSiteId()}`;

function initialEndpoint(): string {
  if (!IS_CONTROL_PLANE) return window.origin;
  try {
    return localStorage.getItem(siteEndpointKey()) ?? '';
  } catch {
    return '';
  }
}

interface Props {
  projectId: string;
  definition: DataSourceDefinition;
  onClose: () => void;
  onApiKeyCreated: (apiKey: ApiKey) => void;
}

/**
 * Config modal for every Data Sources entry. Creates a dedicated API key by
 * default (a key is shown only once); reusing one means pasting its value,
 * which is verified against the stored hash.
 */
export const DataSourceConfigModal = ({
  projectId,
  definition,
  onClose,
  onApiKeyCreated,
}: Props): React.JSX.Element => {
  const [keyName, setKeyName] = useState(`${definition.name} OTel Collector`);
  const [baseUrl, setBaseUrl] = useState(initialEndpoint);
  const [fieldValues, setFieldValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(definition.fields.map((field) => [field.key, field.defaultValue])),
  );
  const [isGenerating, setIsGenerating] = useState(false);
  const [createdApiKey, setCreatedApiKey] = useState<ApiKey | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copyingFile, setCopyingFile] = useState<string | null>(null);
  const [existingKeys, setExistingKeys] = useState<ApiKey[]>([]);
  const [selectedKeyId, setSelectedKeyId] = useState<string | undefined>(undefined);
  const [pastedToken, setPastedToken] = useState('');
  const [existingKeyToken, setExistingKeyToken] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiKeyApi
      .getApiKeys(projectId)
      .then((response) => {
        if (cancelled) return;
        setExistingKeys(response.apiKeys.filter((key) => key.status === ApiKeyStatus.ACTIVE));
      })
      .catch(() => {
        // Optional convenience - without the list the modal just creates a new key.
      });
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const selectedKey = existingKeys.find((key) => key.id === selectedKeyId);

  const updateField = (key: string, value: string) => {
    setFieldValues((prev) => ({ ...prev, [key]: value }));
  };

  const handleGenerate = async () => {
    if (!selectedKey && !keyName.trim()) {
      setError('Name is required');
      return;
    }
    if (!baseUrl.trim()) {
      setError('Obzly endpoint is required');
      return;
    }
    if (IS_CONTROL_PLANE && baseUrl.trim().replace(/\/$/, '') === window.origin) {
      setError(
        "That's the control plane's address - it doesn't accept telemetry. Use the Site's public URL.",
      );
      return;
    }
    if (IS_CONTROL_PLANE) {
      try {
        localStorage.setItem(siteEndpointKey(), baseUrl.trim());
      } catch {
        /* ignore */
      }
    }
    if (selectedKey && !pastedToken.trim()) {
      setError('Paste the value of the selected API key');
      return;
    }

    setError(null);
    setIsGenerating(true);
    try {
      if (selectedKey) {
        const token = pastedToken.trim();
        const validation = await apiKeyApi.verifyApiKey(projectId, { token });
        if (!validation.isValid || validation.apiKeyId !== selectedKey.id) {
          setError(`That value is not the key "${selectedKey.name}" - check it and try again.`);
          return;
        }
        setExistingKeyToken(token);
        return;
      }

      const newApiKey = await apiKeyApi.createApiKey(projectId, { name: keyName.trim() });
      setCreatedApiKey(newApiKey);
      onApiKeyCreated(newApiKey);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setIsGenerating(false);
    }
  };

  const apiKeyValue = createdApiKey?.token ?? existingKeyToken;
  const result = apiKeyValue
    ? (() => {
        const ctx: DataSourceBuildContext = {
          baseUrl: baseUrl.trim().replace(/\/$/, ''),
          apiKey: apiKeyValue,
          fieldValues,
        };
        return definition.build(ctx);
      })()
    : undefined;

  const handleCopy = async (filename: string, content: string) => {
    setCopyingFile(filename);
    try {
      const success = await copyToClipboard(content);
      if (!success) setError('Failed to copy to clipboard - please select and copy manually.');
    } finally {
      setTimeout(() => setCopyingFile(null), 300);
    }
  };

  const files = result?.files ?? (result ? [result] : []);

  return (
    <Modal
      title={
        <div className="flex items-center gap-2">
          <DataSourceIcon sourceId={definition.id} size={22} />
          <span>{definition.name} config</span>
        </div>
      }
      open
      onCancel={onClose}
      footer={
        result
          ? [
              <Button key="close" onClick={onClose}>
                Close
              </Button>,
            ]
          : [
              <Button key="cancel" onClick={onClose}>
                Cancel
              </Button>,
              <Button
                key="generate"
                type="primary"
                loading={isGenerating}
                onClick={handleGenerate}
                className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
              >
                Generate config
              </Button>,
            ]
      }
      width={900}
      maskClosable={false}
    >
      {!result ? (
        <div className="space-y-4 py-2">
          <p className="text-sm text-gray-600 dark:text-gray-300">{definition.description}</p>
          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              API key
            </label>
            <Select
              className="w-full"
              allowClear
              showSearch
              optionFilterProp="label"
              placeholder="Create a new API key (default)"
              value={selectedKeyId}
              onChange={(value) => {
                setSelectedKeyId(value);
                setPastedToken('');
                setError(null);
              }}
              options={existingKeys.map((key) => ({
                value: key.id,
                label: `${key.name} (${key.tokenPrefix})`,
              }))}
            />
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              Leave empty to create a new dedicated key, shown once. Or reuse an existing key to
              avoid creating one per config.
            </div>
          </div>

          {selectedKey ? (
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                Value of &quot;{selectedKey.name}&quot;
              </label>
              <Input.Password
                value={pastedToken}
                onChange={(e) => setPastedToken(e.target.value)}
                placeholder={selectedKey.tokenPrefix}
                autoComplete="off"
              />
              <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                Key values are stored hashed and can&apos;t be shown again, so paste the value you
                saved when creating it. It is checked against this key before use.
              </div>
            </div>
          ) : (
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                New API key name
              </label>
              <Input value={keyName} onChange={(e) => setKeyName(e.target.value)} maxLength={100} />
            </div>
          )}

          <div>
            <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
              Obzly endpoint
            </label>
            <Input
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder={IS_CONTROL_PLANE ? 'https://your-site.example.com' : undefined}
            />
            <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
              {IS_CONTROL_PLANE
                ? "The Site's public URL, reachable from wherever this collector runs. Telemetry goes straight to the Site; the control plane doesn't accept it."
                : "Must be reachable from wherever this collector runs - defaults to the URL you're using right now; change it if that source needs a different address."}
            </div>
          </div>

          {definition.fields.map((field) => (
            <div key={field.key}>
              {field.type !== 'checkbox' && (
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
                  {field.label}
                </label>
              )}
              {field.type === 'checkbox' ? (
                <Checkbox
                  checked={fieldValues[field.key] === 'yes'}
                  onChange={(e) => updateField(field.key, e.target.checked ? 'yes' : 'no')}
                >
                  <span className="text-sm font-medium text-gray-700 dark:text-gray-200">
                    {field.label}
                  </span>
                </Checkbox>
              ) : field.options ? (
                <Select
                  className="w-full"
                  value={fieldValues[field.key]}
                  options={field.options}
                  onChange={(value: string) => updateField(field.key, value)}
                />
              ) : (
                <Input
                  value={fieldValues[field.key]}
                  placeholder={field.placeholder}
                  onChange={(e) => updateField(field.key, e.target.value)}
                />
              )}
              {field.helpText && (
                <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                  {field.helpText}
                </div>
              )}
            </div>
          ))}

          {error && <Alert type="error" message={error} showIcon />}
        </div>
      ) : (
        <div className="space-y-3 py-2">
          <Alert
            type="warning"
            showIcon
            message={createdApiKey ? 'Save this now' : 'Contains an API key'}
            description={
              createdApiKey
                ? `The API key "${createdApiKey.name}" is embedded in this config and won't be shown again. If you lose it, delete this key in the API Keys page and generate a new config.`
                : `The existing API key "${selectedKey?.name}" is embedded in this config - treat the file as a secret.`
            }
          />

          {files.map((file) => (
            <div key={file.filename} className="space-y-1">
              <div className="flex items-center justify-between">
                <code className="!font-mono text-sm font-medium text-gray-700 dark:text-gray-200">
                  {file.filename}
                </code>
                <Button
                  size="small"
                  icon={<CopyOutlined />}
                  loading={copyingFile === file.filename}
                  onClick={() => handleCopy(file.filename, file.content)}
                >
                  Copy
                </Button>
              </div>

              <div className="max-h-[40vh] overflow-auto rounded border border-gray-200 dark:border-gray-700">
                <SyntaxHighlighter
                  language={file.language}
                  style={tomorrow}
                  customStyle={{ margin: 0, fontSize: '12px' }}
                >
                  {file.content}
                </SyntaxHighlighter>
              </div>
            </div>
          ))}

          <div className="text-xs text-gray-500 dark:text-gray-300">{result.instructions}</div>
        </div>
      )}
    </Modal>
  );
};
