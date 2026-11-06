export interface CreateSSOGroupMappingRequest {
  groupName: string;
  // Omit/null with role 'ADMIN' to grant the global Admin role (root admin only).
  projectId?: string | null;
  role: string;
}
