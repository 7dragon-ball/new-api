package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 必须用真实量级的 Unix 秒：LogQuotaData 按小时截断，太小的值截成 0 会被 GORM 的
// autoCreateTime 当成未设置覆盖掉，upsert 判据就永远匹配不到已插入行。
const testQuotaTS = 1795162654

// resetQuotaDataState 清空内存缓存与表数据。
// CacheQuotaData 是包级全局变量，不清理会污染同包其它用例。
func resetQuotaDataState(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&QuotaData{}))
	reset := func() {
		CacheQuotaDataLock.Lock()
		CacheQuotaData = make(map[string]*QuotaData)
		CacheQuotaDataLock.Unlock()
		_ = DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&QuotaData{}).Error
	}
	reset()
	t.Cleanup(reset)
}

// 显示名与部门必须进聚合键：不进的话改名后新旧记录会被合并，历史被改后的名字覆盖。
func TestQuotaDataAggregatesByDisplayName(t *testing.T) {
	resetQuotaDataState(t)

	// 同 user / 同模型 / 同小时，显示名不同 → 必须是两行
	LogQuotaData(QuotaDataLogParams{UserID: 1, Username: "u", DisplayName: "旧名", ModelName: "m", CreatedAt: testQuotaTS, Quota: 1})
	LogQuotaData(QuotaDataLogParams{UserID: 1, Username: "u", DisplayName: "新名", ModelName: "m", CreatedAt: testQuotaTS, Quota: 2})

	CacheQuotaDataLock.Lock()
	cached := len(CacheQuotaData)
	CacheQuotaDataLock.Unlock()
	assert.Equal(t, 2, cached, "内存缓存也必须是两行，与库里一致")

	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Where("user_id = ?", 1).Find(&rows).Error)
	require.Len(t, rows, 2, "显示名不同必须是两行，不能合并")
	// 落库遍历 map，顺序不定，按名字比对集合而不是行号
	names := map[string]bool{}
	for _, row := range rows {
		names[row.DisplayName] = true
	}
	assert.True(t, names["旧名"] && names["新名"], "两行应分别带各自的显示名快照，实际：%v", names)
}

// 部门同理：同一人调到别的部门后，新旧用量不能被合并（否则历史归属被改写）。
func TestQuotaDataAggregatesByDepartment(t *testing.T) {
	resetQuotaDataState(t)

	LogQuotaData(QuotaDataLogParams{UserID: 2, Username: "v", Department: "技术部", ModelName: "m", CreatedAt: testQuotaTS, Quota: 1})
	LogQuotaData(QuotaDataLogParams{UserID: 2, Username: "v", Department: "市场部", ModelName: "m", CreatedAt: testQuotaTS, Quota: 2})
	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Where("user_id = ?", 2).Order("id").Find(&rows).Error)
	require.Len(t, rows, 2, "部门不同必须是两行，不能合并")
}

// 反向保障：只测「拆成两行」抓不到「键被改坏、永远合不上」，相同的用量仍必须累加。
func TestQuotaDataMergesIdenticalRows(t *testing.T) {
	resetQuotaDataState(t)

	params := QuotaDataLogParams{UserID: 3, Username: "w", DisplayName: "王五", Department: "技术部", ModelName: "m", CreatedAt: testQuotaTS}
	first := params
	first.Quota = 1
	second := params
	second.Quota = 2
	LogQuotaData(first)
	LogQuotaData(second)

	CacheQuotaDataLock.Lock()
	cached := len(CacheQuotaData)
	CacheQuotaDataLock.Unlock()
	assert.Equal(t, 1, cached, "同一份用量在内存里必须累加成一行")

	// 落库两次，第二次应走累加而不是新插一行
	SaveQuotaDataCache()
	LogQuotaData(first)
	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Where("user_id = ?", 3).Find(&rows).Error)
	require.Len(t, rows, 1, "同一份用量在库里必须累加成一行")
	assert.Equal(t, 4, rows[0].Quota, "1 + 2 + 1 必须累加进同一行")
	assert.Equal(t, 3, rows[0].Count)
}

// 回归：升级窗口。新快照列是 AutoMigrate 加的，存量行里是 NULL，而新行写的是空串；
// 判据若直接按空串比较，NULL 永远匹配不上，首次保存会给同一聚合键再插一行，
// 旧行从此不再累加，“相同聚合键只有一行”的不变量被打破。
func TestQuotaDataMergesLegacyNullRows(t *testing.T) {
	resetQuotaDataState(t)
	createdAt := testQuotaTS - testQuotaTS%3600
	// 旧形状行：display_name / department 为 NULL，其余聚合键与下面写入的用量一致。
	require.NoError(t, DB.Exec(`INSERT INTO quota_data
		(user_id, username, display_name, department, model_name, created_at, use_group,
		 token_id, channel_id, node_name, token_used, count, quota, cache_read_tokens, cache_creation_tokens)
		VALUES (4, 'legacy', NULL, NULL, 'm', ?, '', 0, 0, '', 0, 1, 5, 0, 0)`, createdAt).Error)

	LogQuotaData(QuotaDataLogParams{UserID: 4, Username: "legacy", ModelName: "m", CreatedAt: testQuotaTS, Quota: 1})
	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Where("user_id = ?", 4).Find(&rows).Error)
	require.Len(t, rows, 1, "旧 NULL 行必须被匹配并累加，不能再插一行")
	assert.Equal(t, 6, rows[0].Quota)
	assert.Equal(t, 2, rows[0].Count)
	assert.Equal(t, "", rows[0].DisplayName, "旧行的 NULL 不改写，读出仍是空串，语义是“当时没记”")
	assert.Equal(t, "", rows[0].Department)
}
