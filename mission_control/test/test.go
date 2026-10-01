// Package test is what the app's tests share: a database of their own,
// migrated (test/testapp: the app on one). Fixtures go in test/fixtures/<table>.yml
// (testkit.Fixtures).
package test

import (
	"testing"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/testkit"

	"github.com/scttymn/houston/mission_control/db/migrations"
)

// DB is an empty, migrated database, removed after the test.
func DB(t testing.TB) *db.DB { return testkit.DB(t, migrations.Up) }
