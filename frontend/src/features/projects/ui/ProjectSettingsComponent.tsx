import { CopyOutlined, LoadingOutlined } from '@ant-design/icons';
import {
  App,
  Button,
  Checkbox,
  Input,
  InputNumber,
  Select,
  Space,
  Spin,
  Switch,
  Tooltip,
} from 'antd';
import dayjs from 'dayjs';
import { useEffect, useRef, useState } from 'react';

import { WEBSITE_URL } from '../../../constants';
import { metricApi } from '../../../entity/metrics/api/metricApi';
import type { MetricsStats } from '../../../entity/metrics/model/MetricsStats';
import { projectApi } from '../../../entity/projects/api/projectApi';
import type { Project } from '../../../entity/projects/model/Project';
import type { ProjectResponse } from '../../../entity/projects/model/ProjectResponse';
import { queryApi } from '../../../entity/query/api/queryApi';
import type { LogsStats } from '../../../entity/query/model/ProjectLogStats';
import { traceApi } from '../../../entity/traces/api/traceApi';
import type { SpansStats } from '../../../entity/traces/model/SpansStats';
import { ProjectRole } from '../../../entity/users/model/ProjectRole';
import type { UserProfile } from '../../../entity/users/model/UserProfile';
import { UserRole } from '../../../entity/users/model/UserRole';
import { copyToClipboard } from '../../../shared/lib';
import { ProjectAuditLogsComponent } from './ProjectAuditLogsComponent';
import { PromotedFieldsComponent } from './PromotedFieldsComponent';
import { RetentionPoliciesComponent } from './RetentionPoliciesComponent';

interface Props {
  projectResponse: ProjectResponse;
  user: UserProfile;
  contentHeight: number;
}

// Quota fields where 0 means unlimited; retention has no unlimited concept.
type QuotaFieldName = 'logsPerSecondLimit' | 'maxLogSizeKb' | 'maxLogsAmount' | 'maxLogsSizeMb';

// The organization plan's caps on a project, for display.
const orgPlanCaps = (project?: Project): string[] => {
  const plan = project?.orgPlan;
  if (!plan) return [];
  const caps: [number, string][] = [
    [plan.logsPerSecondLimit, 'logs/s'],
    [plan.maxLogSizeKb, 'KB per log'],
    [plan.maxLogsAmount, 'logs kept'],
    [plan.maxLogsSizeMb, 'MB stored'],
    [plan.maxLogsLifeDays, 'days retention'],
    [plan.maxProjects, 'projects on this Site'],
  ];
  return caps
    .filter(([value]) => value > 0)
    .map(([value, unit]) => `${value.toLocaleString()} ${unit}`);
};

export function ProjectSettingsComponent({ projectResponse, user, contentHeight }: Props) {
  const { message, modal } = App.useApp();
  const [project, setProject] = useState<Project | undefined>(undefined);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);

  // Loaded per signal so one slow or failing endpoint doesn't block the others.
  const [projectStats, setProjectStats] = useState<LogsStats | undefined>(undefined);
  const [isLoadingStats, setIsLoadingStats] = useState(false);
  const [spanStats, setSpanStats] = useState<SpansStats | undefined>(undefined);
  const [isLoadingSpanStats, setIsLoadingSpanStats] = useState(false);
  const [metricStats, setMetricStats] = useState<MetricsStats | undefined>(undefined);
  const [isLoadingMetricStats, setIsLoadingMetricStats] = useState(false);

  // Last non-zero value per quota field, so unchecking "Unlimited" restores it.
  const lastQuotaValueRef = useRef<Partial<Record<QuotaFieldName, number>>>({});

  const [formProject, setFormProject] = useState<Partial<Project>>({});
  const [nameError, setNameError] = useState(false);
  const [domainErrors, setDomainErrors] = useState<string[]>([]);
  const [ipErrors, setIpErrors] = useState<string[]>([]);

  const [basicInfoChanges, setBasicInfoChanges] = useState(false);
  const [securityPolicyChanges, setSecurityPolicyChanges] = useState(false);
  const [quotasChanges, setQuotasChanges] = useState(false);

  const canEdit =
    user.role === UserRole.ADMIN ||
    projectResponse.userRole === ProjectRole.OWNER ||
    projectResponse.userRole === ProjectRole.ADMIN;

  useEffect(() => {
    loadProject();
    loadProjectStats();
    loadSpanStats();
    loadMetricStats();
  }, [projectResponse.id]);

  const checkBasicInfoChanges = (newFormProject: Partial<Project>): boolean => {
    if (!project) return false;
    return newFormProject.name !== project.name;
  };

  const checkSecurityPolicyChanges = (newFormProject: Partial<Project>): boolean => {
    if (!project) return false;
    return (
      newFormProject.isApiKeyRequired !== project.isApiKeyRequired ||
      newFormProject.isFilterByDomain !== project.isFilterByDomain ||
      newFormProject.isFilterByIp !== project.isFilterByIp ||
      JSON.stringify(newFormProject.allowedDomains) !== JSON.stringify(project.allowedDomains) ||
      JSON.stringify(newFormProject.allowedIps) !== JSON.stringify(project.allowedIps)
    );
  };

  const checkQuotasChanges = (newFormProject: Partial<Project>): boolean => {
    if (!project) return false;
    return (
      newFormProject.isRateLimitingEnabled !== project.isRateLimitingEnabled ||
      newFormProject.logsPerSecondLimit !== project.logsPerSecondLimit ||
      newFormProject.maxLogsAmount !== project.maxLogsAmount ||
      newFormProject.maxLogsSizeMb !== project.maxLogsSizeMb ||
      newFormProject.maxLogsLifeDays !== project.maxLogsLifeDays ||
      newFormProject.maxLogSizeKb !== project.maxLogSizeKb
    );
  };

  const validateDomain = (domain: string): boolean => {
    const domainRegex =
      /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/;
    return domainRegex.test(domain.trim());
  };

  const validateIP = (ip: string): boolean => {
    const ipv4Regex = /^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}$/;
    // Simplified IPv6 check.
    const ipv6Regex =
      /^(([0-9a-fA-F]{1,4}:){7,7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;

    return ipv4Regex.test(ip.trim()) || ipv6Regex.test(ip.trim());
  };

  const validateDomains = (domains: string[]): string[] => {
    const errors: string[] = [];
    domains.forEach((domain, index) => {
      if (!domain.trim()) {
        errors[index] = 'Domain cannot be empty';
      } else if (!validateDomain(domain)) {
        errors[index] = 'Invalid domain format';
      }
    });
    return errors;
  };

  const validateIPs = (ips: string[]): string[] => {
    const errors: string[] = [];
    ips.forEach((ip, index) => {
      if (!ip.trim()) {
        errors[index] = 'IP address cannot be empty';
      } else if (!validateIP(ip)) {
        errors[index] = 'Invalid IP address format';
      }
    });
    return errors;
  };

  const loadProject = async () => {
    setIsLoading(true);

    try {
      const projectData = await projectApi.getProject(projectResponse.id);
      setProject(projectData);
      setFormProject(projectData);
      setNameError(false);
      setDomainErrors([]);
      setIpErrors([]);

      setBasicInfoChanges(false);
      setSecurityPolicyChanges(false);
      setQuotasChanges(false);
    } catch (error: unknown) {
      const errorMessage = error instanceof Error ? error.message : 'Failed to load project';
      message.error(errorMessage);
    } finally {
      setIsLoading(false);
    }
  };

  const loadProjectStats = async () => {
    setIsLoadingStats(true);

    try {
      const stats = await queryApi.getProjectStats(projectResponse.id);
      setProjectStats(stats);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to load project statistics';
      message.error(errorMessage);
    } finally {
      setIsLoadingStats(false);
    }
  };

  const loadSpanStats = async () => {
    setIsLoadingSpanStats(true);

    try {
      const stats = await traceApi.getProjectStats(projectResponse.id);
      setSpanStats(stats);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to load trace statistics';
      message.error(errorMessage);
    } finally {
      setIsLoadingSpanStats(false);
    }
  };

  const loadMetricStats = async () => {
    setIsLoadingMetricStats(true);

    try {
      const stats = await metricApi.getProjectStats(projectResponse.id);
      setMetricStats(stats);
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to load metric statistics';
      message.error(errorMessage);
    } finally {
      setIsLoadingMetricStats(false);
    }
  };

  const handleFieldChange = <K extends keyof Project>(field: K, value: Project[K]) => {
    const newFormProject = { ...formProject, [field]: value };
    setFormProject(newFormProject);

    if (field === 'allowedDomains' && Array.isArray(value)) {
      const errors = validateDomains(value as string[]);
      setDomainErrors(errors);
    }
    if (field === 'allowedIps' && Array.isArray(value)) {
      const errors = validateIPs(value as string[]);
      setIpErrors(errors);
    }

    if (project) {
      setBasicInfoChanges(checkBasicInfoChanges(newFormProject));
      setSecurityPolicyChanges(checkSecurityPolicyChanges(newFormProject));
      setQuotasChanges(checkQuotasChanges(newFormProject));
    }
  };

  // "Unlimited" and the number input are two views of one value (0 = unlimited);
  // unchecking restores the last non-zero value typed, or defaultValue.
  const renderQuotaField = (
    field: QuotaFieldName,
    label: string,
    description: string,
    min: number,
    max: number,
    defaultValue: number,
  ) => {
    const value = formProject[field];
    const isUnlimited = value === 0;
    const planFixed = !!(project?.plan && project.plan[field] !== 0);
    const fieldDisabled = !canEdit || !formProject.isRateLimitingEnabled || planFixed;

    return (
      <div>
        <div className="mb-1 flex items-center justify-between">
          <div className="font-medium text-gray-900 dark:text-gray-100">{label}</div>
          <label className="flex cursor-pointer items-center gap-1.5 text-xs text-gray-500 dark:text-gray-300">
            <Checkbox
              checked={isUnlimited}
              disabled={fieldDisabled}
              onChange={(e) => {
                if (e.target.checked) {
                  if (value) lastQuotaValueRef.current[field] = value;
                  handleFieldChange(field, 0);
                } else {
                  handleFieldChange(field, lastQuotaValueRef.current[field] ?? defaultValue);
                }
              }}
            />
            Unlimited
          </label>
        </div>
        <InputNumber
          value={value}
          onChange={(v) => handleFieldChange(field, v || 0)}
          disabled={fieldDisabled || isUnlimited}
          formatter={(v) => `${v}`.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}
          parser={(v) => v?.replace(/\$\s?|(,*)/g, '') as unknown as number}
          min={min}
          max={max}
          style={{ width: '150px' }}
        />
        <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
          {isUnlimited ? 'No limit' : description}
        </div>
      </div>
    );
  };

  // totalValue undefined means loading or failed (0 is a real "no data"). The
  // date range needs at least one row; an empty project's timestamps are placeholders.
  const renderStatsCard = (
    title: string,
    isLoadingCard: boolean,
    totalLabel: string,
    totalValue: number | undefined,
    sizeMb: number | undefined,
    oldestTime: string | undefined,
    newestTime: string | undefined,
  ) => (
    <div className="rounded border border-gray-200 p-4 dark:border-gray-700">
      <div className="mb-2 font-medium text-gray-900 dark:text-gray-100">{title}</div>
      {isLoadingCard ? (
        <div className="flex items-center py-2">
          <Spin indicator={<LoadingOutlined spin />} />
          <span className="ml-2 text-sm text-gray-500 dark:text-gray-300">Loading...</span>
        </div>
      ) : totalValue !== undefined ? (
        <div className="space-y-1.5 text-sm">
          <div className="flex justify-between">
            <span className="text-gray-600 dark:text-gray-300">{totalLabel}:</span>
            <span className="font-medium">{totalValue.toLocaleString()}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-gray-600 dark:text-gray-300">Storage size:</span>
            <span className="font-medium">{(sizeMb ?? 0).toFixed(2)} MB</span>
          </div>
          {totalValue > 0 && oldestTime && newestTime && (
            <div className="flex justify-between">
              <span className="text-gray-600 dark:text-gray-300">Date range:</span>
              <span className="font-medium">
                {dayjs(oldestTime).format('D MMM YYYY')} - {dayjs(newestTime).format('D MMM YYYY')}
              </span>
            </div>
          )}
        </div>
      ) : (
        <div className="text-sm text-gray-500 dark:text-gray-300">No statistics available</div>
      )}
    </div>
  );

  const saveBasicInfo = async () => {
    if (!basicInfoChanges || !project || !canEdit) return;

    if (!formProject.name?.trim()) {
      setNameError(true);
      message.error('Project name is required');
      return;
    }
    setNameError(false);

    setIsSaving(true);
    try {
      const updateData = {
        ...project,
        name: formProject.name,
      };
      const updatedProject = await projectApi.updateProject(project.id, updateData);
      setProject(updatedProject);
      setFormProject(updatedProject);

      setBasicInfoChanges(false);

      setNameError(false);
      message.success('Basic information updated successfully');
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to update basic information';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const saveSecurityPolicies = async () => {
    if (!securityPolicyChanges || !project || !canEdit) return;

    if (formProject.isFilterByDomain && formProject.allowedDomains) {
      const domainValidationErrors = validateDomains(formProject.allowedDomains);
      if (domainValidationErrors.some((error) => error)) {
        setDomainErrors(domainValidationErrors);
        message.error('Please fix domain validation errors before saving');
        return;
      }
    }

    if (formProject.isFilterByIp && formProject.allowedIps) {
      const ipValidationErrors = validateIPs(formProject.allowedIps);
      if (ipValidationErrors.some((error) => error)) {
        setIpErrors(ipValidationErrors);
        message.error('Please fix IP address validation errors before saving');
        return;
      }
    }

    setIsSaving(true);
    try {
      const updateData = {
        ...project,
        isApiKeyRequired: formProject.isApiKeyRequired ?? project.isApiKeyRequired,
        isFilterByDomain: formProject.isFilterByDomain ?? project.isFilterByDomain,
        isFilterByIp: formProject.isFilterByIp ?? project.isFilterByIp,
        allowedDomains: formProject.allowedDomains ?? project.allowedDomains,
        allowedIps: formProject.allowedIps ?? project.allowedIps,
      };
      const updatedProject = await projectApi.updateProject(project.id, updateData);
      setProject(updatedProject);
      setFormProject(updatedProject);

      setSecurityPolicyChanges(false);

      message.success('Security policies updated successfully');
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to update security policies';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const saveQuotas = async () => {
    if (!quotasChanges || !project || !canEdit) return;

    setIsSaving(true);
    try {
      const updateData = {
        ...project,
        isRateLimitingEnabled: formProject.isRateLimitingEnabled ?? project.isRateLimitingEnabled,
        logsPerSecondLimit: formProject.logsPerSecondLimit ?? project.logsPerSecondLimit,
        maxLogsAmount: formProject.maxLogsAmount ?? project.maxLogsAmount,
        maxLogsSizeMb: formProject.maxLogsSizeMb ?? project.maxLogsSizeMb,
        maxLogsLifeDays: formProject.maxLogsLifeDays ?? project.maxLogsLifeDays,
        maxLogSizeKb: formProject.maxLogSizeKb ?? project.maxLogSizeKb,
      };
      const updatedProject = await projectApi.updateProject(project.id, updateData);
      setProject(updatedProject);
      setFormProject(updatedProject);

      setQuotasChanges(false);

      message.success('Rate limiting & quotas updated successfully');
    } catch (error: unknown) {
      const errorMessage =
        error instanceof Error ? error.message : 'Failed to update rate limiting & quotas';
      message.error(errorMessage);
    } finally {
      setIsSaving(false);
    }
  };

  const handleDeleteProject = async () => {
    if (!project) {
      message.error('Project not found');
      return;
    }

    if (!canEdit) {
      message.error('You do not have permission to delete this project');
      return;
    }

    modal.confirm({
      title: 'Delete Project',
      content: (
        <div>
          <p>
            Are you sure you want to delete the project <strong>{project.name}</strong>?
          </p>
          <p className="mt-2 text-red-600">
            <strong>This action cannot be undone.</strong> All logs and associated data will be
            permanently removed.
          </p>
        </div>
      ),
      okText: 'Delete Project',
      okType: 'danger',
      cancelText: 'Cancel',
      onOk: async () => {
        setIsDeleting(true);
        try {
          await projectApi.deleteProject(project.id);
          message.success('Project deleted successfully');
          window.location.href = '/';
        } catch (error: unknown) {
          const errorMessage = error instanceof Error ? error.message : 'Failed to delete project';
          message.error(errorMessage);
        } finally {
          setIsDeleting(false);
        }
      },
    });
  };

  return (
    <div className="flex grow pl-3">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <h1 className="mb-6 text-2xl font-bold">Settings</h1>

          {isLoading || !project ? (
            <Spin indicator={<LoadingOutlined spin />} size="large" />
          ) : (
            <>
              {!canEdit && (
                <div className="my-4 rounded-md bg-yellow-50 p-3">
                  <div className="text-sm text-yellow-800">
                    You don&apos;t have permission to modify these settings. Only project owners,
                    project admins and system administrators can change project settings.
                  </div>
                </div>
              )}

              <div className="space-y-6 text-sm">
                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <div className="max-w-md">
                    <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
                      Project name
                    </div>
                    <Input
                      value={formProject.name || ''}
                      onChange={(e) => {
                        setNameError(false);
                        handleFieldChange('name', e.target.value);
                      }}
                      disabled={!canEdit}
                      placeholder="Enter project name"
                      maxLength={100}
                      status={nameError ? 'error' : undefined}
                    />
                  </div>

                  {/* Read-only for everyone: needed to grant access to this
                      project from a control plane, and in API calls. */}
                  <div className="mt-4 max-w-md">
                    <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
                      Project ID
                    </div>
                    <Space.Compact className="w-full">
                      <Input value={project.id} readOnly className="!font-mono" />
                      <Tooltip title="Copy project ID">
                        <Button
                          icon={<CopyOutlined />}
                          aria-label="Copy project ID"
                          onClick={async () => {
                            await copyToClipboard(project.id);
                            message.success('Project ID copied');
                          }}
                        />
                      </Tooltip>
                    </Space.Compact>
                    <div className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      Used when granting access to this project from a control plane, and in API
                      calls.
                    </div>
                  </div>

                  {basicInfoChanges && canEdit && (
                    <div className="mt-4 flex space-x-2">
                      <Button
                        type="primary"
                        onClick={saveBasicInfo}
                        loading={isSaving}
                        disabled={isSaving}
                        className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                      >
                        {isSaving ? 'Saving...' : 'Save Changes'}
                      </Button>

                      <Button
                        type="default"
                        onClick={() => {
                          if (project) {
                            const updatedForm = { ...formProject, name: project.name };
                            setFormProject(updatedForm);
                            setBasicInfoChanges(false);
                            setNameError(false);
                          }
                        }}
                        disabled={isSaving}
                      >
                        Reset
                      </Button>
                    </div>
                  )}
                </div>

                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <h2 className="mb-4 text-xl font-bold text-gray-900 dark:text-gray-100">
                    Security policies
                  </h2>

                  <div className="space-y-4">
                    <div className="flex items-start justify-between">
                      <div className="flex-1 pr-20">
                        <div className="font-medium text-gray-900 dark:text-gray-100">
                          Require API key
                        </div>
                        <div className="mt-1 text-gray-500 dark:text-gray-300">
                          When enabled, all log ingestion requests must include a valid API key
                        </div>
                      </div>
                      <div className="ml-4">
                        <Switch
                          checked={formProject.isApiKeyRequired ?? false}
                          onChange={(checked) => handleFieldChange('isApiKeyRequired', checked)}
                          disabled={!canEdit}
                          style={{
                            backgroundColor: formProject.isApiKeyRequired ? '#059669' : undefined,
                          }}
                        />
                      </div>
                    </div>

                    <div className="space-y-3">
                      <div className="flex items-start justify-between">
                        <div className="flex-1 pr-20">
                          <div className="font-medium text-gray-900 dark:text-gray-100">
                            Filter by domain
                          </div>
                          <div className="mt-1 text-gray-500 dark:text-gray-300">
                            When enabled, only requests from allowed domains will be accepted
                          </div>
                        </div>
                        <div className="ml-4">
                          <Switch
                            checked={formProject.isFilterByDomain ?? false}
                            onChange={(checked) => {
                              const newFormProject = {
                                ...formProject,
                                isFilterByDomain: checked,
                                ...(checked ? {} : { allowedDomains: [] }),
                              };
                              setFormProject(newFormProject);

                              if (!checked) {
                                setDomainErrors([]);
                              }

                              if (project) {
                                setBasicInfoChanges(checkBasicInfoChanges(newFormProject));
                                setSecurityPolicyChanges(
                                  checkSecurityPolicyChanges(newFormProject),
                                );
                                setQuotasChanges(checkQuotasChanges(newFormProject));
                              }
                            }}
                            disabled={!canEdit}
                            style={{
                              backgroundColor: formProject.isFilterByDomain ? '#059669' : undefined,
                            }}
                          />
                        </div>
                      </div>

                      {formProject.isFilterByDomain && (
                        <div className="ml-0">
                          <div className="mb-2 text-sm font-medium text-gray-700 dark:text-gray-200">
                            Allowed domains
                          </div>
                          <Select
                            mode="tags"
                            value={formProject.allowedDomains || []}
                            onChange={(value) => handleFieldChange('allowedDomains', value)}
                            disabled={!canEdit}
                            placeholder="Enter domains (e.g., example.com, subdomain.example.com)"
                            className="w-full"
                            style={
                              {
                                '--ant-color-primary': '#059669',
                                '--ant-color-primary-hover': '#047857',
                              } as React.CSSProperties
                            }
                            status={domainErrors.some((error) => error) ? 'error' : undefined}
                            tokenSeparators={[',', ' ']}
                          />
                          {domainErrors.length > 0 && domainErrors.some((error) => error) && (
                            <div className="mt-1 text-xs text-red-600">
                              {domainErrors.map((error, index) =>
                                error ? (
                                  <div key={index}>
                                    Domain {index + 1}: {error}
                                  </div>
                                ) : null,
                              )}
                            </div>
                          )}
                          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                            Press Enter or comma to add multiple domains. Only requests from these
                            domains will be accepted.
                          </div>
                        </div>
                      )}
                    </div>

                    <div className="space-y-3">
                      <div className="flex items-start justify-between">
                        <div className="flex-1 pr-20">
                          <div className="font-medium text-gray-900 dark:text-gray-100">
                            Filter by IP address
                          </div>
                          <div className="mt-1 text-gray-500 dark:text-gray-300">
                            When enabled, only requests from allowed IP addresses will be accepted
                          </div>
                        </div>
                        <div className="ml-4">
                          <Switch
                            checked={formProject.isFilterByIp ?? false}
                            onChange={(checked) => {
                              const newFormProject = {
                                ...formProject,
                                isFilterByIp: checked,
                                ...(checked ? {} : { allowedIps: [] }),
                              };
                              setFormProject(newFormProject);

                              if (!checked) {
                                setIpErrors([]);
                              }

                              if (project) {
                                setBasicInfoChanges(checkBasicInfoChanges(newFormProject));
                                setSecurityPolicyChanges(
                                  checkSecurityPolicyChanges(newFormProject),
                                );
                                setQuotasChanges(checkQuotasChanges(newFormProject));
                              }
                            }}
                            disabled={!canEdit}
                            style={{
                              backgroundColor: formProject.isFilterByIp ? '#059669' : undefined,
                            }}
                          />
                        </div>
                      </div>

                      {formProject.isFilterByIp && (
                        <div className="ml-0">
                          <div className="mb-2 text-sm font-medium text-gray-700 dark:text-gray-200">
                            Allowed IP addresses
                          </div>
                          <Select
                            mode="tags"
                            value={formProject.allowedIps || []}
                            onChange={(value) => handleFieldChange('allowedIps', value)}
                            disabled={!canEdit}
                            placeholder="Enter IP addresses (e.g., 192.168.1.1, 10.0.0.0/8)"
                            className="w-full"
                            style={
                              {
                                '--ant-color-primary': '#059669',
                                '--ant-color-primary-hover': '#047857',
                              } as React.CSSProperties
                            }
                            status={ipErrors.some((error) => error) ? 'error' : undefined}
                            tokenSeparators={[',', ' ']}
                          />
                          {ipErrors.length > 0 && ipErrors.some((error) => error) && (
                            <div className="mt-1 text-xs text-red-600">
                              {ipErrors.map((error, index) =>
                                error ? (
                                  <div key={index}>
                                    IP {index + 1}: {error}
                                  </div>
                                ) : null,
                              )}
                            </div>
                          )}
                          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                            Press Enter or comma to add multiple IP addresses. Supports both IPv4
                            and IPv6 formats.
                          </div>
                        </div>
                      )}
                    </div>
                  </div>

                  {securityPolicyChanges && canEdit && (
                    <div className="mt-4 flex space-x-2">
                      <Button
                        type="primary"
                        onClick={saveSecurityPolicies}
                        loading={isSaving}
                        disabled={isSaving}
                        className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                      >
                        {isSaving ? 'Saving...' : 'Save Changes'}
                      </Button>
                      <Button
                        type="default"
                        onClick={() => {
                          if (project) {
                            const updatedForm = {
                              ...formProject,
                              isApiKeyRequired: project.isApiKeyRequired,
                              isFilterByDomain: project.isFilterByDomain,
                              isFilterByIp: project.isFilterByIp,
                              allowedDomains: project.allowedDomains,
                              allowedIps: project.allowedIps,
                            };
                            setFormProject(updatedForm);
                            setSecurityPolicyChanges(false);
                            setDomainErrors([]);
                            setIpErrors([]);
                          }
                        }}
                        disabled={isSaving}
                      >
                        Reset
                      </Button>
                    </div>
                  )}
                </div>

                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <h2 className="text-xl leading-1.5 font-bold text-gray-900 dark:text-gray-100">
                    Rate limiting & quotas
                  </h2>

                  <div className="mt-3 text-sm text-gray-500 dark:text-gray-300">
                    Read more about settings you can{' '}
                    <a
                      href={`${WEBSITE_URL}/settings`}
                      target="_blank"
                      rel="noreferrer"
                      className="font-bold !text-emerald-600"
                    >
                      here
                    </a>
                  </div>

                  {project?.plan?.warningText && (
                    <div className="mt-1 text-orange-600 opacity-60">
                      {project.plan.warningText}
                    </div>
                  )}

                  {orgPlanCaps(project).length > 0 && (
                    <div className="mt-2 text-gray-500 dark:text-gray-300">
                      Your organization&apos;s plan caps this project, even with rate limiting off:{' '}
                      {orgPlanCaps(project).join(', ')}.
                    </div>
                  )}

                  {/* Doesn't touch log retention: a data-lifecycle setting, not a capacity guard. */}
                  <div className="mt-4 flex items-start justify-between">
                    <div className="flex-1 pr-20">
                      <div className="font-medium text-gray-900 dark:text-gray-100">
                        Rate limiting & quotas
                      </div>
                      <div className="mt-1 text-gray-500 dark:text-gray-300">
                        When disabled, logs are never rejected or deleted for rate/size/count/
                        storage reasons - use this if you&apos;d rather stream without any risk of
                        losing data. Log retention (below) is unaffected either way.
                      </div>
                    </div>
                    <div className="ml-4">
                      <Switch
                        checked={formProject.isRateLimitingEnabled ?? true}
                        onChange={(checked) => handleFieldChange('isRateLimitingEnabled', checked)}
                        disabled={!canEdit}
                        style={{
                          backgroundColor: formProject.isRateLimitingEnabled
                            ? '#059669'
                            : undefined,
                        }}
                      />
                    </div>
                  </div>

                  <div
                    className={`mt-4 grid grid-cols-1 gap-4 md:grid-cols-2 ${
                      formProject.isRateLimitingEnabled ? '' : 'opacity-50'
                    }`}
                  >
                    {renderQuotaField(
                      'logsPerSecondLimit',
                      'Logs per second limit',
                      'Maximum logs that can be ingested per second',
                      1,
                      100000,
                      1000,
                    )}
                    {renderQuotaField(
                      'maxLogSizeKb',
                      'Maximum log size (KB)',
                      'Maximum size allowed for a single log entry',
                      1,
                      1024,
                      64,
                    )}
                    {renderQuotaField(
                      'maxLogsAmount',
                      'Maximum logs amount',
                      'Maximum total number of logs that can be stored',
                      1,
                      1000000000000000,
                      100_000_000,
                    )}
                    {renderQuotaField(
                      'maxLogsSizeMb',
                      'Maximum storage size (MB)',
                      'Maximum total storage size for all logs',
                      1,
                      1000000000000000,
                      100_000,
                    )}
                  </div>

                  {/* Retention stays active regardless of the quotas switch. */}
                  <div className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
                    <div>
                      <div className="mb-1 font-medium text-gray-900 dark:text-gray-100">
                        Log retention (days)
                      </div>
                      <InputNumber
                        value={formProject.maxLogsLifeDays}
                        onChange={(value) => handleFieldChange('maxLogsLifeDays', value || 0)}
                        formatter={(value) => `${value}`.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}
                        parser={(value) => value?.replace(/\$\s?|(,*)/g, '') as unknown as number}
                        disabled={!canEdit || (project.plan && project.plan.maxLogsLifeDays != 0)}
                        min={1}
                        max={3650}
                        style={{ width: '150px' }}
                      />
                      <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                        How long logs should be kept before automatic deletion
                      </div>
                    </div>
                  </div>

                  {quotasChanges && canEdit && (
                    <div className="mt-4 flex space-x-2">
                      <Button
                        type="primary"
                        onClick={saveQuotas}
                        loading={isSaving}
                        disabled={isSaving}
                        className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
                      >
                        {isSaving ? 'Saving...' : 'Save Changes'}
                      </Button>
                      <Button
                        type="default"
                        onClick={() => {
                          if (project) {
                            const updatedForm = {
                              ...formProject,
                              isRateLimitingEnabled: project.isRateLimitingEnabled,
                              logsPerSecondLimit: project.logsPerSecondLimit,
                              maxLogsAmount: project.maxLogsAmount,
                              maxLogsSizeMb: project.maxLogsSizeMb,
                              maxLogsLifeDays: project.maxLogsLifeDays,
                              maxLogSizeKb: project.maxLogSizeKb,
                            };
                            setFormProject(updatedForm);
                            setQuotasChanges(false);
                          }
                        }}
                        disabled={isSaving}
                      >
                        Reset
                      </Button>
                    </div>
                  )}
                </div>

                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <h2 className="mb-4 text-xl font-bold text-gray-900 dark:text-gray-100">
                    Promoted log fields
                  </h2>
                  <PromotedFieldsComponent projectId={project.id} canEdit={canEdit} />
                </div>

                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <h2 className="mb-4 text-xl font-bold text-gray-900 dark:text-gray-100">
                    Retention &amp; sampling
                  </h2>
                  <RetentionPoliciesComponent
                    projectId={project.id}
                    canEdit={canEdit}
                    logRetentionDays={project.maxLogsLifeDays}
                  />
                </div>

                <div className="max-w-2xl border-b border-gray-200 pb-6 dark:border-gray-700">
                  <h2 className="mb-4 text-xl font-bold text-gray-900 dark:text-gray-100">
                    Danger Zone
                  </h2>

                  <div className="rounded-lg border border-red-200 bg-red-50 p-4">
                    <div className="flex items-start justify-between">
                      <div className="flex-1">
                        <div className="font-medium text-red-900">Delete this project</div>
                        <div className="mt-1 text-sm text-red-700">
                          Once you delete a project, there is no going back. All logs and data
                          associated with this project will be permanently removed.
                        </div>
                      </div>

                      <div className="ml-4">
                        <Button
                          type="primary"
                          danger
                          onClick={handleDeleteProject}
                          disabled={!canEdit || isDeleting || isSaving}
                          loading={isDeleting}
                          className="bg-red-600 hover:bg-red-700"
                        >
                          {isDeleting ? 'Deleting...' : 'Delete project'}
                        </Button>
                      </div>
                    </div>
                  </div>
                </div>

                <div className="max-w-2xl">
                  <h2 className="mb-4 text-xl font-bold text-gray-900 dark:text-gray-100">
                    Project statistics
                  </h2>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                    {renderStatsCard(
                      'Logs',
                      isLoadingStats,
                      'Total logs',
                      projectStats?.totalLogs,
                      projectStats?.totalSizeMb,
                      projectStats?.oldestLogTime,
                      projectStats?.newestLogTime,
                    )}
                    {renderStatsCard(
                      'Traces',
                      isLoadingSpanStats,
                      'Total spans',
                      spanStats?.totalSpans,
                      spanStats?.totalSizeMb,
                      spanStats?.oldestSpanTime,
                      spanStats?.newestSpanTime,
                    )}
                    {renderStatsCard(
                      'Metrics',
                      isLoadingMetricStats,
                      'Total samples',
                      metricStats?.totalSamples,
                      metricStats?.totalSizeMb,
                      metricStats?.oldestSampleTime,
                      metricStats?.newestSampleTime,
                    )}
                  </div>
                </div>

                <ProjectAuditLogsComponent projectId={project.id} />
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
