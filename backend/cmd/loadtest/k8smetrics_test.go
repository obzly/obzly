package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Every scrape has the same number of series (about 29 a pod, 137 a node),
// a batch is exactly n points, counters grow from scrape to scrape, and a
// replaced pod brings new series whose counters start over.
func Test_K8sMetrics_ScrapesAreFixedSizeWithGrowingCountersAndChurn(t *testing.T) {
	services := k8sServices(300)
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	perScrape := len(k8sMetricSeries(services, start))
	require.Equal(t, perScrape, len(k8sMetricSeries(services, start.Add(36*time.Hour))))
	assert.InDelta(t, 10_500, perScrape, 1_500, "sized like a production Site's 10.6k series")

	interval := 30 * time.Second
	first := buildK8sMetricsRequest(services, start, interval, perScrape, 0, perScrape)
	second := buildK8sMetricsRequest(services, start, interval, perScrape, int64(perScrape), perScrape)
	require.Equal(t, perScrape, pointCount(first))
	require.Equal(t, perScrape, pointCount(second))

	before, after := nodeCounter(t, first), nodeCounter(t, second)
	assert.Greater(t, after, before, "a node counter grows between scrapes")

	// A day later most pods have been replaced: their names are new.
	later := buildK8sMetricsRequest(services, start.Add(24*time.Hour), interval, perScrape, 0, perScrape)
	assert.NotEqual(t, podNames(first), podNames(later))
}

func pointCount(request interface {
	GetResourceMetrics() []*metricsv1.ResourceMetrics
}) int {
	n := 0
	for _, resource := range request.GetResourceMetrics() {
		for _, metric := range resource.ScopeMetrics[0].Metrics {
			n += len(metric.GetGauge().GetDataPoints()) + len(metric.GetSum().GetDataPoints()) +
				len(metric.GetHistogram().GetDataPoints())
		}
	}
	return n
}

func nodeCounter(t *testing.T, request interface {
	GetResourceMetrics() []*metricsv1.ResourceMetrics
}) float64 {
	t.Helper()
	for _, resource := range request.GetResourceMetrics() {
		for _, metric := range resource.ScopeMetrics[0].Metrics {
			if metric.Name == "system.cpu.time" {
				return metric.GetSum().DataPoints[0].GetAsDouble()
			}
		}
	}
	t.Fatal("no system.cpu.time point")
	return 0
}

func podNames(request interface {
	GetResourceMetrics() []*metricsv1.ResourceMetrics
}) map[string]bool {
	names := map[string]bool{}
	for _, resource := range request.GetResourceMetrics() {
		for _, attr := range resource.Resource.Attributes {
			if attr.Key == "k8s.pod.name" {
				names[attr.Value.GetStringValue()] = true
			}
		}
	}
	return names
}
