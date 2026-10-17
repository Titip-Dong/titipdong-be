-- +goose Up
CREATE INDEX shipments_auto_release_idx ON shipments (shipped_at) WHERE delivered_at IS NULL;

-- +goose Down
DROP INDEX shipments_auto_release_idx;
