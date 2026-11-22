package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 模型上架页新增 / 删除供应关系的端点。
func TestChannelAbilityEndpointAddAndDelete(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	channel := &model.Channel{
		Name: "supply-crud", Key: "test-key", Models: "existing-model", Group: "default",
		Status: common.ChannelStatusEnabled, Priority: common.GetPointer(int64(4)),
	}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))

	call := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(method, "/api/channel/ability", strings.NewReader(body))
		switch method {
		case http.MethodPost:
			AddChannelAbility(ctx)
		case http.MethodDelete:
			DeleteChannelAbility(ctx)
		default:
			t.Fatalf("unsupported method %s", method)
		}
		return recorder
	}

	added := call(http.MethodPost, fmt.Sprintf(`{"model":"brand-new","channel_id":%d,"upstream_model":"upstream-new"}`, channel.Id))
	require.Equal(t, http.StatusOK, added.Code, added.Body.String())
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Model         string `json:"model"`
			ChannelId     int    `json:"channel_id"`
			UpstreamModel string `json:"upstream_model"`
			Priority      int64  `json:"priority"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(added.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.Equal(t, "brand-new", response.Data.Model)
	assert.Equal(t, channel.Id, response.Data.ChannelId)
	assert.Equal(t, "upstream-new", response.Data.UpstreamModel)
	assert.Equal(t, int64(1), response.Data.Priority, "priority defaults to 1")

	var row model.Ability
	require.NoError(t, db.Where("`group` = ? AND model = ? AND channel_id = ?", "default", "brand-new", channel.Id).First(&row).Error)
	require.NotNil(t, row.Priority)
	assert.Equal(t, int64(1), *row.Priority)

	// Duplicate add is a 400, not a silent success.
	dup := call(http.MethodPost, fmt.Sprintf(`{"model":"brand-new","channel_id":%d}`, channel.Id))
	assert.Equal(t, http.StatusBadRequest, dup.Code)
	assert.Contains(t, dup.Body.String(), `"message_key":"The supply relation already exists"`)

	deleted := call(http.MethodDelete, fmt.Sprintf(`{"model":"brand-new","channel_id":%d}`, channel.Id))
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	var count int64
	require.NoError(t, db.Model(&model.Ability{}).Where("model = ? AND channel_id = ?", "brand-new", channel.Id).Count(&count).Error)
	assert.Zero(t, count)
	stored, err := model.GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"existing-model"}, stored.GetModels())
}

func TestChannelAbilityEndpointValidation(t *testing.T) {
	setupModelListControllerTestDB(t)
	call := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(method, "/api/channel/ability", strings.NewReader(body))
		if method == http.MethodPost {
			AddChannelAbility(ctx)
		} else {
			DeleteChannelAbility(ctx)
		}
		return recorder
	}

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, body := range []string{
			`{}`,
			`{"model":"m"}`,
			`{"model":"m","channel_id":0}`,
			`not-json`,
		} {
			assert.Equal(t, http.StatusBadRequest, call(method, body).Code, "%s %s", method, body)
		}
	}

	// 优先级越界：400；渠道不存在：404
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPost, `{"model":"m","channel_id":1,"priority":0}`).Code)
	assert.Equal(t, http.StatusNotFound, call(http.MethodPost, `{"model":"m","channel_id":999999}`).Code)
	missing := call(http.MethodDelete, `{"model":"m","channel_id":999999}`)
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), `"message_key":"Channel does not exist"`)
}

// [FORK] 写后立即生效，不等 3 秒 sync。
func TestAddChannelAbilityRefreshesRoutingCache(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	previousMemoryCache := common.MemoryCacheEnabled
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	common.MemoryCacheEnabled = true

	channel := &model.Channel{Name: "supply-refresh", Key: "k", Models: "base", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	model.InitChannelCache()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/ability",
		strings.NewReader(fmt.Sprintf(`{"model":"fresh-model","channel_id":%d}`, channel.Id)))
	AddChannelAbility(ctx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	picked, err := model.GetRandomSatisfiedChannel("default", "fresh-model", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, picked, "端点返回后缓存已刷新，新供应关系立即可选路")
	assert.Equal(t, channel.Id, picked.Id)
}
