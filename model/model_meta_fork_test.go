package model

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IsModelListed builds a cross-database WHERE clause, so the precedence rules are
// verified on real MySQL and PostgreSQL when TEST_MYSQL_DSN / TEST_POSTGRES_DSN
// are set, not only on SQLite. The dialect harness and catalog fixture are shared
// with the listing-cache tests (listingTestDialects / setupModelListingTestDB /
// seedListingFixture), so the two suites cannot drift apart.
func TestIsModelListedMatchesByNameRule(t *testing.T) {
	for _, dialect := range listingTestDialects() {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip("set " + dialect.env + " to run this database")
				}
			}
			setupModelListingTestDB(t, dialect.kind, dsn)

			versionQuery := "SELECT version()"
			if dialect.kind == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, DB.Raw(versionQuery).Scan(&version).Error)
			t.Logf("%s version: %s", dialect.kind, version)

			seedListingFixture(t)

			for _, tc := range []struct {
				modelName string
				listed    bool
				reason    string
			}{
				{"gpt-4o", true, "exact name matches"},
				{"claude-3-5-sonnet", true, "prefix rule matches"},
				{"gpt-3.5-turbo", true, "suffix rule matches"},
				{"gemini-2.5-flash", true, "contains rule matches"},
				{"gpt-4o-mini", false, "no rule matches"},
				{"retired-model", false, "a retired exact entry shadows the broader enabled prefix rule"},
				{"retired-2025", true, "only the prefix rule applies"},
				{"", false, "empty name is not listed"},
			} {
				listed, err := IsModelListed(tc.modelName)
				require.NoError(t, err)
				assert.Equal(t, tc.listed, listed, tc.reason)
			}
		})
	}
}

func TestIsModelListedWithoutDatabase(t *testing.T) {
	originalDB := DB
	DB = nil
	t.Cleanup(func() { DB = originalDB })

	listed, err := IsModelListed("gpt-4o")
	require.NoError(t, err)
	assert.False(t, listed)
}
