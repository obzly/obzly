import { DownloadOutlined, UploadOutlined } from '@ant-design/icons';
import { Alert, App, Button, Drawer, Input, Tag, Upload } from 'antd';
import { useState } from 'react';

import { type ImportPreview, syntheticsApi } from '../../../entity/synthetics';
import { downloadText } from '../../../shared/lib';
import sample from '../lib/synthetics-checks.sample.yaml?raw';

interface Props {
  open: boolean;
  onClose: () => void;
  onApplied: () => void;
}

/** Bulk provisioning from YAML: preview everything first, then apply the whole file or nothing. */
export const ImportDrawer = ({ open, onClose, onApplied }: Props) => {
  const { message } = App.useApp();
  const [yaml, setYaml] = useState('');
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [preview, setPreview] = useState<ImportPreview>();
  const [busy, setBusy] = useState(false);

  const run = async (apply: boolean) => {
    setBusy(true);
    try {
      const values = Object.fromEntries(Object.entries(secrets).filter(([, value]) => value));
      const result = apply
        ? await syntheticsApi.applyImport(yaml, values)
        : await syntheticsApi.previewImport(yaml, values);
      setPreview(result);
      if (apply && result.applied) {
        message.success(
          `Imported: ${result.create.length} created, ${result.update.length} updated, ${result.delete.length} deleted`,
        );
        onApplied();
        close();
      }
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const close = () => {
    setYaml('');
    setSecrets({});
    setPreview(undefined);
    onClose();
  };

  const windows = preview?.maintenance;
  const windowChanges = windows
    ? windows.create.length + windows.update.length + windows.delete.length
    : 0;
  const nothingToDo =
    preview &&
    preview.create.length + preview.update.length + preview.delete.length + windowChanges === 0;

  return (
    <Drawer
      open={open}
      onClose={close}
      size={640}
      title="Import monitors from YAML"
      extra={
        <Button
          type="primary"
          loading={busy}
          disabled={!preview || preview.errors.length > 0 || nothingToDo}
          onClick={() => run(true)}
        >
          Apply
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <p className="m-0 text-sm text-gray-500 dark:text-gray-400">
          Monitors are matched by name, so importing the same file again updates them. Nothing
          changes until you apply, and a file with any error is applied not at all.{' '}
          <code>prune: true</code> deletes monitors that aren&apos;t in the file.
        </p>
        <div className="flex flex-wrap gap-2">
          <Upload
            accept=".yaml,.yml"
            showUploadList={false}
            beforeUpload={(file) => {
              void file.text().then((text) => {
                setYaml(text);
                setPreview(undefined);
              });
              return false;
            }}
          >
            <Button icon={<UploadOutlined />}>Choose file</Button>
          </Upload>
          <Button
            icon={<DownloadOutlined />}
            onClick={() => downloadText('synthetics-checks.yaml', sample, 'application/yaml')}
          >
            Sample file
          </Button>
        </div>
        <Input.TextArea
          className="font-mono"
          rows={12}
          value={yaml}
          placeholder="apiVersion: obzly.com/synthetics/v1&#10;kind: MonitorList&#10;monitors: ..."
          onChange={(event) => {
            setYaml(event.target.value);
            setPreview(undefined);
          }}
        />
        <Button onClick={() => run(false)} loading={busy} disabled={!yaml.trim()}>
          Preview
        </Button>

        {preview && preview.errors.length > 0 && (
          <Alert
            type="error"
            showIcon
            title="Fix these first; nothing was changed"
            description={
              <ul className="m-0 pl-4">
                {preview.errors.map((error, index) => (
                  <li key={index}>
                    {error.line ? `Line ${error.line}: ` : ''}
                    {error.monitor ? <b>{error.monitor}: </b> : null}
                    {error.window ? <b>Maintenance window {error.window}: </b> : null}
                    {error.message}
                  </li>
                ))}
              </ul>
            }
          />
        )}

        {preview && preview.errors.length === 0 && (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-4 gap-2 text-center">
              <Count label="Create" value={preview.create.length} />
              <Count label="Update" value={preview.update.length} />
              <Count label="Unchanged" value={preview.unchanged.length} />
              <Count label="Delete" value={preview.delete.length} danger />
            </div>
            <ul className="m-0 flex list-none flex-col gap-1 p-0 text-sm">
              {preview.create.map((name) => (
                <li key={`c-${name}`}>
                  <Tag color="success">create</Tag>
                  {name}
                </li>
              ))}
              {preview.update.map((change) => (
                <li key={`u-${change.name}`}>
                  <Tag color="warning">update</Tag>
                  {change.name}{' '}
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {change.changes.join(' · ')}
                  </span>
                </li>
              ))}
              {preview.delete.map((name) => (
                <li key={`d-${name}`}>
                  <Tag color="error">delete</Tag>
                  {name}
                </li>
              ))}
              {preview.unchanged.map((name) => (
                <li key={`n-${name}`} className="text-gray-500">
                  <Tag>unchanged</Tag>
                  {name}
                </li>
              ))}
            </ul>
            {windows && windowChanges > 0 && (
              <ul className="m-0 flex list-none flex-col gap-1 p-0 text-sm">
                {windows.create.map((name) => (
                  <li key={`mc-${name}`}>
                    <Tag color="success">create window</Tag>
                    {name}
                  </li>
                ))}
                {windows.update.map((change) => (
                  <li key={`mu-${change.name}`}>
                    <Tag color="warning">update window</Tag>
                    {change.name}{' '}
                    <span className="text-xs text-gray-500 dark:text-gray-400">
                      {change.changes.join(' · ')}
                    </span>
                  </li>
                ))}
                {windows.delete.map((name) => (
                  <li key={`md-${name}`}>
                    <Tag color="error">delete window</Tag>
                    {name}
                  </li>
                ))}
              </ul>
            )}
            {preview.secrets.length > 0 && (
              <div className="flex flex-col gap-2">
                <span className="text-xs font-medium">
                  Secret values (stored encrypted; leave empty to keep the stored value or use the
                  probe&apos;s environment)
                </span>
                {preview.secrets.map((secret) => (
                  <div
                    key={secret.name}
                    className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)] items-center gap-2"
                  >
                    <span className="font-mono text-xs">
                      {'${env:'}
                      {secret.name}
                      {'}'}
                    </span>
                    <Input.Password
                      autoComplete="new-password"
                      placeholder={secret.stored ? 'stored' : 'not stored'}
                      value={secrets[secret.name] ?? ''}
                      onChange={(event) =>
                        setSecrets({ ...secrets, [secret.name]: event.target.value })
                      }
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </Drawer>
  );
};

const Count = ({ label, value, danger }: { label: string; value: number; danger?: boolean }) => (
  <div className="rounded border border-gray-200 py-2 dark:border-gray-700">
    <div className={`text-xl font-semibold ${danger && value > 0 ? 'text-red-500' : ''}`}>
      {value}
    </div>
    <div className="text-xs text-gray-500 uppercase dark:text-gray-400">{label}</div>
  </div>
);
