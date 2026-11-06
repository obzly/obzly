export interface GetAuditLogsRequest {
  limit?: number;
  offset?: number;
  beforeDate?: string;
  // Global audit logs only.
  user?: string;
  action?: string;
  sortBy?: 'createdAt' | 'user';
  sortOrder?: 'asc' | 'desc';
}
