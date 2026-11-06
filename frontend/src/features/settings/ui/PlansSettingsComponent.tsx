import { LoadingOutlined } from '@ant-design/icons';
import { App, Button, Checkbox, Input, InputNumber, Modal, Select, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useState } from 'react';

import { userPlanApi } from '../../../entity/users/api/userPlanApi';
import type { CreatePlanRequest } from '../../../entity/users/model/CreatePlanRequest';
import type { UpdatePlanRequest } from '../../../entity/users/model/UpdatePlanRequest';
import type { UserPlan } from '../../../entity/users/model/UserPlan';
import { UserPlanType } from '../../../entity/users/model/UserPlanType';

const { TextArea } = Input;

interface FormValues {
  name: string;
  type: UserPlanType;
  isPublic: boolean;
  allowedProjectsCount: number | null;
  warningText: string;
  upgradeText: string;
  logsPerSecondLimit: number | null;
  maxLogsAmount: number | null;
  maxLogsSizeMb: number | null;
  maxLogsLifeDays: number | null;
  maxLogSizeKb: number | null;
}

interface LimitFieldValues {
  allowedProjectsUnlimited: boolean;
  logsPerSecondUnlimited: boolean;
  maxLogsAmountUnlimited: boolean;
  maxLogsSizeMbUnlimited: boolean;
  maxLogsLifeDaysUnlimited: boolean;
  maxLogSizeKbUnlimited: boolean;
}

const initialFormValues: FormValues = {
  name: '',
  type: UserPlanType.DEFAULT,
  isPublic: false,
  allowedProjectsCount: null,
  warningText: '',
  upgradeText: '',
  logsPerSecondLimit: null,
  maxLogsAmount: null,
  maxLogsSizeMb: null,
  maxLogsLifeDays: null,
  maxLogSizeKb: null,
};

export const PlansSettingsComponent = () => {
  const { message } = App.useApp();

  const [plans, setPlans] = useState<UserPlan[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [isDeleteModalVisible, setIsDeleteModalVisible] = useState(false);
  const [editingPlan, setEditingPlan] = useState<UserPlan | null>(null);
  const [deletingPlanId, setDeletingPlanId] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const [isUnsaved, setIsUnsaved] = useState(false);

  const [formValues, setFormValues] = useState<FormValues>(initialFormValues);

  const [limitUnlimited, setLimitUnlimited] = useState<LimitFieldValues>({
    allowedProjectsUnlimited: false,
    logsPerSecondUnlimited: false,
    maxLogsAmountUnlimited: false,
    maxLogsSizeMbUnlimited: false,
    maxLogsLifeDaysUnlimited: false,
    maxLogSizeKbUnlimited: false,
  });

  useEffect(() => {
    loadPlans();
  }, []);

  const loadPlans = async () => {
    setIsLoading(true);
    try {
      const fetchedPlans = await userPlanApi.getPlans();
      setPlans(fetchedPlans);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load plans';
      message.error(errorMessage);
    } finally {
      setIsLoading(false);
    }
  };

  const checkForChanges = () => {
    if (!editingPlan) {
      setIsUnsaved(true);
      return;
    }

    const currentAllowedProjects = limitUnlimited.allowedProjectsUnlimited
      ? 0
      : formValues.allowedProjectsCount || 0;

    const hasBasicChanges =
      formValues.name !== editingPlan.name ||
      formValues.type !== editingPlan.type ||
      formValues.isPublic !== editingPlan.isPublic ||
      currentAllowedProjects !== editingPlan.allowedProjectsCount ||
      formValues.warningText !== editingPlan.warningText ||
      formValues.upgradeText !== editingPlan.upgradeText;

    const currentLogsPerSecond = limitUnlimited.logsPerSecondUnlimited
      ? 0
      : formValues.logsPerSecondLimit || 0;
    const currentMaxLogs = limitUnlimited.maxLogsAmountUnlimited
      ? 0
      : formValues.maxLogsAmount || 0;
    const currentMaxSize = limitUnlimited.maxLogsSizeMbUnlimited
      ? 0
      : formValues.maxLogsSizeMb || 0;
    const currentMaxLife = limitUnlimited.maxLogsLifeDaysUnlimited
      ? 0
      : formValues.maxLogsLifeDays || 0;
    const currentMaxLogSize = limitUnlimited.maxLogSizeKbUnlimited
      ? 0
      : formValues.maxLogSizeKb || 0;

    const hasLimitChanges =
      currentLogsPerSecond !== editingPlan.logsPerSecondLimit ||
      currentMaxLogs !== editingPlan.maxLogsAmount ||
      currentMaxSize !== editingPlan.maxLogsSizeMb ||
      currentMaxLife !== editingPlan.maxLogsLifeDays ||
      currentMaxLogSize !== editingPlan.maxLogSizeKb;

    setIsUnsaved(hasBasicChanges || hasLimitChanges);
  };

  useEffect(() => {
    checkForChanges();
  }, [formValues, limitUnlimited, editingPlan]);

  const updateFormValue = <K extends keyof FormValues>(key: K, value: FormValues[K]) => {
    setFormValues((prev) => ({ ...prev, [key]: value }));
  };

  const handleCreate = () => {
    setEditingPlan(null);
    setFormValues(initialFormValues);
    setLimitUnlimited({
      allowedProjectsUnlimited: false,
      logsPerSecondUnlimited: false,
      maxLogsAmountUnlimited: false,
      maxLogsSizeMbUnlimited: false,
      maxLogsLifeDaysUnlimited: false,
      maxLogSizeKbUnlimited: false,
    });
    setIsUnsaved(false);
    setIsModalVisible(true);
  };

  const handleEdit = (plan: UserPlan) => {
    setEditingPlan(plan);
    setFormValues({
      name: plan.name,
      type: plan.type,
      isPublic: plan.isPublic,
      allowedProjectsCount: plan.allowedProjectsCount,
      warningText: plan.warningText,
      upgradeText: plan.upgradeText,
      logsPerSecondLimit: plan.logsPerSecondLimit || null,
      maxLogsAmount: plan.maxLogsAmount || null,
      maxLogsSizeMb: plan.maxLogsSizeMb || null,
      maxLogsLifeDays: plan.maxLogsLifeDays || null,
      maxLogSizeKb: plan.maxLogSizeKb || null,
    });

    setLimitUnlimited({
      allowedProjectsUnlimited: plan.allowedProjectsCount === 0,
      logsPerSecondUnlimited: plan.logsPerSecondLimit === 0,
      maxLogsAmountUnlimited: plan.maxLogsAmount === 0,
      maxLogsSizeMbUnlimited: plan.maxLogsSizeMb === 0,
      maxLogsLifeDaysUnlimited: plan.maxLogsLifeDays === 0,
      maxLogSizeKbUnlimited: plan.maxLogSizeKb === 0,
    });

    setIsUnsaved(false);
    setIsModalVisible(true);
  };

  const handleDelete = (planId: string) => {
    setDeletingPlanId(planId);
    setIsDeleteModalVisible(true);
  };

  const confirmDelete = async () => {
    if (!deletingPlanId) return;

    setIsSaving(true);
    try {
      await userPlanApi.deletePlan(deletingPlanId);
      message.success('Plan deleted successfully');
      setIsDeleteModalVisible(false);
      setDeletingPlanId(null);
      await loadPlans();
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to delete plan';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const handleModalOk = async () => {
    if (!formValues.name.trim()) {
      message.error('Name is required');
      return;
    }

    setIsSaving(true);
    try {
      const requestData: CreatePlanRequest | UpdatePlanRequest = {
        name: formValues.name,
        type: formValues.type,
        isPublic: formValues.isPublic,
        allowedProjectsCount: limitUnlimited.allowedProjectsUnlimited
          ? 0
          : formValues.allowedProjectsCount || 0,
        warningText: formValues.warningText || '',
        upgradeText: formValues.upgradeText || '',
        logsPerSecondLimit: limitUnlimited.logsPerSecondUnlimited
          ? 0
          : formValues.logsPerSecondLimit || 0,
        maxLogsAmount: limitUnlimited.maxLogsAmountUnlimited ? 0 : formValues.maxLogsAmount || 0,
        maxLogsSizeMb: limitUnlimited.maxLogsSizeMbUnlimited ? 0 : formValues.maxLogsSizeMb || 0,
        maxLogsLifeDays: limitUnlimited.maxLogsLifeDaysUnlimited
          ? 0
          : formValues.maxLogsLifeDays || 0,
        maxLogSizeKb: limitUnlimited.maxLogSizeKbUnlimited ? 0 : formValues.maxLogSizeKb || 0,
      };

      if (editingPlan) {
        await userPlanApi.updatePlan(editingPlan.id, requestData as UpdatePlanRequest);
        message.success('Plan updated successfully');
      } else {
        await userPlanApi.createPlan(requestData as CreatePlanRequest);
        message.success('Plan created successfully');
      }

      setIsModalVisible(false);
      setFormValues(initialFormValues);
      await loadPlans();
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
    setIsUnsaved(false);
  };

  const handleUnlimitedChange = (field: keyof LimitFieldValues, checked: boolean) => {
    setLimitUnlimited({ ...limitUnlimited, [field]: checked });
    if (checked) {
      const fieldMap: Record<keyof LimitFieldValues, keyof FormValues> = {
        allowedProjectsUnlimited: 'allowedProjectsCount',
        logsPerSecondUnlimited: 'logsPerSecondLimit',
        maxLogsAmountUnlimited: 'maxLogsAmount',
        maxLogsSizeMbUnlimited: 'maxLogsSizeMb',
        maxLogsLifeDaysUnlimited: 'maxLogsLifeDays',
        maxLogSizeKbUnlimited: 'maxLogSizeKb',
      };
      updateFormValue(fieldMap[field], null);
    }
  };

  const columns: ColumnsType<UserPlan> = [
    {
      title: 'Name',
      dataIndex: 'name',
      key: 'name',
    },
    {
      title: 'Type',
      dataIndex: 'type',
      key: 'type',
      render: (type: UserPlanType) => (
        <Tag color={type === UserPlanType.EXTENDED ? 'blue' : 'default'}>{type}</Tag>
      ),
    },
    {
      title: 'Public',
      dataIndex: 'isPublic',
      key: 'isPublic',
      render: (isPublic: boolean) => (
        <Tag color={isPublic ? 'green' : 'default'}>{isPublic ? 'Yes' : 'No'}</Tag>
      ),
    },
    {
      title: 'Allowed projects',
      dataIndex: 'allowedProjectsCount',
      key: 'allowedProjectsCount',
    },
    {
      title: 'Actions',
      key: 'actions',
      render: (_: unknown, record: UserPlan) => (
        <div className="flex space-x-2">
          <Button size="small" onClick={() => handleEdit(record)}>
            Edit
          </Button>
          <Button size="small" danger onClick={() => handleDelete(record.id)}>
            Delete
          </Button>
        </div>
      ),
    },
  ];

  const deletingPlan = plans.find((p) => p.id === deletingPlanId);

  return (
    <div className="my-8 max-w-4xl">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-bold text-gray-900 dark:text-gray-100">Plans Management</h2>
        <Button
          type="primary"
          onClick={handleCreate}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          Create Plan
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center py-4">
          <Spin indicator={<LoadingOutlined spin />} />
          <span className="ml-2 text-sm text-gray-500 dark:text-gray-300">Loading plans...</span>
        </div>
      ) : (
        <Table dataSource={plans} columns={columns} rowKey="id" pagination={false} size="small" />
      )}

      <Modal
        title={editingPlan ? 'Edit Plan' : 'Create Plan'}
        open={isModalVisible}
        onOk={handleModalOk}
        onCancel={handleModalCancel}
        confirmLoading={isSaving}
        width={650}
        okText={editingPlan ? (isUnsaved ? 'Update' : 'No Changes') : 'Create'}
        okButtonProps={{
          className:
            'border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700',
          disabled: editingPlan ? !isUnsaved : false,
        }}
      >
        <div className="mt-4 space-y-3">
          <div>
            <label className="mb-1 block text-sm font-medium">
              Name <span className="text-red-500">*</span>
            </label>
            <Input
              placeholder="Plan name"
              value={formValues.name}
              onChange={(e) => updateFormValue('name', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              Type <span className="text-red-500">*</span>
            </label>
            <Select
              placeholder="Type"
              value={formValues.type}
              onChange={(value) => updateFormValue('type', value)}
              className="w-full"
            >
              <Select.Option value={UserPlanType.DEFAULT}>DEFAULT</Select.Option>
              <Select.Option value={UserPlanType.EXTENDED}>EXTENDED</Select.Option>
            </Select>
          </div>

          <div>
            <Checkbox
              checked={formValues.isPublic}
              onChange={(e) => updateFormValue('isPublic', e.target.checked)}
            >
              Is public
            </Checkbox>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Allowed projects</label>
            <div className="flex items-center space-x-2">
              <InputNumber
                min={0}
                placeholder="0"
                value={formValues.allowedProjectsCount}
                onChange={(value) => updateFormValue('allowedProjectsCount', value)}
                disabled={limitUnlimited.allowedProjectsUnlimited}
                className="flex-1"
              />

              <div className="flex-1 pl-3">
                <Checkbox
                  checked={limitUnlimited.allowedProjectsUnlimited}
                  onChange={(e) =>
                    handleUnlimitedChange('allowedProjectsUnlimited', e.target.checked)
                  }
                >
                  Unlimited
                </Checkbox>
              </div>
            </div>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Warning Text</label>
            <TextArea
              rows={2}
              placeholder="Optional"
              value={formValues.warningText}
              onChange={(e) => updateFormValue('warningText', e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">Upgrade Text</label>
            <TextArea
              rows={2}
              placeholder="Optional"
              value={formValues.upgradeText}
              onChange={(e) => updateFormValue('upgradeText', e.target.value)}
            />
          </div>

          <div className="rounded border border-gray-200 p-3 dark:border-gray-700">
            <h3 className="mb-2 text-sm font-semibold">Limits</h3>

            <div className="space-y-2 text-sm">
              <div className="flex items-center justify-between">
                <span className="w-40 text-gray-600 dark:text-gray-300">Logs/Second:</span>

                <div className="flex flex-1 items-center space-x-2">
                  <InputNumber
                    size="small"
                    min={0}
                    className="flex-1"
                    disabled={limitUnlimited.logsPerSecondUnlimited}
                    placeholder="0"
                    value={formValues.logsPerSecondLimit}
                    onChange={(value) => updateFormValue('logsPerSecondLimit', value)}
                  />

                  <div className="flex flex-1 items-center space-x-2 pl-3">
                    <Checkbox
                      checked={limitUnlimited.logsPerSecondUnlimited}
                      onChange={(e) =>
                        handleUnlimitedChange('logsPerSecondUnlimited', e.target.checked)
                      }
                    >
                      Unlimited
                    </Checkbox>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between">
                <span className="w-40 text-gray-600 dark:text-gray-300">Max Logs:</span>

                <div className="flex flex-1 items-center space-x-2">
                  <InputNumber
                    size="small"
                    min={0}
                    className="flex-1"
                    disabled={limitUnlimited.maxLogsAmountUnlimited}
                    placeholder="0"
                    value={formValues.maxLogsAmount}
                    onChange={(value) => updateFormValue('maxLogsAmount', value)}
                  />

                  <div className="flex flex-1 items-center space-x-2 pl-3">
                    <Checkbox
                      checked={limitUnlimited.maxLogsAmountUnlimited}
                      onChange={(e) =>
                        handleUnlimitedChange('maxLogsAmountUnlimited', e.target.checked)
                      }
                    >
                      Unlimited
                    </Checkbox>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between">
                <span className="w-40 text-gray-600 dark:text-gray-300">Max Size (MB):</span>

                <div className="flex flex-1 items-center space-x-2">
                  <InputNumber
                    size="small"
                    min={0}
                    className="flex-1"
                    disabled={limitUnlimited.maxLogsSizeMbUnlimited}
                    placeholder="0"
                    value={formValues.maxLogsSizeMb}
                    onChange={(value) => updateFormValue('maxLogsSizeMb', value)}
                  />

                  <div className="flex flex-1 items-center space-x-2 pl-3">
                    <Checkbox
                      checked={limitUnlimited.maxLogsSizeMbUnlimited}
                      onChange={(e) =>
                        handleUnlimitedChange('maxLogsSizeMbUnlimited', e.target.checked)
                      }
                    >
                      Unlimited
                    </Checkbox>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between">
                <span className="w-40 text-gray-600 dark:text-gray-300">Retention (Days):</span>

                <div className="flex flex-1 items-center space-x-2">
                  <InputNumber
                    size="small"
                    min={0}
                    className="flex-1"
                    disabled={limitUnlimited.maxLogsLifeDaysUnlimited}
                    placeholder="0"
                    value={formValues.maxLogsLifeDays}
                    onChange={(value) => updateFormValue('maxLogsLifeDays', value)}
                  />

                  <div className="flex flex-1 items-center space-x-2 pl-3">
                    <Checkbox
                      checked={limitUnlimited.maxLogsLifeDaysUnlimited}
                      onChange={(e) =>
                        handleUnlimitedChange('maxLogsLifeDaysUnlimited', e.target.checked)
                      }
                    >
                      Unlimited
                    </Checkbox>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between">
                <span className="w-40 text-gray-600 dark:text-gray-300">Max Log Size (KB):</span>

                <div className="flex flex-1 items-center space-x-2">
                  <InputNumber
                    size="small"
                    min={0}
                    className="flex-1"
                    disabled={limitUnlimited.maxLogSizeKbUnlimited}
                    placeholder="0"
                    value={formValues.maxLogSizeKb}
                    onChange={(value) => updateFormValue('maxLogSizeKb', value)}
                  />

                  <div className="flex flex-1 items-center space-x-2 pl-3">
                    <Checkbox
                      checked={limitUnlimited.maxLogSizeKbUnlimited}
                      onChange={(e) =>
                        handleUnlimitedChange('maxLogSizeKbUnlimited', e.target.checked)
                      }
                    >
                      Unlimited
                    </Checkbox>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </Modal>

      <Modal
        title="Delete Plan"
        open={isDeleteModalVisible}
        onOk={confirmDelete}
        onCancel={() => {
          setIsDeleteModalVisible(false);
          setDeletingPlanId(null);
        }}
        confirmLoading={isSaving}
        okText="Delete"
        okButtonProps={{ danger: true }}
      >
        <p>
          Are you sure you want to delete the plan <strong>{deletingPlan?.name}</strong>? This
          action cannot be undone.
        </p>
      </Modal>
    </div>
  );
};
