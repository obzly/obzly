package aiusage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// commonPricesAsOf dates the starting list: list prices change, and Obzly
// doesn't fetch them, so users review each one.
const commonPricesAsOf = "2026-10-27"

const (
	maxModelPrices     = 500
	maxPricePerMillion = 100_000
	seenWindow         = 24 * time.Hour
)

// ModelPrice is USD per million tokens for a model name: exact, or a prefix
// ending in "*". Cached input defaults to the input price when unset.
type ModelPrice struct {
	ID                    uuid.UUID `json:"id"                    gorm:"column:id;primaryKey"`
	ProjectID             uuid.UUID `json:"projectId"             gorm:"column:project_id"`
	Model                 string    `json:"model"                 gorm:"column:model"`
	InputPerMillion       float64   `json:"inputPerMillion"       gorm:"column:input_per_million"`
	OutputPerMillion      float64   `json:"outputPerMillion"      gorm:"column:output_per_million"`
	CachedInputPerMillion *float64  `json:"cachedInputPerMillion" gorm:"column:cached_input_per_million"`
	CreatedAt             time.Time `json:"createdAt"             gorm:"column:created_at"`
	UpdatedAt             time.Time `json:"updatedAt"             gorm:"column:updated_at"`
}

func (ModelPrice) TableName() string { return "ai_model_prices" }

type ModelPriceRequest struct {
	Model                 string   `json:"model"`
	InputPerMillion       float64  `json:"inputPerMillion"`
	OutputPerMillion      float64  `json:"outputPerMillion"`
	CachedInputPerMillion *float64 `json:"cachedInputPerMillion"`
}

type ModelPriceRowDTO struct {
	ModelPrice
	// SeenCalls: LLM calls in the last 24 h this price applies to (-1 when
	// the spans couldn't be read).
	SeenCalls int64 `json:"seenCalls"`
}

type SeenModelDTO struct {
	Model string `json:"model"`
	Calls int64  `json:"calls"`
}

type ModelPricesResponseDTO struct {
	Prices []ModelPriceRowDTO `json:"prices"`
	// UnpricedModels were seen in LLM calls in the last 24 h without a price.
	UnpricedModels []SeenModelDTO `json:"unpricedModels"`
	SeenError      string         `json:"seenError,omitempty"`
}

type FillCommonPricesResponseDTO struct {
	Added  int      `json:"added"`
	AsOf   string   `json:"asOf"`
	Models []string `json:"models"`
}

// commonPrices is a short starting list of public list prices (USD per 1M
// tokens; standard tier, text, prompts up to 200k tokens), checked against
// the providers' pricing pages on commonPricesAsOf.
var commonPrices = []ModelPriceRequest{
	{Model: "claude-opus-4-1*", InputPerMillion: 15, OutputPerMillion: 75, CachedInputPerMillion: ptr(1.5)},
	{Model: "claude-sonnet-4-5*", InputPerMillion: 3, OutputPerMillion: 15, CachedInputPerMillion: ptr(0.3)},
	{Model: "claude-haiku-4-5*", InputPerMillion: 1, OutputPerMillion: 5, CachedInputPerMillion: ptr(0.1)},
	{Model: "gpt-4.1*", InputPerMillion: 2, OutputPerMillion: 8, CachedInputPerMillion: ptr(0.5)},
	{Model: "gpt-4.1-mini*", InputPerMillion: 0.4, OutputPerMillion: 1.6, CachedInputPerMillion: ptr(0.1)},
	{Model: "gpt-4o-mini*", InputPerMillion: 0.15, OutputPerMillion: 0.6, CachedInputPerMillion: ptr(0.075)},
	{Model: "gemini-2.5-pro*", InputPerMillion: 1.25, OutputPerMillion: 10, CachedInputPerMillion: ptr(0.125)},
	{Model: "gemini-2.5-flash*", InputPerMillion: 0.3, OutputPerMillion: 2.5, CachedInputPerMillion: ptr(0.03)},
}

var ErrNotFound = errors.New("not found")

func (s *AIUsageService) ListModelPrices(ctx context.Context, projectID uuid.UUID) (*ModelPricesResponseDTO, error) {
	prices, err := loadPrices(projectID)
	if err != nil {
		return nil, err
	}
	response := &ModelPricesResponseDTO{
		Prices:         make([]ModelPriceRowDTO, 0, len(prices)),
		UnpricedModels: []SeenModelDTO{},
	}

	now := time.Now().UTC()
	seen, seenErr := seenModels(ctx, projectID, now.Add(-seenWindow), now)
	calls := map[uuid.UUID]int64{}
	if seenErr != nil {
		response.SeenError = "Couldn't read the last 24 hours of LLM calls"
	}
	for _, model := range seen {
		if price := matchPrice(prices, model.Model); price != nil {
			calls[price.ID] += model.Calls
		} else {
			response.UnpricedModels = append(response.UnpricedModels, model)
		}
	}
	for _, price := range prices {
		row := ModelPriceRowDTO{ModelPrice: *price, SeenCalls: calls[price.ID]}
		if seenErr != nil {
			row.SeenCalls = -1
		}
		response.Prices = append(response.Prices, row)
	}
	return response, nil
}

func (s *AIUsageService) CreateModelPrice(
	projectID uuid.UUID, request ModelPriceRequest, userID uuid.UUID,
) (*ModelPrice, error) {
	if err := validatePrice(&request); err != nil {
		return nil, err
	}
	var count int64
	if err := storage.GetDb().Model(&ModelPrice{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count >= maxModelPrices {
		return nil, fmt.Errorf("%w: at most %d model prices", ErrInvalidRequest, maxModelPrices)
	}
	if exists, err := priceExists(projectID, request.Model, uuid.Nil); err != nil || exists {
		return nil, duplicatePrice(err, request.Model)
	}
	now := time.Now().UTC()
	price := &ModelPrice{ID: uuid.New(), ProjectID: projectID, CreatedAt: now, UpdatedAt: now}
	applyPrice(price, request)
	if err := storage.GetDb().Create(price).Error; err != nil {
		return nil, fmt.Errorf("failed to save model price: %w", err)
	}
	s.audit.WriteAuditLog("AI model price added: "+price.Model, &userID, &projectID)
	return price, nil
}

func (s *AIUsageService) UpdateModelPrice(
	projectID, priceID uuid.UUID, request ModelPriceRequest, userID uuid.UUID,
) (*ModelPrice, error) {
	if err := validatePrice(&request); err != nil {
		return nil, err
	}
	var price ModelPrice
	if err := storage.GetDb().Where("project_id = ? AND id = ?", projectID, priceID).First(&price).Error; err != nil {
		return nil, notFound(err)
	}
	if exists, err := priceExists(projectID, request.Model, priceID); err != nil || exists {
		return nil, duplicatePrice(err, request.Model)
	}
	applyPrice(&price, request)
	price.UpdatedAt = time.Now().UTC()
	if err := storage.GetDb().Save(&price).Error; err != nil {
		return nil, fmt.Errorf("failed to save model price: %w", err)
	}
	s.audit.WriteAuditLog("AI model price changed: "+price.Model, &userID, &projectID)
	return &price, nil
}

func (s *AIUsageService) DeleteModelPrice(projectID, priceID uuid.UUID, userID uuid.UUID) error {
	result := storage.GetDb().Where("project_id = ? AND id = ?", projectID, priceID).Delete(&ModelPrice{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	s.audit.WriteAuditLog("AI model price deleted: "+priceID.String(), &userID, &projectID)
	return nil
}

// FillCommonPrices adds the starting list's models the project has no
// price for yet; existing prices are left as they are.
func (s *AIUsageService) FillCommonPrices(projectID uuid.UUID, userID uuid.UUID) (*FillCommonPricesResponseDTO, error) {
	prices, err := loadPrices(projectID)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, price := range prices {
		have[strings.ToLower(price.Model)] = true
	}
	response := &FillCommonPricesResponseDTO{AsOf: commonPricesAsOf, Models: []string{}}
	now := time.Now().UTC()
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		for _, common := range commonPrices {
			if have[strings.ToLower(common.Model)] {
				continue
			}
			price := &ModelPrice{ID: uuid.New(), ProjectID: projectID, CreatedAt: now, UpdatedAt: now}
			applyPrice(price, common)
			if err := tx.Create(price).Error; err != nil {
				return err
			}
			response.Models = append(response.Models, price.Model)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to add model prices: %w", err)
	}
	response.Added = len(response.Models)
	if response.Added > 0 {
		s.audit.WriteAuditLog(fmt.Sprintf("AI model prices: added %d common models (list as of %s)",
			response.Added, commonPricesAsOf), &userID, &projectID)
	}
	return response, nil
}

// matchPrice is the price for model: an exact name first, else the longest
// matching prefix. Case-insensitive.
func matchPrice(prices []*ModelPrice, model string) *ModelPrice {
	model = strings.ToLower(model)
	var best *ModelPrice
	bestLength := -1
	for _, price := range prices {
		name := strings.ToLower(price.Model)
		if name == model {
			return price
		}
		if prefix, ok := strings.CutSuffix(name, "*"); ok && strings.HasPrefix(model, prefix) &&
			len(prefix) > bestLength {
			best, bestLength = price, len(prefix)
		}
	}
	return best
}

// cost of one call's tokens. Cached tokens are part of the input tokens
// (GenAI semantic conventions), billed at the cached price.
func (p *ModelPrice) cost(input, output, cached float64) float64 {
	cachedPrice := p.InputPerMillion
	if p.CachedInputPerMillion != nil {
		cachedPrice = *p.CachedInputPerMillion
	}
	cached = math.Min(cached, input)
	return ((input-cached)*p.InputPerMillion + cached*cachedPrice + output*p.OutputPerMillion) / 1e6
}

func loadPrices(projectID uuid.UUID) ([]*ModelPrice, error) {
	var prices []*ModelPrice
	err := storage.GetDb().Where("project_id = ?", projectID).Order("lower(model)").Find(&prices).Error
	return prices, err
}

func seenModels(ctx context.Context, projectID uuid.UUID, from, to time.Time) ([]SeenModelDTO, error) {
	rows, err := queryLLM(ctx, projectID, from, to, 0,
		`SELECT model, count(*)::BIGINT FROM llm WHERE model <> '' GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 500`)
	if err != nil {
		return nil, err
	}
	seen := make([]SeenModelDTO, 0, len(rows))
	for _, r := range rows {
		seen = append(seen, SeenModelDTO{Model: toString(r[0]), Calls: toInt(r[1])})
	}
	sort.SliceStable(seen, func(i, j int) bool { return seen[i].Calls > seen[j].Calls })
	return seen, nil
}

func validatePrice(request *ModelPriceRequest) error {
	request.Model = strings.TrimSpace(request.Model)
	switch {
	case request.Model == "" || len(request.Model) > 200:
		return fmt.Errorf("%w: a model name of up to 200 characters is required", ErrInvalidRequest)
	case strings.Contains(strings.TrimSuffix(request.Model, "*"), "*"):
		return fmt.Errorf("%w: '*' is only allowed at the end, as a prefix match", ErrInvalidRequest)
	}
	for _, value := range []*float64{&request.InputPerMillion, &request.OutputPerMillion, request.CachedInputPerMillion} {
		if value != nil && (math.IsNaN(*value) || *value < 0 || *value > maxPricePerMillion) {
			return fmt.Errorf("%w: prices must be between 0 and %d USD per million tokens",
				ErrInvalidRequest, maxPricePerMillion)
		}
	}
	return nil
}

func applyPrice(price *ModelPrice, request ModelPriceRequest) {
	price.Model, price.InputPerMillion, price.OutputPerMillion = request.Model, request.InputPerMillion, request.OutputPerMillion
	price.CachedInputPerMillion = request.CachedInputPerMillion
}

func priceExists(projectID uuid.UUID, model string, except uuid.UUID) (bool, error) {
	var count int64
	err := storage.GetDb().Model(&ModelPrice{}).
		Where("project_id = ? AND lower(model) = lower(?) AND id <> ?", projectID, model, except).Count(&count).Error
	return count > 0, err
}

func duplicatePrice(err error, model string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s already has a price", ErrInvalidRequest, model)
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func ptr(value float64) *float64 {
	return &value
}
