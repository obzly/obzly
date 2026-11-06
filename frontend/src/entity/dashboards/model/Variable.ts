// A template variable used as ${key} in panel queries; substituted on the
// frontend before a panel query is sent (the backend only stores these).
export type VariableSource = 'static' | 'metricLabel' | 'services';

export interface Variable {
  key: string;
  label: string;
  source: VariableSource;
  // Required when source === 'metricLabel' (e.g. 'k8s_namespace_name').
  metricLabelName?: string;
  // Required when source === 'static'.
  staticValues?: string[];
  defaultValue?: string;
}
