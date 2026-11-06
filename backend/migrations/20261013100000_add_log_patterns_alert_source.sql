-- +goose Up
-- +goose StatementBegin
-- Alert rules on message patterns (log_patterns): new or spiking patterns
-- against a baseline window.
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM alert_rules WHERE source = 'log_patterns';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd
