// Maintenance windows live on the control plane with the monitors.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  MaintenanceOccurrence,
  MaintenanceRequest,
  MaintenanceSchedule,
  MaintenanceWindow,
  Occurrence,
} from '../model/Maintenance';

const base = () => `${getApplicationServer()}/api/v1/synthetics/maintenance`;

const body = (value: unknown) => new RequestOptions().setBody(JSON.stringify(value));

export const maintenanceApi = {
  list: (): Promise<MaintenanceWindow[]> => apiHelper.fetchGetJson(base()),
  create: (request: MaintenanceRequest): Promise<MaintenanceWindow> =>
    apiHelper.fetchPostJson(base(), body(request)),
  update: (id: string, request: MaintenanceRequest): Promise<MaintenanceWindow> =>
    apiHelper.fetchPutJson(`${base()}/${id}`, body(request)),
  remove: (id: string): Promise<unknown> => apiHelper.fetchDeleteJson(`${base()}/${id}`),
  /** Ends the current occurrence now. */
  end: (id: string): Promise<MaintenanceWindow> => apiHelper.fetchPostJson(`${base()}/${id}/end`),
  /** Starts the next occurrence now, for its planned duration. */
  start: (id: string): Promise<MaintenanceWindow> =>
    apiHelper.fetchPostJson(`${base()}/${id}/start`),
  /** Validates a schedule and lists its next occurrences. */
  preview: (
    schedule: MaintenanceSchedule,
  ): Promise<{ schedule: MaintenanceSchedule; occurrences: Occurrence[] }> =>
    apiHelper.fetchPostJson(`${base()}/preview`, body({ schedule })),
  occurrences: (from: Date, to: Date): Promise<MaintenanceOccurrence[]> =>
    apiHelper.fetchGetJson(
      `${base()}/occurrences?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`,
    ),
};
