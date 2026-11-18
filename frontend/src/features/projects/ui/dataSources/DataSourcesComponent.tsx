import { RightOutlined } from '@ant-design/icons';
import { useState } from 'react';

import type { ProjectResponse } from '../../../../entity/projects';
import { Card } from '../../../../shared/ui/v2';
import { ProjectPageShell } from '../ProjectPageShell';
import { DataSourceConfigModal } from './DataSourceConfigModal';
import { DataSourceIcon } from './DataSourceIcon';
import { IngestionStatusComponent } from './IngestionStatusComponent';
import { dataSourceCatalog, dataSourceCategoryOrder, groupDataSourcesByCategory } from './catalog';
import type { DataSourceDefinition } from './types';

interface Props {
  contentHeight: number;
  projectResponse: ProjectResponse;
  embedded?: boolean;
}

/**
 * Catalog of ready-to-use collector configs; each card opens
 * DataSourceConfigModal, which embeds a dedicated API key in the config.
 */
export function DataSourcesComponent({ contentHeight, projectResponse, embedded }: Props) {
  const [selectedSource, setSelectedSource] = useState<DataSourceDefinition | null>(null);

  const grouped = groupDataSourcesByCategory(dataSourceCatalog);

  return (
    <ProjectPageShell
      embedded={embedded}
      contentHeight={contentHeight}
      title="Settings"
      subtitle={projectResponse.name}
    >
      <IngestionStatusComponent projectId={projectResponse.id} />

      <Card
        title="Data Sources"
        subtitle={
          <>
            Pick what you want to onboard and get a ready-to-use config with your project&apos;s
            endpoint and a fresh API key already filled in.
          </>
        }
      >
        <div className="flex flex-col gap-5">
          {dataSourceCategoryOrder.map((category) => {
            const sources = grouped.get(category);
            if (!sources || sources.length === 0) return null;

            return (
              <section key={category} aria-label={category} className="flex flex-col gap-2">
                <h3 className="m-0 text-xs font-medium tracking-wide text-gray-500 uppercase dark:text-gray-400">
                  {category}
                </h3>
                <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,260px),1fr))] gap-2.5">
                  {sources.map((source) => (
                    <button
                      key={source.id}
                      type="button"
                      onClick={() => setSelectedSource(source)}
                      className="hover:border-accent-500/60 flex cursor-pointer items-center gap-3 rounded-xl border border-gray-200 bg-white p-3 text-left transition-colors hover:bg-[var(--v2-ok-soft)] dark:border-gray-700 dark:bg-gray-800"
                    >
                      <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-[10px] border border-gray-200 bg-gray-50 dark:border-gray-700 dark:bg-gray-900/40">
                        <DataSourceIcon sourceId={source.id} size={22} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block font-medium text-gray-900 dark:text-gray-100">
                          {source.name}
                        </span>
                        <span className="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">
                          {source.description}
                        </span>
                      </span>
                      <RightOutlined
                        aria-hidden="true"
                        className="shrink-0 text-xs text-gray-400 dark:text-gray-400"
                      />
                    </button>
                  ))}
                </div>
              </section>
            );
          })}
        </div>
      </Card>

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
    </ProjectPageShell>
  );
}
