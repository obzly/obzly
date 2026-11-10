// Saved Cost Explorer and Posture Trends views, kept per user on the control plane.
export type SavedViewKind = 'cost' | 'posture';

export interface SavedView<State = Record<string, unknown>> {
  id: string;
  kind: SavedViewKind;
  name: string;
  /** Shared views are read-only for everyone but their owner. */
  shared: boolean;
  state: State;
  owner: string;
  mine: boolean;
  updatedAt: string;
}

export interface SavedViewList<State = Record<string, unknown>> {
  views: SavedView<State>[];
  defaultId: string | null;
}

export interface SavedViewRequest<State = Record<string, unknown>> {
  kind: SavedViewKind;
  name: string;
  shared: boolean;
  state: State;
}
