-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_budgets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID             NOT NULL,
    name                TEXT             NOT NULL,
    amount_usd          DOUBLE PRECISION NOT NULL,
    period              TEXT             NOT NULL,
    time_zone           TEXT             NOT NULL DEFAULT 'UTC',
    scope               TEXT             NOT NULL,
    scope_value         TEXT             NOT NULL DEFAULT '',
    thresholds          JSONB            NOT NULL DEFAULT '[80, 100]',
    channel_ids         JSONB            NOT NULL DEFAULT '[]',
    include_claude_code BOOLEAN          NOT NULL DEFAULT TRUE,
    include_llm_apps    BOOLEAN          NOT NULL DEFAULT FALSE,
    created_by          UUID,
    created_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_budgets
    ADD CONSTRAINT fk_ai_budgets_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_budgets
    ADD CONSTRAINT fk_ai_budgets_created_by
    FOREIGN KEY (created_by)
    REFERENCES users (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_budgets
    ADD CONSTRAINT chk_ai_budgets_values
    CHECK (amount_usd > 0
        AND period IN ('day', 'week', 'month')
        AND scope IN ('project', 'each_user', 'each_model', 'user', 'model'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_ai_budgets_project_id ON ai_budgets (project_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE ai_model_prices (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id               UUID             NOT NULL,
    model                    TEXT             NOT NULL,
    input_per_million        DOUBLE PRECISION NOT NULL,
    output_per_million       DOUBLE PRECISION NOT NULL,
    cached_input_per_million DOUBLE PRECISION,
    created_at               TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_model_prices
    ADD CONSTRAINT fk_ai_model_prices_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_model_prices
    ADD CONSTRAINT uq_ai_model_prices_project_model UNIQUE (project_id, model);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE ai_model_prices
    ADD CONSTRAINT chk_ai_model_prices_values
    CHECK (input_per_million >= 0 AND output_per_million >= 0
        AND (cached_input_per_million IS NULL OR cached_input_per_million >= 0));
-- +goose StatementEnd

-- +goose StatementBegin
-- Budgets keep a managed alert rule each (source ai_cost).
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns', 'ai_cost')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM alert_rules WHERE source = 'ai_cost';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS ai_model_prices;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS ai_budgets;
-- +goose StatementEnd
