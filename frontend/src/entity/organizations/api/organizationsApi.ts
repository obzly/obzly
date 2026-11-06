// Organizations live where users are managed - always this deployment's
// own origin (getApplicationServer), never a Site through the proxy.
import { getApplicationServer } from '../../../constants';
import RequestOptions from '../../../shared/api/RequestOptions';
import { apiHelper } from '../../../shared/api/apiHelper';
import type {
  NewSCIMToken,
  OrgDetail,
  OrgMember,
  OrgPlan,
  OrgPlanLimits,
  OrgRole,
  Organization,
  SCIMToken,
  Team,
  TeamGrant,
} from '../model/Organization';

const base = () => `${getApplicationServer()}/api/v1/orgs`;

const withBody = (body: unknown): RequestOptions => {
  const options = new RequestOptions();
  options.setBody(JSON.stringify(body));
  return options;
};

export const organizationsApi = {
  listOrgs: (): Promise<Organization[]> => apiHelper.fetchGetJson(base(), new RequestOptions()),
  createOrg: (name: string, slug: string): Promise<Organization> =>
    apiHelper.fetchPostJson(base(), withBody({ name, slug })),
  getOrg: (orgId: string): Promise<OrgDetail> =>
    apiHelper.fetchGetJson(`${base()}/${orgId}`, new RequestOptions()),
  renameOrg: (orgId: string, name: string): Promise<Organization> =>
    apiHelper.fetchPutJson(`${base()}/${orgId}`, withBody({ name })),
  deleteOrg: (orgId: string): Promise<{ message: string }> =>
    apiHelper.fetchDeleteJson(`${base()}/${orgId}`, new RequestOptions()),

  listMembers: (orgId: string): Promise<OrgMember[]> =>
    apiHelper.fetchGetJson(`${base()}/${orgId}/members`, new RequestOptions()),
  setMember: (orgId: string, member: { userId?: string; email?: string; role: OrgRole }) =>
    apiHelper.fetchPostJson(`${base()}/${orgId}/members`, withBody(member)),
  removeMember: (orgId: string, userId: string) =>
    apiHelper.fetchDeleteJson(`${base()}/${orgId}/members/${userId}`, new RequestOptions()),
  assignSite: (orgId: string, siteId: string) =>
    apiHelper.fetchPutJson(
      `${base()}/${orgId}/sites/${encodeURIComponent(siteId)}`,
      new RequestOptions(),
    ),

  listTeams: (orgId: string): Promise<Team[]> =>
    apiHelper.fetchGetJson(`${base()}/${orgId}/teams`, new RequestOptions()),
  createTeam: (orgId: string, name: string): Promise<Team> =>
    apiHelper.fetchPostJson(`${base()}/${orgId}/teams`, withBody({ name })),
  deleteTeam: (orgId: string, teamId: string) =>
    apiHelper.fetchDeleteJson(`${base()}/${orgId}/teams/${teamId}`, new RequestOptions()),
  addTeamMember: (orgId: string, teamId: string, email: string) =>
    apiHelper.fetchPostJson(`${base()}/${orgId}/teams/${teamId}/members`, withBody({ email })),
  removeTeamMember: (orgId: string, teamId: string, userId: string) =>
    apiHelper.fetchDeleteJson(
      `${base()}/${orgId}/teams/${teamId}/members/${userId}`,
      new RequestOptions(),
    ),
  addGrant: (
    orgId: string,
    teamId: string,
    grant: { siteId: string; projectId: string | null; role: string },
  ): Promise<TeamGrant> =>
    apiHelper.fetchPostJson(`${base()}/${orgId}/teams/${teamId}/grants`, withBody(grant)),
  getPlan: (orgId: string): Promise<OrgPlan> =>
    apiHelper.fetchGetJson(`${base()}/${orgId}/plan`, new RequestOptions()),
  setPlan: (orgId: string, limits: OrgPlanLimits): Promise<OrgPlan> =>
    apiHelper.fetchPutJson(`${base()}/${orgId}/plan`, withBody(limits)),

  listSCIMTokens: (orgId: string): Promise<SCIMToken[]> =>
    apiHelper.fetchGetJson(`${base()}/${orgId}/scim-tokens`, new RequestOptions()),
  createSCIMToken: (orgId: string, name: string): Promise<NewSCIMToken> =>
    apiHelper.fetchPostJson(`${base()}/${orgId}/scim-tokens`, withBody({ name })),
  deleteSCIMToken: (orgId: string, tokenId: string) =>
    apiHelper.fetchDeleteJson(`${base()}/${orgId}/scim-tokens/${tokenId}`, new RequestOptions()),
  /** The SCIM 2.0 base URL to give an IdP. */
  scimBaseUrl: () => `${getApplicationServer()}/scim/v2`,
  removeGrant: (orgId: string, teamId: string, grantId: string) =>
    apiHelper.fetchDeleteJson(
      `${base()}/${orgId}/teams/${teamId}/grants/${grantId}`,
      new RequestOptions(),
    ),
};
