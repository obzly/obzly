export interface SetAdminPasswordRequest {
  password: string;
  // Printed to the server logs at first start, so only someone with log
  // access can claim the root admin.
  bootstrapToken: string;
}
