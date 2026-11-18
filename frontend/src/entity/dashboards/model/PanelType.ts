// statTrend is its own type (not a stat option) so stat panels saved before it
// render exactly as they did.
export type PanelType =
  | 'line'
  | 'bar'
  | 'pie'
  | 'stat'
  | 'table'
  | 'statTrend'
  | 'rankedList'
  | 'donut'
  | 'gauge'
  | 'stackedBar'
  | 'statusStrip';
