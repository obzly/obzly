import type { ProjectRole } from './ProjectRole';

export interface InviteUserResponse {
  id: string;
  email: string;
  intendedProjectId?: string;
  intendedProjectRole?: ProjectRole;
  createdAt: string;
  // Shown once; the inviter relays it out of band since no email is sent.
  inviteToken: string;
}
