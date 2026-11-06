import { auditLogApi } from '../../../entity/audit-logs/api/auditLogApi';
import { AuditLogTable } from '../../../entity/audit-logs/ui/AuditLogTable';

export function AuditLogsComponent() {
  return (
    <AuditLogTable
      title="Audit logs"
      exportName="obzly-audit-logs"
      load={(request) => auditLogApi.getGlobalAuditLogs(request)}
    />
  );
}
