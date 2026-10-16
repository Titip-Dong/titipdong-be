-- +goose Up
CREATE TABLE audit_logs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id    UUID REFERENCES users (id),
    actor_type  TEXT NOT NULL CHECK (actor_type IN ('buyer', 'jastiper', 'admin', 'system')),
    action      TEXT NOT NULL CHECK (char_length(action) <= 64),
    target_type TEXT NOT NULL CHECK (char_length(target_type) <= 32),
    target_id   TEXT NOT NULL CHECK (char_length(target_id) <= 64),
    metadata    JSONB NOT NULL DEFAULT '{}',
    request_id  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((actor_type = 'system') = (actor_id IS NULL))
);

CREATE INDEX audit_logs_target_idx ON audit_logs (target_type, target_id, id);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_id, id) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC, id DESC);

CREATE TRIGGER audit_logs_append_only BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose Down
DROP TABLE audit_logs;
