-- Named personal tokens for the remote API (/api/v1): the houston CLI with
-- --server, or an agent. Only a token's SHA-256 is kept; the token itself
-- is shown once, when it's issued.
-- +goose Up
CREATE TABLE api_tokens (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 50),
  token_digest TEXT NOT NULL UNIQUE,
  last_used_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE api_tokens;
