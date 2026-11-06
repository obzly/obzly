import { Button, Input } from 'antd';
import { useEffect, useMemo, useRef, useState } from 'react';

import { type ProjectResponse, splitSystemProject } from '../../../entity/projects';
import { SystemProjectLinkComponent } from './SystemProjectLinkComponent';

interface Props {
  projects: ProjectResponse[];
  selectedProject?: ProjectResponse;
  onCreateProject: () => void;
  onProjectSelect: (project: ProjectResponse) => void;
}

export const ProjectSelectionComponent = ({
  projects,
  selectedProject,
  onCreateProject,
  onProjectSelect,
}: Props) => {
  const [isDropdownOpen, setIsDropdownOpen] = useState(false);
  const [searchValue, setSearchValue] = useState('');
  const dropdownRef = useRef<HTMLDivElement>(null);

  const { regular: regularProjects, system: systemProject } = useMemo(
    () => splitSystemProject(projects),
    [projects],
  );

  const filteredProjects = useMemo(() => {
    // The system project stays out of the list unless it's the one open.
    const listed =
      systemProject && selectedProject?.id === systemProject.id
        ? [...regularProjects, systemProject]
        : regularProjects;
    if (!searchValue.trim()) return listed;
    const searchTerm = searchValue.toLowerCase();
    return listed.filter((project) => project.name.toLowerCase().includes(searchTerm));
  }, [regularProjects, systemProject, selectedProject, searchValue]);

  const openProject = (project: ProjectResponse) => {
    setIsDropdownOpen(false);
    setSearchValue('');
    onProjectSelect?.(project);
  };

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsDropdownOpen(false);
        setSearchValue('');
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const isSystemProjectOpen = !!systemProject && selectedProject?.id === systemProject.id;

  if (regularProjects.length === 0 && !isSystemProjectOpen) {
    return (
      <div className="flex items-center gap-3">
        <Button
          type="primary"
          onClick={onCreateProject}
          className="border-emerald-600 bg-emerald-600 hover:border-emerald-700 hover:bg-emerald-700"
        >
          Create project
        </Button>
        {systemProject && <SystemProjectLinkComponent onClick={() => openProject(systemProject)} />}
      </div>
    );
  }

  return (
    <div className="my-1 w-[250px] select-none" ref={dropdownRef}>
      <div className="mb-1 text-xs text-gray-400 dark:text-gray-400" style={{ lineHeight: 0.7 }}>
        Selected project
      </div>

      <div className="relative">
        <div
          className="cursor-pointer rounded bg-gray-100 p-1 px-2 hover:bg-gray-200 dark:bg-gray-700"
          onClick={() => setIsDropdownOpen(!isDropdownOpen)}
        >
          <div className="flex items-center justify-between text-sm">
            <div className="max-w-[250px] truncate">
              {selectedProject?.name || 'Select a project'}
            </div>
            <img
              src="/icons/menu/arrow-down-gray.svg"
              alt="arrow-down"
              className={`ml-1 transition-transform duration-200 ${isDropdownOpen ? 'rotate-180' : ''}`}
              width={15}
              height={15}
            />
          </div>
        </div>

        {isDropdownOpen && (
          <div className="absolute top-full left-0 z-50 mt-1 min-w-full rounded-md border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800">
            <div className="border-b border-gray-100 p-2">
              <Input
                placeholder="Search projects..."
                value={searchValue}
                onChange={(e) => setSearchValue(e.target.value)}
                className="border-0 shadow-none"
                autoFocus
              />
            </div>

            <div className="max-h-[400px] overflow-y-auto">
              {filteredProjects.map((project) => (
                <div
                  key={project.id}
                  className="max-w-[250px] cursor-pointer truncate px-3 py-2 text-sm hover:bg-gray-50 dark:bg-gray-900 dark:hover:bg-gray-700"
                  onClick={() => openProject(project)}
                >
                  {project.name}
                </div>
              ))}

              {filteredProjects.length === 0 && searchValue && (
                <div className="px-3 py-2 text-sm text-gray-500 dark:text-gray-300">
                  No projects found
                </div>
              )}
            </div>

            {systemProject && !isSystemProjectOpen && (
              <div className="border-t border-gray-100 px-3 py-2 dark:border-gray-700">
                <SystemProjectLinkComponent onClick={() => openProject(systemProject)} />
              </div>
            )}

            <div className="border-t border-gray-100">
              <div
                className="cursor-pointer px-3 py-2 text-sm text-emerald-600 hover:bg-gray-50 hover:text-emerald-700 dark:bg-gray-900 dark:hover:bg-gray-700"
                onClick={() => {
                  onCreateProject();
                  setIsDropdownOpen(false);
                  setSearchValue('');
                }}
              >
                + Create new project
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};
