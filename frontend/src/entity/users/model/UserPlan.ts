import type { UserPlanType } from './UserPlanType';

export interface UserPlan {
  id: string;
  name: string;
  type: UserPlanType;
  isPublic: boolean;
  allowedProjectsCount: number;
  warningText: string;
  upgradeText: string;

  // limits (0 means "unlimited")
  logsPerSecondLimit: number;
  maxLogsAmount: number;
  maxLogsSizeMb: number;
  maxLogsLifeDays: number;
  maxLogSizeKb: number;
}
