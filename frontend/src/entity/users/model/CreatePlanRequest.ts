import type { UserPlanType } from './UserPlanType';

export interface CreatePlanRequest {
  name: string;
  type: UserPlanType;
  isPublic?: boolean;
  warningText?: string;
  upgradeText?: string;
  logsPerSecondLimit: number;
  maxLogsAmount: number;
  maxLogsSizeMb: number;
  maxLogsLifeDays: number;
  maxLogSizeKb: number;
  allowedProjectsCount: number;
}
