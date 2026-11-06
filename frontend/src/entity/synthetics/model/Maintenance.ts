export type ScheduleKind = 'once' | 'daily' | 'weekly' | 'monthly_day' | 'monthly_weekday';

export type Weekday = 'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat' | 'sun';

/**
 * When a window applies: once (startsAt to endsAt), or repeatedly at
 * startTime in timeZone for durationMinutes, from startDate through the
 * optional endDate.
 */
export interface MaintenanceSchedule {
  kind: ScheduleKind;
  startsAt?: string;
  endsAt?: string;
  timeZone?: string;
  /** "HH:MM", 24-hour. */
  startTime?: string;
  durationMinutes?: number;
  weekdays?: Weekday[];
  dayOfMonth?: number;
  /** 1-4, or -1 for the last one. */
  weekOfMonth?: number;
  weekday?: Weekday;
  /** "YYYY-MM-DD". */
  startDate?: string;
  endDate?: string;
}

export interface MaintenanceScope {
  all: boolean;
  monitorIds: string[];
  /** Folder paths; subfolders are covered too. */
  folders: string[];
}

export interface Occurrence {
  start: string;
  end: string;
}

export type MaintenanceState = 'active' | 'upcoming' | 'past' | 'paused';

export interface MaintenanceWindow {
  id: string;
  name: string;
  reason: string;
  scope: MaintenanceScope;
  schedule: MaintenanceSchedule;
  enabled: boolean;
  version: number;
  createdBy?: string;
  createdAt: string;
  updatedAt: string;
  state: MaintenanceState;
  current?: Occurrence;
  next?: Occurrence;
  lastEnded?: string;
  /** Monitors it covers now. */
  monitorIds: string[];
}

export interface MaintenanceRequest {
  name: string;
  reason: string;
  scope: MaintenanceScope;
  schedule: MaintenanceSchedule;
  enabled: boolean;
  version?: number;
}

export interface MaintenanceOccurrence {
  windowId: string;
  name: string;
  reason: string;
  start: string;
  end: string;
  monitorIds: string[];
}
