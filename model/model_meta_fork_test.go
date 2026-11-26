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

// IsModelListed builds a cross-database WHERE clause, so the precedence rules are
// verified on real MySQL and PostgreSQL when TEST_MYSQL_DSN / TEST_POSTGRES_DSN
// are set, not only on SQLite.
func TestIsModelListedMatchesByNameRule(t *testing.T) {
	for _, dialect := range []struct {
		kind string
		env  string
		open func(dsn string) gorm.Dialector
	}{
		{kind: "sqlite", open: func(string) gorm.Dialector { return sqlite.Open(filepath.Join(t.TempDir(), "models.db")) }},
		{kind: "mysql", env: "TEST_MYSQL_DSN", open: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{kind: "postgres", env: "TEST_POSTGRES_DSN", open: func(dsn string) gorm.Dialector { return postgres.Open(dsn) }},
	} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip("set " + dialect.env + " to run this database")
				}
			}
			db, err := gorm.Open(dialect.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

			previousDB := DB
			previousType := common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect.kind))
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&Model{}))
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
			})
			require.NoError(t, db.AutoMigrate(&Model{}))

			versionQuery := "SELECT version()"
			if dialect.kind == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("%s version: %s", dialect.kind, version)

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
			// A broader enabled prefix rule, so the retired exact entry must still shadow it
			// to stay consistent with the public catalog (resolveModelMetadata precedence).
			require.NoError(t, DB.Create(&Model{ModelName: "retired-", Status: 1, NameRule: NameRulePrefix}).Error)

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
