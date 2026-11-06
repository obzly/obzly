import type { Session } from './Session';
import type { SignalDetail } from './SignalDetail';

export interface SessionDetail {
  session: Session;
  signals: SignalDetail[];
}
