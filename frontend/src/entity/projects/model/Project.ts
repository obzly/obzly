import type { OrgPlanLimits } from '../../organizations/model/Organization';
import type { UserPlan } from '../../users/model/UserPlan';

export interface Project {
  id: string;
  name: string;
  createdAt: Date;
  plan?: UserPlan;
  // The organization plan this Site enforces, when it limits anything: it
  // caps the limits below even with rate limiting off.
  orgPlan?: OrgPlanLimits;

  isApiKeyRequired: boolean;
  isFilterByDomain: boolean;
  isFilterByIp: boolean;
  allowedDomains: string[];
  allowedIps: string[];

  // Gates the limits below except maxLogsLifeDays (retention always applies).
  isRateLimitingEnabled: boolean;
  logsPerSecondLimit: number;
  maxLogsAmount: number;
  maxLogsSizeMb: number;
  maxLogsLifeDays: number;
  maxLogSizeKb: number;
}
