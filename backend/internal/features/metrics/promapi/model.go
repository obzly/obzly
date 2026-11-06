// Package metrics_promapi serves the Prometheus-compatible HTTP query API
// per project at /api/v1/metrics/:projectId/..., so a Prometheus datasource
// works unmodified with the project's API key.
//
// Responses follow the documented Prometheus HTTP API by hand: promql.Value's
// MarshalJSON methods are marked unused within Prometheus itself.
package metrics_promapi

import (
	"strconv"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

type apiResponse struct {
	Status    string   `json:"status"`
	Data      any      `json:"data,omitempty"`
	ErrorType string   `json:"errorType,omitempty"`
	Error     string   `json:"error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type queryData struct {
	ResultType string `json:"resultType"`
	Result     any    `json:"result"`
}

type vectorResult struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

type matrixResult struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

func queryDataFor(value parser.Value) queryData {
	switch v := value.(type) {
	case promql.Vector:
		results := make([]vectorResult, len(v))
		for i, sample := range v {
			results[i] = vectorResult{Metric: labelsToMap(sample.Metric), Value: pointJSON(sample.T, sample.F)}
		}
		return queryData{ResultType: "vector", Result: results}

	case promql.Matrix:
		results := make([]matrixResult, len(v))
		for i, series := range v {
			values := make([][2]any, len(series.Floats))
			for j, point := range series.Floats {
				values[j] = pointJSON(point.T, point.F)
			}
			results[i] = matrixResult{Metric: labelsToMap(series.Metric), Values: values}
		}
		return queryData{ResultType: "matrix", Result: results}

	case promql.Scalar:
		return queryData{ResultType: "scalar", Result: pointJSON(v.T, v.V)}

	case promql.String:
		return queryData{ResultType: "string", Result: [2]any{float64(v.T) / 1000, v.V}}

	default:
		return queryData{ResultType: "vector", Result: []vectorResult{}}
	}
}

// pointJSON is [seconds, "value"]: the value is a string, as in Prometheus,
// so NaN/±Inf survive JSON.
func pointJSON(unixMillis int64, value float64) [2]any {
	return [2]any{float64(unixMillis) / 1000, formatSampleValue(value)}
}

func formatSampleValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func labelsToMap(lbls labels.Labels) map[string]string {
	return lbls.Map()
}

func successResponse(data any, warnings []string) apiResponse {
	return apiResponse{Status: "success", Data: data, Warnings: warnings}
}

func errorResponse(errorType, message string) apiResponse {
	return apiResponse{Status: "error", ErrorType: errorType, Error: message}
}
