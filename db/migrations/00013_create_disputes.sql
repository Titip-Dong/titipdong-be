-- +goose Up
CREATE TABLE disputes (
    id            UUID PRIMARY KEY,
    order_id      UUID NOT NULL UNIQUE REFERENCES orders (id),
    opened_by     UUID NOT NULL REFERENCES users (id),
    category      TEXT NOT NULL CHECK (category IN ('not_received', 'not_as_described', 'damaged', 'other')),
    description   TEXT NOT NULL CHECK (char_length(description) <= 2000),
    status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    decision      TEXT CHECK (decision IN ('refund', 'release')),
    decision_note TEXT CHECK (char_length(decision_note) <= 2000),
    decided_by    UUID REFERENCES users (id),
    decided_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'resolved') = (decision IS NOT NULL AND decided_by IS NOT NULL AND decided_at IS NOT NULL))
);

CREATE INDEX disputes_queue_idx ON disputes (status, created_at, id);

CREATE TABLE dispute_evidence (
    id          UUID PRIMARY KEY,
    dispute_id  UUID NOT NULL REFERENCES disputes (id),
    uploaded_by UUID NOT NULL REFERENCES users (id),
    file_key    TEXT NOT NULL,
    note        TEXT CHECK (char_length(note) <= 500),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX dispute_evidence_dispute_idx ON dispute_evidence (dispute_id);

CREATE TABLE dispute_messages (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dispute_id UUID NOT NULL REFERENCES disputes (id),
    author_id  UUID NOT NULL REFERENCES users (id),
    body       TEXT NOT NULL CHECK (char_length(body) <= 2000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX dispute_messages_dispute_idx ON dispute_messages (dispute_id, id);

-- +goose Down
DROP TABLE dispute_messages;
DROP TABLE dispute_evidence;
DROP TABLE disputes;
