export interface SiteGrant {
  id: string;
  userId: string;
  userEmail: string;
  siteId: string;
  projectId?: string | null;
  role: string;
  createdAt: string;
  /** Created by SSO group sync - a hand removal only lasts until their next SSO sign-in. */
  ssoManaged?: boolean;
}

export interface CreateGrantRequest {
  userId: string;
  siteId: string;
  projectId?: string | null;
  role: string;
}
