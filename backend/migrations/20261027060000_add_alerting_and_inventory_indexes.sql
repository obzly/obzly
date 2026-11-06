-- Indexes for foreign-key cascades and per-project lookups that scanned
-- whole tables (notification_outbox, alert rules and silences, inventory).
-- Built inside the migration's transaction, not CONCURRENTLY: the tables
-- are small apart from the outbox and the inventory change feed, a build
-- takes seconds there, and a failed CONCURRENTLY build would leave an
-- invalid index that IF NOT EXISTS then skips on the next run.

-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_notification_outbox_rule_id ON notification_outbox (rule_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_notification_outbox_channel_id ON notification_outbox (channel_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_alert_rules_project_id ON alert_rules (project_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_silences_project_ends_at ON silences (project_id, ends_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_alert_rule_channels_channel_id ON alert_rule_channels (channel_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_inventory_assets_project_node ON inventory_assets (project_id, node_key);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_inventory_changes_at ON inventory_changes (at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_inventory_changes_at;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_inventory_assets_project_node;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_alert_rule_channels_channel_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_silences_project_ends_at;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_alert_rules_project_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_notification_outbox_channel_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_notification_outbox_rule_id;
-- +goose StatementEnd
