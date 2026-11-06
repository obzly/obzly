export type PromotedFieldType = 'string' | 'number';

// A log field copied into its own typed column at ingest, so filters and
// grouping on it skip parsing the fields JSON.
export interface PromotedField {
  projectId: string;
  slot: string;
  field: string;
  createdAt: string;
}
