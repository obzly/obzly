export interface SignUpRequest {
  email: string;
  password: string;
  name: string;
  // Required for an invited email: proves the signup holds the invite link.
  inviteToken?: string;
}
