package move

import (
	"context"
	"database/sql"
	"errors"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
)

// moveInstallation moves the one installations row (none on a server that
// never finished setup).
func moveInstallation(ctx context.Context, r *rails, tx *db.Tx) (int, error) {
	var (
		base, zone, dns, account, zoneID, tunnelID, release, releaseURL sql.NullString
		apiToken, tunnelToken                                           sql.NullString
		connected, checked, cleanup, created, updated                   sql.NullString
		portOpen                                                        bool
	)
	err := r.db.QueryRowContext(ctx, `SELECT base_domain, time_zone, dns_mode, port_open, cloudflare_account_id,
		cloudflare_zone_id, cloudflare_api_token, cloudflare_connected_at, tunnel_id, tunnel_token, latest_release,
		latest_release_url, latest_release_checked_at, registry_cleanup_since, created_at, updated_at
		FROM installations ORDER BY id LIMIT 1`).Scan(&base, &zone, &dns, &portOpen, &account,
		&zoneID, &apiToken, &connected, &tunnelID, &tunnelToken, &release,
		&releaseURL, &checked, &cleanup, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	plainAPIToken, err := r.decrypt(apiToken)
	if err != nil {
		return 0, err
	}
	plainTunnelToken, err := r.decrypt(tunnelToken)
	if err != nil {
		return 0, err
	}
	times := make([]sql.NullTime, 5)
	for i, s := range []sql.NullString{connected, checked, cleanup, created, updated} {
		if times[i], err = railsTime(s); err != nil {
			return 0, err
		}
	}
	if zone.String == "" {
		zone.String = "UTC"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO installations (id, base_domain, time_zone, dns_mode, port_open,
		cloudflare_account_id, cloudflare_zone_id, cloudflare_api_token, cloudflare_connected_at, tunnel_id,
		tunnel_token, latest_release, latest_release_url, latest_release_checked_at, registry_cleanup_since,
		created_at, updated_at) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		base.String, zone.String, dns.String, portOpen, account.String, zoneID.String,
		crypt.Of(plainAPIToken), times[0], tunnelID.String, crypt.Of(plainTunnelToken),
		release.String, releaseURL.String, times[1], times[2], times[3].Time, times[4].Time)
	if err != nil {
		return 0, err
	}
	return 1, nil
}
