import { auditLogApi } from '../../../entity/audit-logs/api/auditLogApi';
import { AuditLogTable } from '../../../entity/audit-logs/ui/AuditLogTable';
import { Card } from '../../../shared/ui/v2';

export function AuditLogsComponent() {
  return (
    <Card>
      <AuditLogTable
        title="Audit logs"
        exportName="obzly-audit-logs"
        load={(request) => auditLogApi.getGlobalAuditLogs(request)}
      />
    </Card>
  );
}
