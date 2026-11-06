import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { App, Button, Input, List, Modal, Popconfirm } from 'antd';
import React, { useState } from 'react';

import { type Folder, dashboardApi } from '../../../entity/dashboards';

interface Props {
  projectId: string;
  folders: Folder[];
  onChange: () => void;
  onCancel: () => void;
}

// Deleting a folder un-files its dashboards (ON DELETE SET NULL); it doesn't delete them.
export const FolderManagerModalComponent = ({
  projectId,
  folders,
  onChange,
  onCancel,
}: Props): React.JSX.Element => {
  const { message } = App.useApp();
  const [newTitle, setNewTitle] = useState('');
  const [renamingId, setRenamingId] = useState<string | null>(null);

  const handleCreate = async () => {
    const title = newTitle.trim();
    if (!title) return;
    try {
      await dashboardApi.createFolder(projectId, title);
      setNewTitle('');
      onChange();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleRename = async (folderId: string, title: string) => {
    setRenamingId(null);
    if (!title.trim()) return;
    try {
      await dashboardApi.renameFolder(projectId, folderId, title.trim());
      onChange();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const handleDelete = async (folderId: string) => {
    try {
      await dashboardApi.deleteFolder(projectId, folderId);
      onChange();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  return (
    <Modal title="Manage dashboard folders" open onCancel={onCancel} footer={null} width={480}>
      <div className="mb-3 flex gap-2">
        <Input
          placeholder="New folder name"
          value={newTitle}
          onChange={(e) => setNewTitle(e.target.value)}
          onPressEnter={handleCreate}
        />
        <Button icon={<PlusOutlined />} onClick={handleCreate}>
          Add
        </Button>
      </div>

      <List
        dataSource={folders}
        locale={{ emptyText: 'No folders yet' }}
        renderItem={(folder) => (
          <List.Item
            actions={[
              <Popconfirm
                key="delete"
                title="Delete this folder? Its dashboards become unfiled."
                onConfirm={() => handleDelete(folder.id)}
              >
                <Button danger type="text" icon={<DeleteOutlined />} />
              </Popconfirm>,
            ]}
          >
            {renamingId === folder.id ? (
              <Input
                autoFocus
                defaultValue={folder.title}
                onBlur={(e) => handleRename(folder.id, e.target.value)}
                onPressEnter={(e) => e.currentTarget.blur()}
              />
            ) : (
              <span
                className="cursor-pointer"
                onDoubleClick={() => setRenamingId(folder.id)}
                title="Double-click to rename"
              >
                {folder.title}
              </span>
            )}
          </List.Item>
        )}
      />
    </Modal>
  );
};
