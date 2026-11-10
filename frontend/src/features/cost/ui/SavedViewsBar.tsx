import { DeleteOutlined, SaveOutlined, StarFilled, StarOutlined } from '@ant-design/icons';
import { App, Button, Checkbox, Input, Modal, Popconfirm, Select, Tooltip } from 'antd';
import { useEffect, useState } from 'react';

import { type SavedView, type SavedViewKind, savedViewsApi } from '../../../entity/savedViews';

interface Props<State> {
  kind: SavedViewKind;
  state: State;
  /** Called with each view picked, and with the default one once loaded when applyDefault. */
  onOpen: (state: State, view?: SavedView<State>) => void;
  /** False once the tab was opened on something else (a drill-in from the overview). */
  applyDefault?: boolean;
}

export const SavedViewsBar = <State extends object>({
  kind,
  state,
  onOpen,
  applyDefault = true,
}: Props<State>) => {
  const { message } = App.useApp();
  const [views, setViews] = useState<SavedView<State>[]>([]);
  const [defaultId, setDefaultId] = useState<string | null>(null);
  const [openId, setOpenId] = useState<string>();
  const [saving, setSaving] = useState<{ name: string; shared: boolean }>();

  useEffect(() => {
    savedViewsApi
      .list<State>(kind)
      .then((list) => {
        setViews(list.views);
        setDefaultId(list.defaultId);
        const initial = list.views.find((view) => view.id === list.defaultId);
        if (initial && applyDefault) {
          setOpenId(initial.id);
          onOpen(initial.state, initial);
        }
      })
      .catch((reason: Error) => message.error(`Couldn't load saved views: ${reason.message}`));
    // Once per tab: later changes come from this bar.
  }, [kind]);

  const open = views.find((view) => view.id === openId);

  const run = async (action: () => Promise<void>, done: string) => {
    try {
      await action();
      message.success(done);
    } catch (reason) {
      message.error((reason as Error).message);
    }
  };

  const saveAs = () =>
    saving &&
    run(async () => {
      const view = await savedViewsApi.create<State>({ kind, ...saving, state });
      setViews((current) => [...current, view]);
      setOpenId(view.id);
      setSaving(undefined);
    }, 'View saved');

  const update = () =>
    open &&
    run(async () => {
      const view = await savedViewsApi.update<State>(open.id, {
        kind,
        name: open.name,
        shared: open.shared,
        state,
      });
      setViews((current) => current.map((item) => (item.id === view.id ? view : item)));
    }, `Saved “${open.name}”`);

  const remove = () =>
    open &&
    run(async () => {
      await savedViewsApi.remove(open.id);
      setViews((current) => current.filter((item) => item.id !== open.id));
      if (defaultId === open.id) setDefaultId(null);
      setOpenId(undefined);
    }, 'View deleted');

  const toggleDefault = () =>
    open &&
    run(
      async () => {
        const list = await savedViewsApi.setDefault<State>(
          kind,
          defaultId === open.id ? null : open.id,
        );
        setDefaultId(list.defaultId);
      },
      defaultId === open.id ? 'No default view' : `“${open.name}” opens first`,
    );

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        size="small"
        className="w-56"
        placeholder="Saved views"
        allowClear
        value={openId}
        onChange={(id?: string) => {
          setOpenId(id);
          const view = views.find((item) => item.id === id);
          if (view) onOpen(view.state, view);
        }}
        options={[
          {
            label: 'My views',
            options: views
              .filter((view) => view.mine)
              .map((view) => ({
                value: view.id,
                label: `${view.name}${view.shared ? ' (shared)' : ''}`,
              })),
          },
          {
            label: 'Shared with me',
            options: views
              .filter((view) => !view.mine)
              .map((view) => ({ value: view.id, label: `${view.name} · ${view.owner}` })),
          },
        ].filter((group) => group.options.length > 0)}
        notFoundContent="No saved views yet"
      />
      {open && (
        <Tooltip
          title={defaultId === open.id ? 'Opens first; click to clear' : 'Open this view first'}
        >
          <Button
            size="small"
            aria-label="Default view"
            icon={
              defaultId === open.id ? <StarFilled className="text-amber-500" /> : <StarOutlined />
            }
            onClick={toggleDefault}
          />
        </Tooltip>
      )}
      {open?.mine && (
        <Button size="small" icon={<SaveOutlined />} onClick={update}>
          Save
        </Button>
      )}
      <Button size="small" onClick={() => setSaving({ name: '', shared: false })}>
        {open ? 'Save as…' : 'Save view…'}
      </Button>
      {open?.mine && (
        <Popconfirm
          title={`Delete “${open.name}”?`}
          description={open.shared ? 'Everyone it is shared with loses it too.' : undefined}
          okText="Delete"
          okButtonProps={{ danger: true }}
          onConfirm={remove}
        >
          <Button size="small" aria-label="Delete view" icon={<DeleteOutlined />} />
        </Popconfirm>
      )}
      {open && !open.mine && (
        <span className="text-xs text-gray-500 dark:text-gray-400">
          Shared by {open.owner}, read-only: save a copy to change it
        </span>
      )}
      <Modal
        open={!!saving}
        title="Save view"
        okText="Save"
        okButtonProps={{ disabled: !saving?.name.trim() }}
        onOk={saveAs}
        onCancel={() => setSaving(undefined)}
      >
        <div className="flex flex-col gap-3">
          <Input
            autoFocus
            maxLength={60}
            placeholder="Name, e.g. Azure spend by service"
            value={saving?.name}
            onChange={(event) =>
              setSaving((current) => current && { ...current, name: event.target.value })
            }
            onPressEnter={saveAs}
          />
          <Checkbox
            checked={saving?.shared}
            onChange={(event) =>
              setSaving((current) => current && { ...current, shared: event.target.checked })
            }
          >
            Share with everyone (read-only for them)
          </Checkbox>
        </div>
      </Modal>
    </div>
  );
};
