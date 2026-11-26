package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupIsModelListedTest(t *testing.T) {
	t.Helper()
	originalDB := DB
	t.Cleanup(func() { DB = originalDB })
	var err error
	DB, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, DB.AutoMigrate(&Model{}))
}

func TestIsModelListedMatchesByNameRule(t *testing.T) {
	setupIsModelListedTest(t)

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

	assert.True(t, IsModelListed("gpt-4o"), "exact name matches")
	assert.True(t, IsModelListed("claude-3-5-sonnet"), "prefix rule matches")
	assert.True(t, IsModelListed("gpt-3.5-turbo"), "suffix rule matches")
	assert.True(t, IsModelListed("gemini-2.5-flash"), "contains rule matches")
	assert.False(t, IsModelListed("gpt-4o-mini"), "no rule matches")
	assert.False(t, IsModelListed("retired-model"), "metadata with status != 1 is not listed")
	assert.False(t, IsModelListed(""), "empty name is not listed")
}

func TestIsModelListedWithoutDatabase(t *testing.T) {
	originalDB := DB
	DB = nil
	t.Cleanup(func() { DB = originalDB })

	assert.False(t, IsModelListed("gpt-4o"))
}
