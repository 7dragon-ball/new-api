package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAbortWithOpenAiMessageRecordsRelayErrorLog(t *testing.T) {
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

	require.NoError(t, database.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)

	newAbortContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		ctx.Set("id", 7)
		ctx.Set("token_name", "test-token")
		ctx.Set("token_id", 11)
		ctx.Set("original_model", "gpt-test")
		ctx.Set("group", "default")
		return ctx, recorder
	}

	ctx, recorder := newAbortContext()
	abortWithOpenAiMessage(ctx, http.StatusForbidden, "the token cannot use this model", types.ErrorCodeAccessDenied)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), string(types.ErrorCodeAccessDenied))

	logs, total, err := model.GetUserLogs(7, model.LogFilter{Type: model.LogTypeError}, 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, "gpt-test", logs[0].ModelName)
	assert.Equal(t, 0, logs[0].ChannelId)
	storedOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.Equal(t, string(types.ErrorCodeAccessDenied), storedOther["error_code"])
	assert.Equal(t, float64(http.StatusForbidden), storedOther["status_code"])

	// The recorded error log is governed by the ErrorLogEnabled switch.
	constant.ErrorLogEnabled = false
	ctx, _ = newAbortContext()
	abortWithOpenAiMessage(ctx, http.StatusForbidden, "the token cannot use this model", types.ErrorCodeAccessDenied)
	_, total, err = model.GetUserLogs(7, model.LogFilter{Type: model.LogTypeError}, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "an aborted request must not be recorded while error logs are disabled")
}
