-- +goose Up
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          VARCHAR(16) NOT NULL CHECK (role IN ('buyer', 'jastiper', 'admin')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_lower_key ON users (LOWER(email));

-- +goose Down
DROP TABLE users;
