package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type createUserResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Data    struct {
		Id float64 `json:"id"`
	} `json:"data"`
}

// createUserOnce 发一次建号请求。**不调用夹具** —— setupUserSearchControllerTest
// 每次都换一个新的 sqlite 库，所以夹具必须在每个用例开头只调一次，之后连发多个请求。
func createUserOnce(t *testing.T, identity service.AuthIdentity, body string) createUserResult {
	t.Helper()
	response := adminUserRequest(http.MethodPost, "/api/user/", body,
		"", identity, common.RoleRootUser, nil, CreateUser)
	var result createUserResult
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
	return result
}

// createBody 用 json.Marshal 拼 body：手机号用例要传真实的换行/制表符，
// 手工拼串会把它们写成非法 JSON（字面换行不能出现在 JSON 字符串里）。
func createBody(username, phone string) string {
	payload := map[string]any{
		"username": username,
		"password": "password-123",
		"role":     1,
		"phone":    phone,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func countUsers(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, model.DB.Model(&model.User{}).Count(&n).Error)
	return n
}

func TestCreateUserStoresElevenDigitPhone(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	result := createUserOnce(t, identity, createBody("phone-ok", "13956026852"))
	require.True(t, result.Success, "message=%s", result.Message)

	stored, err := model.GetUserById(int(result.Data.Id), false)
	require.NoError(t, err)
	require.NotNil(t, stored.Phone)
	assert.Equal(t, "13956026852", *stored.Phone)
}

// trim 后是 11 位就放行：BFF 与前端都会 trim，上游不能因为客户端没 trim 就拒绝。
func TestCreateUserTrimsPhoneBeforeValidation(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	result := createUserOnce(t, identity, createBody("phone-trim", "  13956026852\n"))
	require.True(t, result.Success, "message=%s", result.Message)

	stored, err := model.GetUserById(int(result.Data.Id), false)
	require.NoError(t, err)
	require.NotNil(t, stored.Phone)
	assert.Equal(t, "13956026852", *stored.Phone)
}

func TestCreateUserRejectsNonElevenDigitPhoneWithoutInsertingRow(t *testing.T) {
	for _, phone := range []string{"1234567890", "123456789012", "abc", "138-0013-8000", "１３９５６０２６８５２"} {
		t.Run(phone, func(t *testing.T) {
			_, identity := setupUserSearchControllerTest(t)
			before := countUsers(t)
			result := createUserOnce(t, identity, createBody("phone-bad", phone))
			assert.False(t, result.Success, "%s 应被拒", phone)
			assert.Equal(t, "PHONE_INVALID", result.Code)
			assert.EqualValues(t, before, countUsers(t), "被拒时 users 表不得新增行")
		})
	}
}

func TestCreateUserRejectsDuplicatePhoneWithoutInsertingRow(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	first := createUserOnce(t, identity, createBody("phone-first", "13800138000"))
	require.True(t, first.Success, "message=%s", first.Message)

	before := countUsers(t)
	second := createUserOnce(t, identity, createBody("phone-second", "13800138000"))
	assert.False(t, second.Success)
	assert.Equal(t, "PHONE_TAKEN", second.Code)
	assert.EqualValues(t, before, countUsers(t), "号码冲突时不得新增行")
}

// 自助注册、种子脚本都不带 phone：必须照常成功，号码为 NULL。
func TestCreateUserWithoutPhoneSucceeds(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	result := createUserOnce(t, identity, `{"username":"phone-none","password":"password-123","role":1}`)
	require.True(t, result.Success, "message=%s", result.Message)

	stored, err := model.GetUserById(int(result.Data.Id), false)
	require.NoError(t, err)
	assert.Nil(t, stored.Phone)
}

// 空串与纯空白等价于「没有手机号」，不能落成 ''（会占掉唯一索引）。
func TestCreateUserTreatsBlankPhoneAsNull(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	for i, phone := range []string{"", "   ", "\t\n"} {
		result := createUserOnce(t, identity, createBody("phone-blank"+strconv.Itoa(i), phone))
		require.True(t, result.Success, "phone=%q message=%s", phone, result.Message)
		stored, err := model.GetUserById(int(result.Data.Id), false)
		require.NoError(t, err)
		assert.Nil(t, stored.Phone, "phone=%q 应落 NULL", phone)
	}
}

// 部门校验失败必须仍然优先于手机号写入，且不新增行（回归：新增的预检不能改变既有事务语义）。
func TestCreateUserUnknownDepartmentStillRejectsBeforePhone(t *testing.T) {
	_, identity := setupUserSearchControllerTest(t)
	before := countUsers(t)
	result := createUserOnce(t, identity,
		`{"username":"phone-ghost-dept","password":"password-123","role":1,"department_id":999999,"phone":"13700137000"}`)
	assert.False(t, result.Success)
	assert.Equal(t, "Department not found", result.Message)
	assert.EqualValues(t, before, countUsers(t), "部门被拒时不得新增行")
}

func TestNormalizePhoneFork(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n", "　"} {
		got, err := normalizePhoneFork(raw)
		require.NoError(t, err, "raw=%q", raw)
		assert.Nil(t, got, "raw=%q 应归一为 nil", raw)
	}
	got, err := normalizePhoneFork(" 13956026852 ")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "13956026852", *got)

	for _, raw := range []string{"1234567890", "123456789012", "abc", "１３９５６０２６８５２"} {
		_, err := normalizePhoneFork(raw)
		assert.ErrorIs(t, err, ErrInvalidPhoneFork, "raw=%q 应报格式错误", raw)
	}
}
