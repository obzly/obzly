-- +goose Up
-- +goose StatementBegin
CREATE TABLE alert_rule_activity (
    rule_id       UUID NOT NULL,
    day           DATE NOT NULL,
    fired         INT  NOT NULL DEFAULT 0,
    notifications INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (rule_id, day)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rule_activity
    ADD CONSTRAINT fk_alert_rule_activity_rule_id
    FOREIGN KEY (rule_id)
    REFERENCES alert_rules (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE alert_rule_activity;
-- +goose StatementEnd
