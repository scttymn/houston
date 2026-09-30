-- Who signs in (gantry's auth, on the tables Rails 8's generator makes: the
-- Rails app's users move with their bcrypt hashes), and their sessions: a
-- random token's digest, bound to how it was made (through the tunnel or
-- not). Sessions don't move: everyone signs in again at the switch.
-- +goose Up
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  email_address TEXT NOT NULL UNIQUE,
  password_digest TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE sessions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  token_digest TEXT NOT NULL UNIQUE,
  ip_address TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  bound_to TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL,
  last_seen_at DATETIME NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
