package logs_core

import (
	"errors"
	"fmt"
)

// MaxTopGroups bounds AggregationRequestDTO.TopGroups: each group is one value
// of an IN filter and one series on a chart.
const MaxTopGroups = 50

// ErrInvalidAggregation wraps a refused aggregation request (a 400).
var ErrInvalidAggregation = errors.New("invalid aggregation request")

// ValidateOptions checks the measure, top-groups and band options.
func (r *AggregationRequestDTO) ValidateOptions() error {
	if !r.Measure.IsValid() {
		return fmt.Errorf("%w: unknown measure %q", ErrInvalidAggregation, r.Measure)
	}
	if !r.Measure.IsCount() && r.MeasureField == "" {
		return fmt.Errorf("%w: measure %q needs a measureField", ErrInvalidAggregation, r.Measure)
	}
	if r.TopGroups < 0 || r.TopGroups > MaxTopGroups {
		return fmt.Errorf("%w: topGroups must be between 0 and %d", ErrInvalidAggregation, MaxTopGroups)
	}
	if r.TopGroups > 0 && r.GroupField == "" {
		return fmt.Errorf("%w: topGroups needs a groupField", ErrInvalidAggregation)
	}
	switch r.GroupBands {
	case "":
	case GroupBandsLog:
		if r.GroupField == "" {
			return fmt.Errorf("%w: groupBands needs a numeric groupField", ErrInvalidAggregation)
		}
		if r.TopGroups > 0 {
			return fmt.Errorf("%w: groupBands and topGroups can't be combined", ErrInvalidAggregation)
		}
	default:
		return fmt.Errorf("%w: unknown groupBands %q", ErrInvalidAggregation, r.GroupBands)
	}
	return nil
}

// UsesCountsOnly reports whether the request is a plain line count, which
// per-minute counts and the partition cache can answer.
func (r *AggregationRequestDTO) UsesCountsOnly() bool {
	return r.Measure.IsCount() && r.GroupBands == ""
}
