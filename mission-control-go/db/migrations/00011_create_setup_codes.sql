-- First run's one-time code: only its bcrypt digest. The installer prints
-- a new one (`mission-control-go setup-code`) until the admin exists, and
-- making the admin uses it up. Nothing moves: a server switched from the
-- Rails app has its admin already.
-- +goose Up
CREATE TABLE setup_codes (
  id INTEGER PRIMARY KEY,
  code_digest TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE setup_codes;
