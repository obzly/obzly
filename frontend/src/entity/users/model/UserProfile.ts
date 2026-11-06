import type { UserPlan } from './UserPlan';
import type { UserRole } from './UserRole';

export interface UserProfile {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  isActive: boolean;
  createdAt: Date;
  /** Null when the user never signed in. */
  lastLoginAt?: string | null;
  /** The last authenticated request, at most ~5 minutes stale. */
  lastActiveAt?: string | null;
  /** An authenticator is set up. */
  mfaEnabled?: boolean;
  plan?: UserPlan;
  /** The bootstrap super-admin ("admin", "superadmin" on a control plane, or ADMIN_EMAIL). */
  isRootAdmin?: boolean;
}
