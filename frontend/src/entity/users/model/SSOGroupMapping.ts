export interface SSOGroupMapping {
  id: string;
  connectorId: string;
  groupName: string;
  // null = grants the global Admin role instead of a project role.
  projectId: string | null;
  role: string;
  createdAt: string;
}
