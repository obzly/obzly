import type { Panel } from './Panel';

export interface Tab {
  id: string;
  title: string;
  panels: Panel[];
}
