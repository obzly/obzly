package aiusage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_MatchPrice_ExactBeatsPrefixAndLongestPrefixWins(t *testing.T) {
	prices := []*ModelPrice{
		{Model: "gpt-4.1*", InputPerMillion: 2},
		{Model: "gpt-4.1-mini*", InputPerMillion: 0.4},
		{Model: "gpt-4.1-mini-2025-04-14", InputPerMillion: 0.3},
		{Model: "claude-sonnet-4-5*", InputPerMillion: 3},
	}

	assert.Equal(t, 0.3, matchPrice(prices, "gpt-4.1-mini-2025-04-14").InputPerMillion)
	assert.Equal(t, 0.4, matchPrice(prices, "gpt-4.1-mini-2025-05-01").InputPerMillion)
	assert.Equal(t, 2.0, matchPrice(prices, "gpt-4.1-2025-04-14").InputPerMillion)
	assert.Equal(t, 3.0, matchPrice(prices, "Claude-Sonnet-4-5-20250929").InputPerMillion, "case-insensitive")
	assert.Nil(t, matchPrice(prices, "mistral-large-latest"))
	assert.Nil(t, matchPrice(prices, "gpt-4"), "a prefix only matches names that start with it")
}

func Test_ModelPriceCost_BillsCachedInputAtItsPrice(t *testing.T) {
	cached := 0.3
	price := &ModelPrice{InputPerMillion: 3, OutputPerMillion: 15, CachedInputPerMillion: &cached}

	assert.InDelta(t, (600_000*3+400_000*0.3+100_000*15)/1e6, price.cost(1_000_000, 100_000, 400_000), 1e-9)

	price.CachedInputPerMillion = nil
	assert.InDelta(t, 3.0, price.cost(1_000_000, 0, 400_000), 1e-9, "unset cached price is the input price")
}

func Test_ValidatePrice_AllowsOnlyATrailingWildcard(t *testing.T) {
	require.NoError(t, validatePrice(&ModelPriceRequest{Model: " claude-sonnet-4-5* ", InputPerMillion: 3}))
	assert.ErrorIs(t, validatePrice(&ModelPriceRequest{Model: "claude-*-4-5"}), ErrInvalidRequest)
	assert.ErrorIs(t, validatePrice(&ModelPriceRequest{Model: "gpt", InputPerMillion: -1}), ErrInvalidRequest)
	assert.ErrorIs(t, validatePrice(&ModelPriceRequest{Model: ""}), ErrInvalidRequest)
}

func Test_FinishReasons_ReadsArrayOrString(t *testing.T) {
	assert.Equal(t, []string{"length"}, finishReasons(`["length"]`))
	assert.Equal(t, []string{"stop"}, finishReasons("stop"))
	assert.Equal(t, []string{}, finishReasons(""))
}
