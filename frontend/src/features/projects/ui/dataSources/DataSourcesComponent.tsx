import { RightOutlined } from '@ant-design/icons';
import { useState } from 'react';

import type { ProjectResponse } from '../../../../entity/projects';
import { DataSourceConfigModal } from './DataSourceConfigModal';
import { DataSourceIcon } from './DataSourceIcon';
import { dataSourceCatalog, dataSourceCategoryOrder, groupDataSourcesByCategory } from './catalog';
import type { DataSourceDefinition } from './types';

interface Props {
  contentHeight: number;
  projectResponse: ProjectResponse;
}

/**
 * Catalog of ready-to-use collector configs; each card opens
 * DataSourceConfigModal, which embeds a dedicated API key in the config.
 */
export function DataSourcesComponent({ contentHeight, projectResponse }: Props) {
  const [selectedSource, setSelectedSource] = useState<DataSourceDefinition | null>(null);

  const grouped = groupDataSourcesByCategory(dataSourceCatalog);

  return (
    <div className="flex grow pl-3">
      <div className="w-full">
        <div
          className="grow overflow-y-auto rounded bg-white p-5 shadow dark:bg-gray-800"
          style={{ height: contentHeight }}
        >
          <div className="max-w-[900px]">
            <h1 className="mb-1 text-2xl font-bold">Data Sources</h1>
            <p className="mb-6 text-sm text-gray-600 dark:text-gray-300">
              Pick what you want to onboard and get a ready-to-use config with your project&apos;s
              endpoint and a fresh API key already filled in.
            </p>

            {dataSourceCategoryOrder.map((category) => {
              const sources = grouped.get(category);
              if (!sources || sources.length === 0) return null;

              return (
                <div key={category} className="mb-8">
                  <h2 className="mb-3 text-sm font-semibold tracking-wide text-gray-500 uppercase dark:text-gray-300">
                    {category}
                  </h2>
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    {sources.map((source) => (
                      <button
                        key={source.id}
                        type="button"
                        onClick={() => setSelectedSource(source)}
                        className="flex items-center gap-3 rounded border border-gray-200 p-4 text-left transition-colors hover:border-emerald-500 hover:bg-emerald-50 dark:border-gray-700 dark:hover:bg-emerald-900/40"
                      >
                        <DataSourceIcon sourceId={source.id} className="shrink-0" />
                        <div className="min-w-0 flex-1">
                          <div className="font-medium text-gray-900 dark:text-gray-100">
                            {source.name}
                          </div>
                          <div className="mt-1 text-xs text-gray-500 dark:text-gray-300">
                            {source.description}
                          </div>
                        </div>
                        <RightOutlined className="ml-1 shrink-0 text-gray-400 dark:text-gray-400" />
                      </button>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>

      {selectedSource && (
        <DataSourceConfigModal
          projectId={projectResponse.id}
          definition={selectedSource}
          onClose={() => setSelectedSource(null)}
          onApiKeyCreated={() => {
            // Nothing to sync: the API Keys page reloads its list on mount.
          }}
        />
      )}
    </div>
  );
}
