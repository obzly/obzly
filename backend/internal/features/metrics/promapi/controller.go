package metrics_promapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	metrics_promql "obzly/internal/features/metrics/promql"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/util/annotations"
)

const maxWarnings = 10

// Controller serves the Prometheus-compatible query API per project, so a
// Prometheus datasource's URL is exactly this prefix.
type Controller struct {
	service *MetricsQueryService
}

func (c *Controller) RegisterRoutes(router gin.IRouter) {
	routes := router.Group("/api/v1/metrics/:projectId/api/v1")

	routes.GET("/query", c.InstantQuery)
	routes.POST("/query", c.InstantQuery)
	routes.GET("/query_range", c.RangeQuery)
	routes.POST("/query_range", c.RangeQuery)
	routes.GET("/series", c.Series)
	routes.POST("/series", c.Series)
	routes.GET("/labels", c.LabelNames)
	routes.POST("/labels", c.LabelNames)
	routes.GET("/label/:name/values", c.LabelValues)
	// Some clients (Grafana included) probe buildinfo when a datasource is
	// tested and report it broken on a 404.
	routes.GET("/status/buildinfo", c.BuildInfo)
}

func (c *Controller) InstantQuery(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	query := formValue(ctx, "query")
	if query == "" {
		writeError(ctx, http.StatusBadRequest, "bad_data", "missing 'query' parameter")
		return
	}

	ts, ok := parseTimeParam(ctx, "time", time.Now().UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'time' parameter")
		return
	}

	value, warnings, err := c.service.InstantQuery(projectID, apiKeyFromRequest(ctx), query, ts)
	if err != nil {
		writeQueryError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, successResponse(queryDataFor(value), warningStrings(warnings, query)))
}

func (c *Controller) RangeQuery(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	query := formValue(ctx, "query")
	if query == "" {
		writeError(ctx, http.StatusBadRequest, "bad_data", "missing 'query' parameter")
		return
	}

	start, ok := parseTimeParam(ctx, "start", time.Time{})
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'start' parameter")
		return
	}
	end, ok := parseTimeParam(ctx, "end", time.Time{})
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'end' parameter")
		return
	}
	step, ok := parseDurationParam(ctx, "step")
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'step' parameter")
		return
	}

	value, warnings, err := c.service.RangeQuery(projectID, apiKeyFromRequest(ctx), query, start, end, step)
	if err != nil {
		writeQueryError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, successResponse(queryDataFor(value), warningStrings(warnings, query)))
}

func (c *Controller) Series(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	matches := formValues(ctx, "match[]")
	if len(matches) == 0 {
		writeError(ctx, http.StatusBadRequest, "bad_data", "missing 'match[]' parameter")
		return
	}
	matcherSets, err := metrics_promql.GetParser().ParseMetricSelectors(matches)
	if err != nil {
		writeError(ctx, http.StatusBadRequest, "bad_data", err.Error())
		return
	}

	start, ok := parseTimeParam(ctx, "start", time.Now().UTC().Add(-1*time.Hour))
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'start' parameter")
		return
	}
	end, ok := parseTimeParam(ctx, "end", time.Now().UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'end' parameter")
		return
	}

	result, err := c.service.Series(projectID, apiKeyFromRequest(ctx), matcherSets, start, end)
	if err != nil {
		writeQueryError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, successResponse(result, nil))
}

func (c *Controller) LabelNames(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	matchers, ok := parseOptionalMatchers(ctx)
	if !ok {
		return
	}

	start, ok := parseTimeParam(ctx, "start", time.Unix(0, 0).UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'start' parameter")
		return
	}
	end, ok := parseTimeParam(ctx, "end", time.Now().UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'end' parameter")
		return
	}

	names, err := c.service.LabelNames(projectID, apiKeyFromRequest(ctx), matchers, start, end)
	if err != nil {
		writeQueryError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, successResponse(names, nil))
}

func (c *Controller) LabelValues(ctx *gin.Context) {
	projectID, ok := parseProjectID(ctx)
	if !ok {
		return
	}

	matchers, ok := parseOptionalMatchers(ctx)
	if !ok {
		return
	}

	start, ok := parseTimeParam(ctx, "start", time.Unix(0, 0).UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'start' parameter")
		return
	}
	end, ok := parseTimeParam(ctx, "end", time.Now().UTC())
	if !ok {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid 'end' parameter")
		return
	}

	values, err := c.service.LabelValues(projectID, apiKeyFromRequest(ctx), ctx.Param("name"), matchers, start, end)
	if err != nil {
		writeQueryError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, successResponse(values, nil))
}

// BuildInfo is a fixed stub: clients only check that the request succeeds.
func (c *Controller) BuildInfo(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, successResponse(gin.H{
		"version":   "2.x-compatible",
		"revision":  "obzly",
		"branch":    "",
		"buildUser": "",
		"buildDate": "",
		"goVersion": "",
	}, nil))
}

func parseOptionalMatchers(ctx *gin.Context) ([]*labels.Matcher, bool) {
	matches := formValues(ctx, "match[]")
	if len(matches) == 0 {
		return nil, true
	}

	var all []*labels.Matcher
	for _, m := range matches {
		parsed, err := metrics_promql.GetParser().ParseMetricSelector(m)
		if err != nil {
			writeError(ctx, http.StatusBadRequest, "bad_data", err.Error())
			return nil, false
		}
		all = append(all, parsed...)
	}
	return all, true
}

func parseProjectID(ctx *gin.Context) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(ctx.Param("projectId"))
	if err != nil {
		writeError(ctx, http.StatusBadRequest, "bad_data", "invalid project ID")
		return uuid.Nil, false
	}
	return projectID, true
}

// apiKeyFromRequest accepts a header or a query param: Prometheus
// datasources use either.
func apiKeyFromRequest(ctx *gin.Context) string {
	if apiKey := ctx.GetHeader("X-API-Key"); apiKey != "" {
		return apiKey
	}
	return ctx.Query("api_key")
}

func formValue(ctx *gin.Context, name string) string {
	if v := ctx.PostForm(name); v != "" {
		return v
	}
	return ctx.Query(name)
}

func formValues(ctx *gin.Context, name string) []string {
	if values := ctx.PostFormArray(name); len(values) > 0 {
		return values
	}
	return ctx.QueryArray(name)
}

// parseTimeParam accepts Unix seconds (fractional) or RFC3339, per the
// Prometheus API.
func parseTimeParam(ctx *gin.Context, name string, fallback time.Time) (time.Time, bool) {
	raw := formValue(ctx, name)
	if raw == "" {
		return fallback, true
	}

	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.UnixMilli(int64(seconds * 1000)).UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}

// parseDurationParam accepts seconds or a duration ("1m30s"), per the
// Prometheus API; empty defaults to Prometheus's 15s step.
func parseDurationParam(ctx *gin.Context, name string) (time.Duration, bool) {
	raw := formValue(ctx, name)
	if raw == "" {
		return 15 * time.Second, true
	}

	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Duration(seconds * float64(time.Second)), true
	}
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed, true
	}
	return 0, false
}

func warningStrings(warnings annotations.Annotations, query string) []string {
	if len(warnings) == 0 {
		return nil
	}
	strs, _ := warnings.AsStrings(query, maxWarnings, 0)
	return strs
}

func writeQueryError(ctx *gin.Context, err error) {
	if errors.Is(err, errUnauthenticated) {
		writeError(ctx, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	writeError(ctx, http.StatusUnprocessableEntity, "execution", err.Error())
}

func writeError(ctx *gin.Context, statusCode int, errorType, message string) {
	ctx.JSON(statusCode, errorResponse(errorType, message))
}
