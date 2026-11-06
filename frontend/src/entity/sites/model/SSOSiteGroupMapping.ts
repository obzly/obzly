// An SSO connector's group -> Site access mapping (control plane only):
// projectId null = a Site-wide role (ADMIN / MEMBER / VIEWER), otherwise a
// project role on that Site's project.
export interface SSOSiteGroupMapping {
  id: string;
  connectorId: string;
  groupName: string;
  siteId: string;
  projectId: string | null;
  role: string;
  createdAt: string;
}

export interface CreateSSOSiteGroupMappingRequest {
  groupName: string;
  siteId: string;
  projectId: string | null;
  role: string;
}
