-- name: InstallationBaseDomain :one
-- The base domain setup chose ("" before it did); no row before setup.
SELECT base_domain FROM installations WHERE id = 1;
