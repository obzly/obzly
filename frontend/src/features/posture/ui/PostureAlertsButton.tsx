import { BellOutlined } from '@ant-design/icons';
import { App, Button, Modal, Select } from 'antd';
import { useEffect, useState } from 'react';

import { type ProjectChannel, costApi } from '../../../entity/cost';
import { type PostureAlertSettings, postureApi } from '../../../entity/posture';
import { type ProjectResponse, projectApi, splitSystemProject } from '../../../entity/projects';
import type { CostSite } from '../../cost';

// Where new and reopened findings are sent: per project, on its Site.
export const PostureAlertsButton = ({ sites }: { sites: CostSite[] }) => {
  const { message } = App.useApp();
  const [open, setOpen] = useState(false);
  const [siteId, setSiteId] = useState(sites[0]?.siteId ?? '');
  const [projects, setProjects] = useState<ProjectResponse[]>([]);
  const [projectId, setProjectId] = useState<string>();
  const [channels, setChannels] = useState<ProjectChannel[]>([]);
  const [settings, setSettings] = useState<PostureAlertSettings>({
    channelIds: [],
    minSeverity: 'high',
  });
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    (siteId ? projectApi.getProjectsOnSite(siteId) : projectApi.getProjects())
      .then((response) => {
        const regular = splitSystemProject(response.projects).regular;
        setProjects(regular);
        setProjectId(regular[0]?.id);
      })
      .catch((reason: Error) => message.error(reason.message));
  }, [open, siteId, message]);

  useEffect(() => {
    if (!open || !projectId) return;
    Promise.all([costApi.channels(siteId, projectId), postureApi.alertSettings(siteId, projectId)])
      .then(([channelList, current]) => {
        setChannels(channelList);
        setSettings(current);
      })
      .catch((reason: Error) => message.error(reason.message));
  }, [open, siteId, projectId, message]);

  const save = async () => {
    if (!projectId) return;
    setSaving(true);
    try {
      const saved = await postureApi.updateAlertSettings(siteId, projectId, settings);
      setSettings(saved);
      message.success(saved.channelIds.length ? 'Finding alerts on' : 'Finding alerts off');
      setOpen(false);
    } catch (reason) {
      message.error((reason as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Button icon={<BellOutlined />} onClick={() => setOpen(true)}>
        Alerts
      </Button>
      <Modal
        open={open}
        title="Alert on new findings"
        okText="Save"
        confirmLoading={saving}
        okButtonProps={{ disabled: !projectId }}
        onOk={save}
        onCancel={() => setOpen(false)}
      >
        <div className="flex flex-col gap-3">
          <span className="text-sm text-gray-500 dark:text-gray-400">
            A finding alerts once when it opens or reopens; suppressed findings never do. Alerts
            belong to the project the connection delivers findings to.
          </span>
          {sites.length > 1 && (
            <Select
              value={siteId}
              onChange={setSiteId}
              options={sites.map((site) => ({ value: site.siteId, label: site.siteName }))}
            />
          )}
          <Select
            placeholder="Project"
            value={projectId}
            onChange={setProjectId}
            options={projects.map((project) => ({ value: project.id, label: project.name }))}
          />
          <Select
            value={settings.minSeverity}
            onChange={(minSeverity: PostureAlertSettings['minSeverity']) =>
              setSettings((current) => ({ ...current, minSeverity }))
            }
            options={[
              { value: 'critical', label: 'Critical only' },
              { value: 'high', label: 'High and critical' },
              { value: 'medium', label: 'Medium and above' },
              { value: 'low', label: 'Low and above' },
            ]}
          />
          <Select
            mode="multiple"
            allowClear
            placeholder="No channel (alerts off)"
            value={settings.channelIds}
            onChange={(channelIds: string[]) =>
              setSettings((current) => ({ ...current, channelIds }))
            }
            options={channels.map((channel) => ({
              value: channel.id,
              label: `${channel.name} (${channel.kind})`,
            }))}
            notFoundContent="The project has no notification channels"
          />
        </div>
      </Modal>
    </>
  );
};
