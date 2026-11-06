import { ProjectRole } from '../../users/model/ProjectRole';

export interface ProjectResponse {
  id: string;
  name: string;
  createdAt: Date;
  userRole?: ProjectRole;
  /** The reserved project holding Obzly's own request traces; only global admins see it. */
  isSystem?: boolean;
}

/**
 * Splits a project list into the ones people work in and the reserved
 * system project, which pickers hide behind an explicit link.
 */
export const splitSystemProject = (
  projects: ProjectResponse[],
): { regular: ProjectResponse[]; system?: ProjectResponse } => ({
  regular: projects.filter((project) => !project.isSystem),
  system: projects.find((project) => project.isSystem),
});
