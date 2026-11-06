export type OrgRole = 'ORG_OWNER' | 'ORG_ADMIN' | 'ORG_MEMBER';

export interface Organization {
  id: string;
  name: string;
  slug: string;
  createdAt: string;
  // The caller's role; '' for a global admin who isn't a member.
  myRole: OrgRole | '';
}

export interface OrgDetail extends Organization {
  sites: string[];
}

export interface OrgMember {
  orgId: string;
  userId: string;
  role: OrgRole;
  // Set by SCIM: the IdP's id, and deprovisioned (no team access).
  externalId?: string;
  suspended: boolean;
  email: string;
  name: string;
}

export interface TeamMember {
  userId: string;
  email: string;
  name: string;
}

// A team's role on a Site (site-wide when projectId is null) or a project
// of it; siteId '' is a standalone install's own projects.
export interface TeamGrant {
  id: string;
  teamId: string;
  siteId: string;
  projectId: string | null;
  role: string;
}

export interface Team {
  id: string;
  orgId: string;
  name: string;
  members: TeamMember[];
  grants: TeamGrant[];
}

export interface SCIMToken {
  id: string;
  orgId: string;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string | null;
}

// Returned once, at creation.
export interface NewSCIMToken extends SCIMToken {
  token: string;
}

// An organization's quotas; 0 is unlimited. Sites enforce the per-project
// limits (the stricter of a project's own and these) and maxProjects; the
// control plane enforces maxSites and maxMembers.
export interface OrgPlanLimits {
  logsPerSecondLimit: number;
  maxLogsAmount: number;
  maxLogsSizeMb: number;
  maxLogsLifeDays: number;
  maxLogSizeKb: number;
  maxProjects: number;
  maxSites: number;
  maxMembers: number;
}

export interface OrgPlan extends OrgPlanLimits {
  orgId: string;
  updatedBy: string | null;
  updatedAt: string;
}
