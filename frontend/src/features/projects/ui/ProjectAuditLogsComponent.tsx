import { AuditLogTable } from '../../../entity/audit-logs/ui/AuditLogTable';
import { projectApi } from '../../../entity/projects/api/projectApi';

interface Props {
  projectId: string;
}

export function ProjectAuditLogsComponent({ projectId }: Props) {
  return (
    <AuditLogTable
      title="Project Audit Logs"
      scopeKey={projectId}
      showProjectColumn={false}
      load={(request) => projectApi.getProjectAuditLogs(projectId, request)}
    />
  );
}
