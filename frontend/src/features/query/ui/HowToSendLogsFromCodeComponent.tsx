import { CopyOutlined, LoadingOutlined } from '@ant-design/icons';
import { Button, Modal, Spin } from 'antd';
import React, { useEffect, useState } from 'react';

import { type Project, projectApi } from '../../../entity/projects';
import { copyToClipboard } from '../../../shared/lib';
import { CodeUsageComponent } from './CodeUsageComponent';

interface Props {
  projectId: string;
  onClose: () => void;
}

export const HowToSendLogsFromCodeComponent = ({
  projectId,
  onClose,
}: Props): React.JSX.Element => {
  const [project, setProject] = useState<Project | null>(null);
  const [copyingStates, setCopyingStates] = useState<Record<string, boolean>>({});

  const loadInfo = async () => {
    const project = await projectApi.getProject(projectId);
    setProject(project);
  };

  const handleCopyToClipboard = async (text: string) => {
    const type = text === window.origin ? 'obzly-url' : 'project-id';
    setCopyingStates((prev) => ({ ...prev, [type]: true }));

    try {
      await copyToClipboard(text);
    } finally {
      setTimeout(() => {
        setCopyingStates((prev) => ({ ...prev, [type]: false }));
      }, 300);
    }
  };

  useEffect(() => {
    loadInfo();
  }, [projectId]);

  const baseUrl = window.origin;

  return (
    <Modal
      title="How to send logs from code?"
      open={true}
      onCancel={onClose}
      footer={null}
      width={1000}
      style={{ top: 20 }}
    >
      {!project ? (
        <div className="flex justify-center py-8">
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : (
        <div>
          <div style={{ marginBottom: 16 }}>
            {project.isApiKeyRequired && (
              <div
                style={{
                  marginBottom: 16,
                  padding: '8px 12px',
                  backgroundColor: '#fff3cd',
                  border: '1px solid #ffeaa7',
                  borderRadius: '4px',
                }}
              >
                <strong style={{ color: '#856404' }}>
                  📝 API Key Required: This project requires an X-API-Key header. Create an API key
                  in your project settings.
                </strong>
              </div>
            )}

            {project.isFilterByDomain && (
              <div
                style={{
                  marginBottom: 16,
                  padding: '8px 12px',
                  backgroundColor: '#d1ecf1',
                  border: '1px solid #bee5eb',
                  borderRadius: '4px',
                }}
              >
                <strong style={{ color: '#0c5460' }}>
                  🌐 Domain Filtering: This project filters by domain. Allowed domains:{' '}
                  {project.allowedDomains.join(', ')}
                </strong>
              </div>
            )}

            {project.isFilterByIp && (
              <div
                style={{
                  marginBottom: 16,
                  padding: '8px 12px',
                  backgroundColor: '#d1ecf1',
                  border: '1px solid #bee5eb',
                  borderRadius: '4px',
                }}
              >
                <strong style={{ color: '#0c5460' }}>
                  🔒 IP Filtering: This project filters by IP address. Allowed IPs:{' '}
                  {project.allowedIps.join(', ')}
                </strong>
              </div>
            )}
          </div>

          <div className="mb-4 flex">
            <div className="mr-5 w-80">
              <div className="mb-1">
                <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
                  Obzly URL:
                </span>
              </div>
              <div className="flex items-center justify-between rounded border border-gray-200 bg-gray-100 px-3 py-1.5 dark:border-gray-700 dark:bg-gray-700">
                <span className="truncate !font-mono text-xs text-gray-800 dark:text-gray-100">
                  {baseUrl}
                </span>
                <Button
                  type="text"
                  size="small"
                  icon={<CopyOutlined />}
                  loading={copyingStates['obzly-url']}
                  onClick={() => handleCopyToClipboard(baseUrl)}
                  className="ml-2 h-5 min-w-5 p-0.5 text-gray-600 dark:text-gray-300"
                />
              </div>
            </div>

            <div className="w-80">
              <div className="mb-1">
                <span className="text-xs font-medium text-gray-600 dark:text-gray-300">
                  Project ID:
                </span>
              </div>
              <div className="flex items-center justify-between rounded border border-gray-200 bg-gray-100 px-3 py-1.5 dark:border-gray-700 dark:bg-gray-700">
                <span className="truncate !font-mono text-xs text-gray-800 dark:text-gray-100">
                  {projectId}
                </span>
                <Button
                  type="text"
                  size="small"
                  icon={<CopyOutlined />}
                  loading={copyingStates['project-id']}
                  onClick={() => handleCopyToClipboard(projectId)}
                  className="ml-2 h-5 min-w-5 p-0.5 text-gray-600 dark:text-gray-300"
                />
              </div>
            </div>
          </div>

          <CodeUsageComponent
            obzlyHost={baseUrl}
            obzlyProjectId={projectId}
            obzlyApiKey="YOUR_API_KEY_HERE"
            isApiKeyRequired={project.isApiKeyRequired}
          />
        </div>
      )}
    </Modal>
  );
};
