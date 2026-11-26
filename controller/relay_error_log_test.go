package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newErrorLogTestDB installs a fresh in-memory SQLite database as both the main and
// the log database for the duration of the test, turns error logging on, and seeds
// the user row the test records logs for. The previous globals are restored on cleanup.
func newErrorLogTestDB(t *testing.T, userId int, username string) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: userId, Username: username, Group: "default"}).Error)
	return database
}

func TestProcessChannelErrorUsesSnapshotWithoutLeakingChannelMetadata(t *testing.T) {
	database := newErrorLogTestDB(t, 7, "log-owner")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 7)
	ctx.Set("username", "log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 11)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	ctx.Set("channel_id", 202)
	ctx.Set("channel_name", "mutable-context-channel")
	ctx.Set("channel_type", 9)
	ctx.Set("use_channel", []string{"101"})
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	channelSnapshot := types.ChannelError{
		ChannelId:   101,
		ChannelType: 1,
		ChannelName: "snapshot-channel",
		AutoBan:     false,
	}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	processChannelError(ctx, channelSnapshot, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, channelSnapshot.ChannelId, stored.ChannelId)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadGateway), storedOther["status_code"])
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, storedOther, key)
	}
	adminInfo, ok := storedOther["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"101"}, adminInfo["use_channel"])

	logs, total, err := model.GetUserLogs(7, model.LogFilter{Type: model.LogTypeError}, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, channelSnapshot.ChannelId, logs[0].ChannelId)
	assert.Empty(t, logs[0].ChannelName)
	userOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.NotContains(t, userOther, "admin_info")
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, userOther, key)
	}
}

func TestForceRecordRelayErrorLogBypassesSuppression(t *testing.T) {
	database := newErrorLogTestDB(t, 8, "fallback-owner")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 8)
	ctx.Set("username", "fallback-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 12)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	// A pre-consume failure opts out of ordinary error logging.
	suppressed := types.NewErrorWithStatusCode(
		errors.New("quota exhausted"),
		types.ErrorCodeInsufficientUserQuota,
		http.StatusForbidden,
		types.ErrOptionWithNoRecordErrorLog(),
	)

	service.RecordRelayErrorLog(ctx, 0, suppressed, nil)
	var count int64
	require.NoError(t, database.Model(&model.Log{}).Count(&count).Error)
	require.Zero(t, count, "a suppressed error must not be recorded by RecordRelayErrorLog")

	service.ForceRecordRelayErrorLog(ctx, 0, suppressed, nil)
	require.NoError(t, database.Model(&model.Log{}).Count(&count).Error)
	require.Equal(t, int64(1), count, "ForceRecordRelayErrorLog must bypass the suppression option")

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, "gpt-test", stored.ModelName)
	assert.Equal(t, string(types.ErrorCodeInsufficientUserQuota), storedOther["error_code"])
	assert.Equal(t, float64(http.StatusForbidden), storedOther["status_code"])
}

// A quota failure whose text was localized for the reader must still be recorded in
// the fixed English sentence, while the error object the response is built from keeps
// the reader's text.
func TestForceRecordRelayErrorLogRecordsLocalizedQuotaErrorAsEnglish(t *testing.T) {
	database := newErrorLogTestDB(t, 9, "localized-owner")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Accept-Language", "zh-CN")
	ctx.Set("id", 9)
	ctx.Set("username", "localized-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 13)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	// What billing_session.go hands over for a zh-CN request: reader-language text
	// plus the suppression option that makes RecordRelayErrorLog skip it.
	const localizedText = "预扣费额度失败，用户剩余额度：1，需要预扣费额度：2"
	localized := types.NewErrorWithStatusCode(
		errors.New(localizedText),
		types.ErrorCodeInsufficientUserQuota,
		http.StatusForbidden,
		types.ErrOptionWithNoRecordErrorLog(),
	)

	service.ForceRecordRelayErrorLog(ctx, 0, localized, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, "status_code=403, Insufficient user quota", stored.Content)
	assert.Equal(t, localizedText, localized.Error(), "the error the response is built from keeps the reader's text")
}

// A model-price failure is localized where the text is built, not at the call
// site, so it cannot be normalized by error code like a quota failure. The fixed
// English sentence travels on the error object itself (types.ErrOptionWithLogMessage)
// and survives the types.NewError wrapper PrepareRequestBilling applies.
func TestForceRecordRelayErrorLogRecordsLocalizedModelPriceErrorAsEnglish(t *testing.T) {
	require.NoError(t, i18n.Init())
	database := newErrorLogTestDB(t, 10, "price-owner")

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"localized-price-test":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"localized-price-test":"tier(\"base\", u(\"seconds\") * 0.4)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("Accept-Language", "zh-CN")
	ctx.Set("id", 10)
	ctx.Set("username", "price-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 14)
	ctx.Set("original_model", "localized-price-test")
	ctx.Set("group", "default")
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	info := &relaycommon.RelayInfo{
		OriginModelName: "localized-price-test",
		UserGroup:       "default",
		UsingGroup:      "default",
	}
	_, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.Error(t, err, "a task usage expression must be rejected as a pricing failure")
	// PrepareRequestBilling wraps the pricing failure exactly like this.
	apiErr := types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))

	service.ForceRecordRelayErrorLog(ctx, 0, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, "status_code=400, Model localized-price-test uses a task usage expression; configure ordinary relay pricing and separate plugin billing expressions", stored.Content)
	assert.Equal(t, "模型 localized-price-test 使用了任务用量表达式；请为普通转发配置常规定价，并为插件单独配置计费表达式", apiErr.Error(), "the error the response is built from keeps the reader's text")
}
