package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func TestGeminiRelayHandsLongRunningPredictionToPluginRoute(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "veo-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "veotestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)

	engine := gin.New()
	SetRelayRouter(engine)
	source, err := plugins.Source("google")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	_, err = registry.RegisterFactory(source, jsplugin.Options{Key: "google"})
	require.NoError(t, err)
	// The plugin route authenticates as in production, then names itself.
	handlers := func(generation *jsplugin.RoutingGeneration, group jsplugin.RouteGroup) []gin.HandlerFunc {
		production := productionPluginRouteHandlers(generation, group)
		return []gin.HandlerFunc{production[0], production[1], production[2], func(c *gin.Context) {
			c.String(http.StatusOK, "plugin %s for user %d", group.Bindings[0].Plugin.Meta.Key, c.GetInt("id"))
		}}
	}
	require.NoError(t, registry.SetGenerationPreparer(newPluginGenerationBuilder(engine.Routes(), nil, handlers).prepare))
	require.Empty(t, registry.RoutingErrors())
	previousRegistry := defaultPluginDispatcher.registry
	defaultPluginDispatcher.registry = registry
	t.Cleanup(func() { defaultPluginDispatcher.registry = previousRegistry })
	engine.NoRoute(defaultPluginDispatcher.dispatch)

	for _, testCase := range []struct {
		method   string
		path     string
		toPlugin bool
	}{
		{method: http.MethodPost, path: "/v1beta/models/veo-3.1-generate-preview:predictLongRunning", toPlugin: true},
		{method: http.MethodGet, path: "/v1beta/models/veo-3.1-generate-preview/operations/task_sample", toPlugin: true},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(`{}`))
			request.Header.Set("x-goog-api-key", "veotestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if testCase.toPlugin {
				assert.Equal(t, fmt.Sprintf("plugin google for user %d", user.Id), recorder.Body.String())
				return
			}
			// The relay looks for a Gemini channel serving the model; there is none,
			// and the model is absent from the platform model catalog.
			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"model_not_listed"`)
		})
	}
}

// [FORK] 推理入口只拦截「显式下架」（目录里有命中行且 status != 1）的模型；目录里没
// 有该行的模型照旧放行。已上架但无可用渠道，必须落到「渠道问题」而不是「模型未上架」。
func TestRelayBlocksOnlyRetiredModels(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	user := model.User{Username: "listing-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "listingtestkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	require.NoError(t, model.DB.Create(&model.Model{ModelName: "gemini-2.5-flash", Status: 1, NameRule: model.NameRuleExact}).Error)
	// Status has gorm:"default:1", so a Create with Status:0 would fall back to the
	// DB default; unlist through an explicit UPDATE as the platform does.
	require.NoError(t, model.DB.Create(&model.Model{ModelName: "retired-model", Status: 1, NameRule: model.NameRuleExact}).Error)
	require.NoError(t, model.DB.Model(&model.Model{}).Where("model_name = ?", "retired-model").Update("status", 0).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	for _, testCase := range []struct {
		modelName string
		wantCode  string
		wantText  string
	}{
		{modelName: "gemini-2.5-flash", wantCode: "no_available_channel", wantText: "No available channel for model gemini-2.5-flash"},
		{modelName: "retired-model", wantCode: "model_not_listed", wantText: "Model retired-model is not offered by this platform"},
		{modelName: "never-catalogued", wantCode: "model_not_listed", wantText: "No available channel for model never-catalogued"},
	} {
		t.Run(testCase.modelName, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+testCase.modelName+":generateContent", strings.NewReader(`{}`))
			request.Header.Set("x-goog-api-key", "listingtestkey")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			body := recorder.Body.String()
			assert.Contains(t, body, `"code":"`+testCase.wantCode+`"`)
			// 「显式下架」的前置拦截与「已上架但无渠道」的选渠道失败共用 model_not_listed
			// 这一错误码，只有响应文案能区分两条路径：后者是 no_available_channel 文案，前者
			// 是 model_not_listed 文案。若删掉 Distribute 里的前置拦截，retired-model 会落到
			// 选渠道失败、得到「No available channel…」，本条断言随即失败。
			assert.Contains(t, body, testCase.wantText)
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	// The model catalog table must exist for failure paths that read it: an empty
	// catalog means "model not listed", a missing table would be a lookup error.
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}, &model.Model{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}
