import type { AddMemberStatusEnum } from './AddMemberStatus';

export interface AddMemberResponse {
  status: AddMemberStatusEnum;
  // Only when INVITED: relay it to the user, since no email is sent.
  inviteToken?: string;
}
