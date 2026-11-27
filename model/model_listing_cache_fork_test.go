package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// listingCandidateNames covers every name-rule outcome plus the retired/empty
// edge cases the database version is tested against.
var listingCandidateNames = []string{
	"gpt-4o",
	"claude-3-5-sonnet",
	"gpt-3.5-turbo",
	"gemini-2.5-flash",
	"gpt-4o-mini",
	"retired-model",
	"retired-2025",
	"",
}

func listingTestDialects() []struct{ kind, env string } {
	return []struct{ kind, env string }{
		{kind: "sqlite"},
		{kind: "mysql", env: "TEST_MYSQL_DSN"},
		{kind: "postgres", env: "TEST_POSTGRES_DSN"},
	}
}

// setupModelListingTestDB points the package's global DB at a fresh database and
// drops the published snapshot on cleanup so tests never read each other's
// catalog. It mirrors setupRelayRouterTestDB's per-dialect handling.
func setupModelListingTestDB(t *testing.T, kind, dsn string) {
	t.Helper()

	var open func(string) gorm.Dialector
	switch kind {
	case "sqlite":
		open = func(string) gorm.Dialector { return sqlite.Open(filepath.Join(t.TempDir(), "models.db")) }
	case "mysql":
		open = func(d string) gorm.Dialector { return mysql.Open(d) }
	case "postgres":
		open = func(d string) gorm.Dialector { return postgres.Open(d) }
	default:
		t.Fatalf("unknown dialect %q", kind)
	}

	db, err := gorm.Open(open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseType(kind))
	initCol()

	t.Cleanup(func() {
		require.NoError(t, db.Migrator().DropTable(&Model{}))
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		initCol()
		modelListingCatalogPtr.Store(nil)
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, db.AutoMigrate(&Model{}))
}

// seedListingFixture inserts the same admin-curated catalog as
// TestIsModelListedMatchesByNameRule: one row per rule type, plus a retired
// exact entry that must shadow a broader enabled prefix rule.
func seedListingFixture(t *testing.T) {
	t.Helper()
	for _, record := range []struct {
		modelName string
		rule      int
	}{
		{"gpt-4o", NameRuleExact},
		{"claude-", NameRulePrefix},
		{"-turbo", NameRuleSuffix},
		{"gemini", NameRuleContains},
	} {
		require.NoError(t, DB.Create(&Model{ModelName: record.modelName, Status: 1, NameRule: record.rule}).Error)
	}
	require.NoError(t, DB.Create(&Model{ModelName: "retired-model", Status: 1, NameRule: NameRuleExact}).Error)
	require.NoError(t, DB.Model(&Model{}).Where("model_name = ?", "retired-model").Update("status", 0).Error)
	require.NoError(t, DB.Create(&Model{ModelName: "retired-", Status: 1, NameRule: NameRulePrefix}).Error)
}

// assertCacheMatchesDatabase is the differential check: whatever the cached
// snapshot decides, the database version must decide the same thing.
func assertCacheMatchesDatabase(t *testing.T) {
	t.Helper()
	for _, name := range listingCandidateNames {
		want, err := IsModelListed(name)
		require.NoError(t, err)
		got, err := IsModelListedCached(name)
		require.NoError(t, err)
		assert.Equal(t, want, got, "cached listing verdict must match the database for %q", name)

		wantRetired, err := IsModelRetired(name)
		require.NoError(t, err)
		gotRetired, err := IsModelRetiredCached(name)
		require.NoError(t, err)
		assert.Equal(t, wantRetired, gotRetired, "cached retired verdict must match the database for %q", name)
		// A name is at most one of listed / retired; a name in no rule is neither.
		assert.False(t, want && wantRetired, "%q cannot be both listed and retired", name)
	}
}

func TestModelListingCacheMatchesDatabase(t *testing.T) {
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
			seedListingFixture(t)
			require.NoError(t, InitModelListingCache())
			assertCacheMatchesDatabase(t)
		})
	}
}

func TestModelListingCacheFallsBackWithoutSnapshot(t *testing.T) {
	setupModelListingTestDB(t, "sqlite", "")
	seedListingFixture(t)

	// No snapshot published (startup race / failed warm-up): the cache must read
	// the database instead of treating every model as unlisted.
	modelListingCatalogPtr.Store(nil)
	assertCacheMatchesDatabase(t)
}

func TestModelListingCacheRetiredOnlyForDisabledRows(t *testing.T) {
	setupModelListingTestDB(t, "sqlite", "")
	seedListingFixture(t)
	require.NoError(t, InitModelListingCache())

	for _, testCase := range []struct {
		name        string
		wantListed  bool
		wantRetired bool
	}{
		{"gpt-4o", true, false},            // enabled exact
		{"claude-3-5-sonnet", true, false}, // enabled prefix rule
		{"gemini-2.5-flash", true, false},  // enabled contains rule
		{"gpt-3.5-turbo", true, false},     // enabled suffix rule
		{"retired-model", false, true},     // disabled exact shadows the enabled retired- prefix
		{"retired-2025", true, false},      // covered by the enabled retired- prefix
		{"never-catalogued", false, false}, // no rule: neither listed nor retired
		{"", false, false},
	} {
		listed, err := IsModelListedCached(testCase.name)
		require.NoError(t, err)
		retired, err := IsModelRetiredCached(testCase.name)
		require.NoError(t, err)
		assert.Equal(t, testCase.wantListed, listed, "listed(%q)", testCase.name)
		assert.Equal(t, testCase.wantRetired, retired, "retired(%q)", testCase.name)
	}
}

func TestModelListingCacheRefreshesOnReinit(t *testing.T) {
	setupModelListingTestDB(t, "sqlite", "")
	seedListingFixture(t)
	require.NoError(t, InitModelListingCache())

	listed, err := IsModelListedCached("gpt-4o")
	require.NoError(t, err)
	assert.True(t, listed, "the listed exact model is served from the snapshot")

	// Unlist the model: the published snapshot must stay stale until it is rebuilt.
	require.NoError(t, DB.Model(&Model{}).Where("model_name = ?", "gpt-4o").Update("status", 0).Error)
	listed, err = IsModelListedCached("gpt-4o")
	require.NoError(t, err)
	assert.True(t, listed, "the snapshot only changes when it is rebuilt")

	require.NoError(t, InitModelListingCache())
	listed, err = IsModelListedCached("gpt-4o")
	require.NoError(t, err)
	assert.False(t, listed, "rebuilding the snapshot picks up the unlist")
}
