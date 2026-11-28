package logs_receiving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"obzly/internal/broker"
	api_keys "obzly/internal/features/api_keys"
	"obzly/internal/features/ingestion"
	logs_core "obzly/internal/features/logs/core"
	logs_livetail "obzly/internal/features/logs/livetail"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	system_metrics "obzly/internal/features/system/metrics"
	"obzly/internal/ingest"
	"obzly/internal/telemetry"
	"obzly/internal/util/attrnames"
	rate_limit "obzly/internal/util/rate_limit"
	obzly_sampling "obzly/internal/util/sampling"
	time_parser "obzly/internal/util/time"

	"github.com/google/uuid"
)

const (
	// Rate limiting
	LogsBurstMultiplier = 5 // 5x base limit for burst handling

	// Batch limits: 5,000 per request lets a high-volume producer (many pods
	// behind one OTel Collector) send fewer, larger requests.
	MaxBatchSize      = 5000             // Maximum number of logs per batch
	MaxBatchSizeBytes = 10 * 1024 * 1024 // 10MB maximum batch size

	// Individual log limits
	MaxLogSizeFactor = 1024 // Convert KB to bytes
)

type LogReceivingService struct {
	rateLimiter       *rate_limit.RateLimiter
	projectService    *projects_services.ProjectService
	apiKeyService     *api_keys.ApiKeyService
	liveTailPublisher *logs_livetail.Publisher
	logger            *slog.Logger
}

// SubmitLogs accepts a batch for projectID. A refused batch is counted on
// the project's ingestion status by reason, but only for a caller the project
// accepts (it exists, its domain and IP filters pass, the API key is valid):
// the endpoint is open and the project id comes from the URL, so counting
// earlier let anyone add to a project's refusals or fill the tracker with
// unknown projects.
func (s *LogReceivingService) SubmitLogs(
	projectID uuid.UUID,
	request *SubmitLogsRequestDTO,
	clientIP, apiKey, origin string,
) (*SubmitLogsResponseDTO, error) {
	limitsErr := s.validateBasicBatchLimits(request)
	project, authErr := s.authorize(projectID, clientIP, apiKey, origin)
	if limitsErr != nil {
		if authErr == nil {
			s.countRefused(projectID, limitsErr, len(request.Logs))
		}
		return nil, limitsErr
	}
	if authErr != nil {
		return nil, authErr
	}

	response, err := s.submitAuthorized(project, request, clientIP)
	if err != nil {
		s.countRefused(projectID, err, len(request.Logs))
	}
	return response, err
}

// authorize returns the project when this caller may send it logs.
func (s *LogReceivingService) authorize(
	projectID uuid.UUID,
	clientIP, apiKey, origin string,
) (*projects_models.Project, error) {
	project, err := s.validateBasicProjectConstraints(projectID, origin, clientIP)
	if err != nil {
		return nil, err
	}
	if err := s.validateApiKey(project, apiKey); err != nil {
		return nil, err
	}
	return project, nil
}

// countRefused records an authorized caller's refused batch. rows is capped
// at MaxBatchSize: an oversized request states its own length.
func (s *LogReceivingService) countRefused(projectID uuid.UUID, err error, rows int) {
	var validationErr *logs_core.ValidationError
	if !errors.As(err, &validationErr) || rows <= 0 {
		return
	}
	ingestion.GetTracker().Refused(projectID, ingestion.SignalLogs, validationErr.Code, min(rows, MaxBatchSize))
}

func (s *LogReceivingService) submitAuthorized(
	project *projects_models.Project,
	request *SubmitLogsRequestDTO,
	clientIP string,
) (*SubmitLogsResponseDTO, error) {
	projectID := project.ID

	ids := requestRowIDs(projectID, request)
	validLogs, errors, totalBatchSize, acceptedBytes := s.processLogItems(
		request.Logs, project, projectID, clientIP, ids,
	)

	if err := s.validateTotalBatchSize(totalBatchSize); err != nil {
		return nil, err
	}

	if err := s.takeRateLimit(project, len(validLogs)); err != nil {
		return nil, err
	}

	s.logRejectionsIfAny(projectID, errors)
	for code, count := range rejectedByCode(errors) {
		ingestion.GetTracker().Refused(projectID, ingestion.SignalLogs, code, count)
	}
	// Sampled-out logs still count as accepted: dropping them is policy,
	// not an error the sender should retry.
	accepted := len(validLogs)
	sent := validLogs
	validLogs = sampleLogs(validLogs, projects_services.GetRetentionPolicyService().LogSampleRules(projectID))
	if err := s.queueValidLogs(validLogs, project); err != nil {
		return nil, err
	}
	// What the source sent, sampled-out logs included.
	ingestion.GetTracker().ObserveLogs(projectID, sent)
	system_metrics.RecordIngestion(len(validLogs), acceptedBytes)
	telemetry.IngestAcceptedRows.WithLabelValues(telemetry.SignalLogs).Add(float64(len(validLogs)))
	telemetry.IngestRejectedRows.WithLabelValues(telemetry.SignalLogs).Add(float64(len(errors)))

	// Fire-and-forget: Live Tail is a best-effort broadcast, so a slow transport
	// must never delay or fail ingest. validLogs is not mutated after this.
	// AI tool events live in their own stream (logs_core.StreamAILogs), not Search.
	if tailLogs := withoutAIEvents(validLogs); len(tailLogs) > 0 {
		go s.liveTailPublisher.PublishBatch(context.Background(), projectID, tailLogs)
	}

	return &SubmitLogsResponseDTO{
		Accepted: accepted,
		Rejected: len(errors),
		Errors:   errors,
	}, nil
}

// processLogItems validates and converts each item. The project's rate limit
// applies to the whole batch afterwards (takeRateLimit), never per item: an
// item refused inside an accepted batch is dropped by the sender.
func (s *LogReceivingService) processLogItems(
	logItems []LogItemRequestDTO,
	project *projects_models.Project,
	projectID uuid.UUID,
	clientIP string,
	ids *ingest.RowIDs,
) ([]*logs_core.LogItem, []LogSubmissionError, int, int64) {
	var validLogs []*logs_core.LogItem
	var errors []LogSubmissionError
	var totalBatchSize int
	var acceptedBytes int64

	for i, logItem := range logItems {
		// Every item takes its id, so ids follow the item's position.
		id := ids.Next()
		logSize, err := s.calculateLogSize(&logItem)
		if err != nil {
			errors = append(errors, s.createLogError(i, err))
			continue
		}

		totalBatchSize += logSize
		logItem.Level = s.normalizeLogLevel(logItem.Level)

		if err := s.validateLogItemWithSize(&logItem, project, logSize); err != nil {
			errors = append(errors, s.createLogError(i, err))
			continue
		}

		for key, value := range logItem.Fields {
			logItem.Fields[key] = s.convertFieldValueToString(value)
		}
		logItem.Fields = attrnames.NormalizeMap(logItem.Fields)

		validLogs = append(validLogs, &logs_core.LogItem{
			ID:        id,
			ProjectID: projectID,
			Timestamp: time_parser.ParseTimestamp(logItem.Timestamp),
			Level:     logItem.Level,
			Message:   s.prettyFormatIfMessageJSON(logItem.Message),
			Fields:    logItem.Fields,
			ClientIP:  clientIP,
			TraceID:   logItem.TraceID,
			SpanID:    logItem.SpanID,
		})

		acceptedBytes += int64(logSize)
	}

	return validLogs, errors, totalBatchSize, acceptedBytes
}

func (s *LogReceivingService) createLogError(index int, err error) LogSubmissionError {
	message := err.Error()
	if validationErr, ok := err.(*logs_core.ValidationError); ok {
		message = validationErr.Code
	}
	return LogSubmissionError{Index: index, Message: message}
}

// logRejectionsIfAny logs an aggregated count per validation code for
// rejected items (senders often only log "N of M rejected"), never log content.
func (s *LogReceivingService) logRejectionsIfAny(projectID uuid.UUID, errors []LogSubmissionError) {
	if len(errors) == 0 {
		return
	}

	s.logger.Warn("some log items in this batch were rejected",
		slog.String("projectId", projectID.String()),
		slog.Int("rejectedCount", len(errors)),
		slog.Any("rejectedByCode", rejectedByCode(errors)))
}

func rejectedByCode(errors []LogSubmissionError) map[string]int {
	byCode := make(map[string]int, len(errors))
	for _, e := range errors {
		byCode[e.Message]++
	}
	return byCode
}

// requestRowIDs seeds the batch's row ids from the request as the sender
// sent it, so a resent batch stores the same rows (ingest.RowIDs). A batch
// with a line lacking a timestamp gets random ids: that line is stamped on
// arrival, so the same request sent again later is new data, not a resend.
func requestRowIDs(projectID uuid.UUID, request *SubmitLogsRequestDTO) *ingest.RowIDs {
	encoded, err := json.Marshal(request.Logs)
	if err != nil || !allTimestamped(request.Logs) {
		random := uuid.New()
		encoded = random[:]
	}
	return ingest.NewRowIDs(projectID, encoded)
}

func allTimestamped(items []LogItemRequestDTO) bool {
	for _, item := range items {
		if item.Timestamp == nil || item.Timestamp == "" {
			return false
		}
	}
	return true
}

func (s *LogReceivingService) queueValidLogs(
	validLogs []*logs_core.LogItem,
	project *projects_models.Project,
) error {
	if len(validLogs) == 0 {
		return nil
	}

	if client := broker.Default(); client != nil {
		return s.publishValidLogs(client, validLogs, project.ID)
	}

	// Without the broker, straight into this replica's segment writer, as
	// spans and metrics are.
	if err := storeBatch(map[uuid.UUID][]*logs_core.LogItem{project.ID: validLogs}); err != nil {
		s.logger.Error("Failed to store logs",
			slog.String("projectId", project.ID.String()), slog.String("error", err.Error()))
		return fmt.Errorf("failed to store logs: %w", err)
	}
	return nil
}

// storeBatch routes AI tool events to their own stream and everything else
// to logs.
func storeBatch(batch map[uuid.UUID][]*logs_core.LogItem) error {
	logsBatch, aiBatch := logs_core.SplitAIEvents(batch)
	if len(logsBatch) > 0 {
		if err := logs_core.GetLogCoreRepository().StoreLogsBatch(logsBatch); err != nil {
			return err
		}
	}
	if len(aiBatch) > 0 {
		if err := logs_core.GetAIRepository().StoreLogsBatch(aiBatch); err != nil {
			return err
		}
	}
	return nil
}

// publishValidLogs returns once JetStream holds the batch, so an accepted
// response means the logs survive any single pod or broker loss.
func (s *LogReceivingService) publishValidLogs(
	client *broker.Client,
	validLogs []*logs_core.LogItem,
	projectID uuid.UUID,
) error {
	err := ingest.Publish(client, broker.SignalLogs, projectID, validLogs)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, broker.ErrIngestFull):
		s.logger.Warn("Logs ingest buffer is full", slog.String("projectId", projectID.String()))
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorIngestBufferFull,
			Message: "ingest buffer is full, retry later",
		}
	default:
		s.logger.Error("Failed to publish logs to the ingest buffer",
			slog.String("projectId", projectID.String()), slog.String("error", err.Error()))
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorIngestUnavailable,
			Message: "ingest buffer is unavailable, retry later",
		}
	}
}

func (s *LogReceivingService) validateBasicBatchLimits(request *SubmitLogsRequestDTO) error {
	if len(request.Logs) == 0 {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorBatchTooLarge,
			Message: "batch cannot be empty",
		}
	}

	if len(request.Logs) > MaxBatchSize {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorBatchTooLarge,
			Message: fmt.Sprintf("batch size cannot exceed %d logs", MaxBatchSize),
		}
	}

	return nil
}

func (s *LogReceivingService) validateBasicProjectConstraints(
	projectID uuid.UUID,
	origin, clientIP string,
) (*projects_models.Project, error) {
	project, err := s.projectService.GetProjectWithCache(projectID)
	if errors.Is(err, projects_services.ErrProjectNotFound) {
		return nil, &logs_core.ValidationError{
			Code:    logs_core.ErrorProjectNotFound,
			Message: "project not found",
		}
	}
	if err != nil {
		s.logger.Warn("Project lookup failed; logs refused as retryable",
			slog.String("projectId", projectID.String()), slog.String("error", err.Error()))
		return nil, &logs_core.ValidationError{
			Code:    logs_core.ErrorIngestUnavailable,
			Message: "project lookup is unavailable, retry later",
		}
	}

	if err := s.validateDomainFilter(project, origin); err != nil {
		return nil, err
	}

	if err := s.validateIPFilter(project, clientIP); err != nil {
		return nil, err
	}

	return project, nil
}

func (s *LogReceivingService) validateApiKey(project *projects_models.Project, apiKey string) error {
	if !project.IsApiKeyRequired {
		return nil
	}

	if apiKey == "" {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorAPIKeyRequired,
			Message: "API key required for this project",
		}
	}

	result, err := s.apiKeyService.ValidateApiKey(apiKey, project.ID)
	if err != nil {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorIngestUnavailable,
			Message: api_keys.LookupUnavailableMessage,
		}
	}

	if !result.IsValid {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorAPIKeyInvalid,
			Message: "invalid API key",
		}
	}

	return nil
}

func (s *LogReceivingService) validateDomainFilter(project *projects_models.Project, origin string) error {
	if !project.IsFilterByDomain {
		return nil
	}

	if origin == "" {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorDomainNotAllowed,
			Message: "origin header required for domain filtering",
		}
	}

	for _, allowedDomain := range project.AllowedDomains {
		if s.matchesDomain(origin, allowedDomain) {
			return nil
		}
	}

	return &logs_core.ValidationError{
		Code:    logs_core.ErrorDomainNotAllowed,
		Message: "domain not allowed",
	}
}

func (s *LogReceivingService) validateIPFilter(project *projects_models.Project, clientIP string) error {
	if !project.IsFilterByIP {
		return nil
	}

	if clientIP == "" {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorIPNotAllowed,
			Message: "client IP required for IP filtering",
		}
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorIPNotAllowed,
			Message: "invalid client IP format",
		}
	}

	for _, allowedIP := range project.AllowedIPs {
		if s.matchesIPOrCIDR(ip, allowedIP) {
			return nil
		}
	}

	return &logs_core.ValidationError{
		Code:    logs_core.ErrorIPNotAllowed,
		Message: "IP address not allowed",
	}
}

// takeRateLimit admits or refuses the batch's n items as one. A refusal is
// a 429 with Retry-After, which every sender retries; refusing items one by
// one inside a 200 made OTLP senders drop them.
func (s *LogReceivingService) takeRateLimit(project *projects_models.Project, n int) error {
	limit := project.EffectiveLogsPerSecondLimit()
	if limit == 0 || n == 0 {
		return nil
	}

	decision := s.rateLimiter.TakeBatch(project.ID, n, limit, limit*LogsBurstMultiplier)
	if decision.Allowed {
		return nil
	}

	telemetry.IngestThrottledRows.WithLabelValues(telemetry.SignalLogs).Add(float64(n))
	return &logs_core.ValidationError{
		Code:       logs_core.ErrorRateLimitExceeded,
		Message:    fmt.Sprintf("project rate limit of %d logs/s exceeded, retry later", limit),
		RetryAfter: decision.RetryAfter,
	}
}

func (s *LogReceivingService) validateLogItemWithSize(
	entry *LogItemRequestDTO,
	project *projects_models.Project,
	logSize int,
) error {
	if !entry.Level.IsValid() {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorInvalidLogLevel,
			Message: "invalid log level",
			Field:   "level",
		}
	}

	if strings.TrimSpace(entry.Message) == "" {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorMessageEmpty,
			Message: "message cannot be empty",
			Field:   "message",
		}
	}

	// MaxLogSizeKB <= 0 means unlimited, like the other per-project limits;
	// otherwise 0 would reject every log.
	if maxLogSizeKB := project.EffectiveMaxLogSizeKB(); maxLogSizeKB > 0 {
		maxSizeBytes := maxLogSizeKB * MaxLogSizeFactor
		if logSize > maxSizeBytes {
			return &logs_core.ValidationError{
				Code:    logs_core.ErrorLogTooLarge,
				Message: fmt.Sprintf("log size %d bytes exceeds maximum %d bytes", logSize, maxSizeBytes),
				Field:   "size",
			}
		}
	}

	if err := s.validateTimestamp(entry.Timestamp); err != nil {
		return err
	}

	return nil
}

func (s *LogReceivingService) validateTotalBatchSize(totalBatchSize int) error {
	if totalBatchSize > MaxBatchSizeBytes {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorBatchTooLarge,
			Message: fmt.Sprintf("batch size %d bytes exceeds maximum %d bytes", totalBatchSize, MaxBatchSizeBytes),
		}
	}

	return nil
}

func (s *LogReceivingService) calculateLogSize(entry *LogItemRequestDTO) (int, error) {
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}

	return len(jsonData), nil
}

func (s *LogReceivingService) matchesDomain(origin, allowedDomain string) bool {
	origin = strings.ToLower(origin)
	allowedDomain = strings.ToLower(allowedDomain)

	if strings.HasPrefix(allowedDomain, "*.") {
		domain := allowedDomain[2:]
		return strings.HasSuffix(origin, "."+domain) || origin == domain
	}

	return origin == allowedDomain
}

func (s *LogReceivingService) matchesIPOrCIDR(ip net.IP, allowedIP string) bool {
	_, cidr, err := net.ParseCIDR(allowedIP)
	if err == nil {
		return cidr.Contains(ip)
	}

	allowed := net.ParseIP(allowedIP)
	if allowed != nil {
		return ip.Equal(allowed)
	}

	return false
}

func (s *LogReceivingService) prettyFormatIfMessageJSON(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return message
	}

	var jsonData any
	if err := json.Unmarshal([]byte(message), &jsonData); err != nil {
		return message
	}

	prettyJSON, err := json.MarshalIndent(jsonData, "", "  ")
	if err != nil {
		return message
	}

	return string(prettyJSON)
}

func (s *LogReceivingService) validateTimestamp(timestamp any) error {
	if timestamp == nil {
		return nil
	}

	parsedTimestamp := time_parser.ParseTimestamp(timestamp)
	currentTime := time.Now().UTC()

	// Logs slightly in the future are normal (clock skew, libraries nudging equal
	// timestamps to keep order); more than 5 minutes ahead is a mistake.
	maxFutureOffset := 5 * time.Minute
	if parsedTimestamp.After(currentTime.Add(maxFutureOffset)) {
		return &logs_core.ValidationError{
			Code:    logs_core.ErrorFutureTimestamp,
			Message: "timestamp cannot be more than 5 minutes in the future",
			Field:   "timestamp",
		}
	}

	return nil
}

// normalizeLogLevel maps level names from various languages and frameworks
// to the standard levels.
func (s *LogReceivingService) normalizeLogLevel(level logs_core.LogLevel) logs_core.LogLevel {
	normalizedLevel := strings.ToUpper(strings.TrimSpace(string(level)))

	switch normalizedLevel {
	case "DEBUG", "TRACE", "VERBOSE", "SILLY":
		return logs_core.LogLevelDebug

	case "INFO", "INFORMATION", "NOTICE":
		return logs_core.LogLevelInfo

	case "WARN", "WARNING":
		return logs_core.LogLevelWarn

	case "ERROR", "ERR", "PANIC", "CRITICAL", "CRIT", "ALERT", "EMERG", "EMERGENCY":
		return logs_core.LogLevelError

	case "FATAL":
		return logs_core.LogLevelFatal

	default:
		// Unknown levels are kept, lowercased like every stored level.
		return logs_core.LogLevel(strings.ToLower(normalizedLevel))
	}
}

func (s *LogReceivingService) convertFieldValueToString(value any) string {
	switch v := value.(type) {
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%f", v)
	case float32:
		if v == float32(int32(v)) {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%f", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func withoutAIEvents(items []*logs_core.LogItem) []*logs_core.LogItem {
	result := make([]*logs_core.LogItem, 0, len(items))
	for _, item := range items {
		if !logs_core.IsAIEvent(item) {
			result = append(result, item)
		}
	}
	return result
}

// sampleLogs applies the project's log sampling rules: the first rule matching
// a log's service, level and pattern sets the rate, decided by trace id when present
// (so a trace's logs go together, as with trace sampling), else by log id.
func sampleLogs(logs []*logs_core.LogItem, rules projects_models.LogSampleRules) []*logs_core.LogItem {
	if len(rules) == 0 {
		return logs
	}
	kept := logs[:0:0]
	for _, log := range logs {
		service, _ := log.Fields["service_name"].(string)
		rate := rules.RateFor(service, string(log.Level), func() []string {
			return logs_core.PatternHashes(log.Message)
		})
		key := log.TraceID
		if key == "" {
			key = log.ID.String()
		}
		if rate >= 1 || obzly_sampling.Keep(key, rate) {
			kept = append(kept, log)
		}
	}
	telemetry.LogsSampledOut.Add(float64(len(logs) - len(kept)))
	return kept
}
