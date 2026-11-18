import type { UserRole } from './UserRole';

export interface UserSignInMethod {
  kind: 'password' | 'sso' | 'github' | 'google';
  /** The SSO connector's name. */
  name?: string;
}

export interface UserOrgMembership {
  orgId: string;
  name: string;
  role: string;
  suspended: boolean;
  teams: string[];
}

/** A control-plane grant on a Site: Site-wide when projectId is null. */
export interface UserSiteAccess {
  siteId: string;
  siteName: string;
  projectId: string | null;
  role: string;
  via: 'direct' | 'sso' | 'team';
  team?: string;
  org?: string;
}

/** A role on this instance's own projects; projectId null is every project. */
export interface UserProjectRole {
  projectId: string | null;
  projectName: string;
  role: string;
  via: 'direct' | 'sso' | 'control-plane' | 'team';
  team?: string;
  org?: string;
}

export interface UserDetail {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  status: 'ACTIVE' | 'INVITED' | 'INACTIVE';
  isActive: boolean;
  isRootAdmin: boolean;
  createdAt: string;
  lastLoginAt: string | null;
  lastActiveAt: string | null;
  mfaEnabled: boolean;
  mfaRequired: boolean;
  /** Ways the account can sign in, not which it used last. */
  signInMethods: UserSignInMethod[];
  provisionedBy: { id: string; name: string } | null;
  organizations: UserOrgMembership[];
  siteAccess: UserSiteAccess[];
  projectRoles: UserProjectRole[];
  /** Control plane: roles given on a Site itself aren't known here. */
  projectRolesOnSites: boolean;
}
