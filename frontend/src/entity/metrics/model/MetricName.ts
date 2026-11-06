export interface MetricNameInfo {
  name: string;
  type: 'gauge' | 'counter';
}

export interface ListMetricNamesResponse {
  metrics: MetricNameInfo[];
}

export interface ListLabelValuesRequest {
  labelName: string;
}

export interface ListLabelValuesResponse {
  values: string[];
}
