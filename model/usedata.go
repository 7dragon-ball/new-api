package model

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// QuotaData 柱状图数据
type QuotaData struct {
	Id                  int    `json:"id"`
	UserID              int    `json:"user_id" gorm:"index"`
	Username            string `json:"username" gorm:"index:idx_qdt_model_user_name,priority:2;size:64;default:''"`
	ModelName           string `json:"model_name" gorm:"index:idx_qdt_model_user_name,priority:1;size:64;default:''"`
	CreatedAt           int64  `json:"created_at" gorm:"bigint;index:idx_qdt_created_at,priority:2"`
	UseGroup            string `json:"use_group" gorm:"index;size:64;default:''"`
	TokenID             int    `json:"token_id" gorm:"index;default:0"`
	ChannelID           int    `json:"channel_id" gorm:"index;default:0"`
	NodeName            string `json:"node_name" gorm:"index;size:64;default:''"`
	TokenUsed           int    `json:"token_used" gorm:"default:0"`
	Count               int    `json:"count" gorm:"default:0"`
	Quota               int    `json:"quota" gorm:"default:0"`
	CacheReadTokens     int64  `json:"cache_read_tokens" gorm:"default:0"`
	CacheCreationTokens int64  `json:"cache_creation_tokens" gorm:"default:0"`
	// [FORK] 身份快照，与 Username 同一性质：用户/部门都是硬删，产生用量的那一刻抄一份。
	// type:text 取源列最宽的一侧，免得比源列先截断而让整条用量写入失败。
	// 不带 default:''（MySQL 不允许 TEXT 带字面默认值，ERROR 1101）：没有 default tag，
	// GORM 就会把零值写进 INSERT，新行一律是空串，聚合键用 = '' 比较。
	DisplayName string `json:"display_name" gorm:"type:text"`
	Department  string `json:"department" gorm:"type:text"`
}

type QuotaDataLogParams struct {
	UserID              int
	Username            string
	DisplayName         string
	Department          string
	ModelName           string
	Quota               int
	CreatedAt           int64
	TokenUsed           int
	CacheReadTokens     int64
	CacheCreationTokens int64
	UseGroup            string
	TokenID             int
	ChannelID           int
	NodeName            string
}

func UpdateQuotaData() {
	for {
		if common.DataExportEnabled {
			common.SysLog(common.LogText("updating dashboard data..."))
			SaveQuotaDataCache()
		}
		time.Sleep(time.Duration(common.DataExportInterval) * time.Minute)
	}
}

var CacheQuotaData = make(map[string]*QuotaData)
var CacheQuotaDataLock = sync.Mutex{}

// [FORK] quotaDataKeyPart 是 quota_data 聚合键的一列。
type quotaDataKeyPart struct {
	Column string
	Value  any
}

// [FORK] quotaDataKeyParts 是聚合键的唯一定义：这些列全相同才累加进同一行。
// 内存分桶键与两条 upsert 判据都由它生成，三处各写一遍时漏一列不报错，
// 只会「内存两行、库里一行」。身份两列在键里，改名前后各是一行，历史不会被覆盖。
func quotaDataKeyParts(quotaData *QuotaData) []quotaDataKeyPart {
	return []quotaDataKeyPart{
		{"user_id", quotaData.UserID},
		{"username", quotaData.Username},
		{"display_name", quotaData.DisplayName},
		{"department", quotaData.Department},
		{"model_name", quotaData.ModelName},
		{"created_at", quotaData.CreatedAt},
		{"use_group", quotaData.UseGroup},
		{"token_id", quotaData.TokenID},
		{"channel_id", quotaData.ChannelID},
		{"node_name", quotaData.NodeName},
	}
}

// [FORK] quotaDataCacheKey 把聚合键拼成内存分桶键，用 \x00 分隔，避免两组值撞成同一个键。
func quotaDataCacheKey(quotaData *QuotaData) string {
	var builder strings.Builder
	for i, part := range quotaDataKeyParts(quotaData) {
		if i > 0 {
			builder.WriteByte(0)
		}
		fmt.Fprintf(&builder, "%v", part.Value)
	}
	return builder.String()
}

// [FORK] applyQuotaDataKey 把聚合键铺成 upsert 的 WHERE 条件。
// 两个快照列在存量行里是 NULL（AutoMigrate 加列），新行是空串（GORM 写零值）；
// 不统一就匹配不到旧行，部署后首次保存会给同一聚合键再插一行，旧行从此不再累加。
// COALESCE 在 SQLite / MySQL / PostgreSQL 上语义一致，这两列也没有索引可依赖。
func applyQuotaDataKey(tx *gorm.DB, quotaData *QuotaData) *gorm.DB {
	for _, part := range quotaDataKeyParts(quotaData) {
		switch part.Column {
		case "display_name", "department":
			tx = tx.Where("COALESCE("+part.Column+", '') = ?", part.Value)
		default:
			tx = tx.Where(part.Column+" = ?", part.Value)
		}
	}
	return tx
}

func logQuotaDataCache(quotaData *QuotaData) {
	key := quotaDataCacheKey(quotaData)
	count := quotaData.Count
	quota := quotaData.Quota
	tokenUsed := quotaData.TokenUsed
	cachedQuotaData, ok := CacheQuotaData[key]
	if ok {
		cachedQuotaData.Count += count
		cachedQuotaData.Quota += quota
		cachedQuotaData.TokenUsed += tokenUsed
		cachedQuotaData.CacheReadTokens += quotaData.CacheReadTokens
		cachedQuotaData.CacheCreationTokens += quotaData.CacheCreationTokens
		quotaData = cachedQuotaData
	}
	CacheQuotaData[key] = quotaData
}

func LogQuotaData(params QuotaDataLogParams) {
	// 只精确到小时
	createdAt := params.CreatedAt - (params.CreatedAt % 3600)
	quotaData := &QuotaData{
		UserID:              params.UserID,
		Username:            params.Username,
		DisplayName:         params.DisplayName,
		Department:          params.Department,
		ModelName:           params.ModelName,
		CreatedAt:           createdAt,
		UseGroup:            params.UseGroup,
		TokenID:             params.TokenID,
		ChannelID:           params.ChannelID,
		NodeName:            params.NodeName,
		Count:               1,
		Quota:               params.Quota,
		TokenUsed:           params.TokenUsed,
		CacheReadTokens:     max(0, params.CacheReadTokens),
		CacheCreationTokens: max(0, params.CacheCreationTokens),
	}

	CacheQuotaDataLock.Lock()
	defer CacheQuotaDataLock.Unlock()
	logQuotaDataCache(quotaData)
}

func SaveQuotaDataCache() {
	CacheQuotaDataLock.Lock()
	defer CacheQuotaDataLock.Unlock()
	size := len(CacheQuotaData)
	// 如果缓存中有数据，就保存到数据库中
	// 1. 先查询数据库中是否有数据
	// 2. 如果有数据，就更新数据
	// 3. 如果没有数据，就插入数据
	for _, quotaData := range CacheQuotaData {
		quotaDataDB := &QuotaData{}
		// 判据与内存键同源，见 quotaDataKeyParts。
		applyQuotaDataKey(DB.Table("quota_data"), quotaData).First(quotaDataDB)
		if quotaDataDB.Id > 0 {
			//quotaDataDB.Count += quotaData.Count
			//quotaDataDB.Quota += quotaData.Quota
			//DB.Table("quota_data").Save(quotaDataDB)
			increaseQuotaData(quotaData)
		} else {
			DB.Table("quota_data").Create(quotaData)
		}
	}
	CacheQuotaData = make(map[string]*QuotaData)
	common.SysLog(common.LogText("saved dashboard data, %d records", size))
}

func increaseQuotaData(quotaData *QuotaData) {
	err := applyQuotaDataKey(DB.Table("quota_data"), quotaData).
		Updates(map[string]any{
			"count":                 gorm.Expr("count + ?", quotaData.Count),
			"quota":                 gorm.Expr("quota + ?", quotaData.Quota),
			"token_used":            gorm.Expr("token_used + ?", quotaData.TokenUsed),
			"cache_read_tokens":     gorm.Expr("cache_read_tokens + ?", quotaData.CacheReadTokens),
			"cache_creation_tokens": gorm.Expr("cache_creation_tokens + ?", quotaData.CacheCreationTokens),
		}).Error
	if err != nil {
		common.SysLog(common.LogText("increaseQuotaData error: %s", err))
	}
}

func GetQuotaDataByUsername(username string, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	err = DB.Table("quota_data").
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, sum(cache_read_tokens) as cache_read_tokens, sum(cache_creation_tokens) as cache_creation_tokens").
		Where("username = ? and created_at >= ? and created_at <= ?", username, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetQuotaDataByUserId(userId int, startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	err = DB.Table("quota_data").
		Select("user_id, username, model_name, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, sum(cache_read_tokens) as cache_read_tokens, sum(cache_creation_tokens) as cache_creation_tokens").
		Where("user_id = ? and created_at >= ? and created_at <= ?", userId, startTime, endTime).
		Group("user_id, username, model_name, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetQuotaDataGroupByUser(startTime int64, endTime int64) (quotaData []*QuotaData, err error) {
	var quotaDatas []*QuotaData
	err = DB.Table("quota_data").
		Select("username, created_at, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, sum(cache_read_tokens) as cache_read_tokens, sum(cache_creation_tokens) as cache_creation_tokens").
		Where("created_at >= ? and created_at <= ?", startTime, endTime).
		Group("username, created_at").
		Find(&quotaDatas).Error
	return quotaDatas, err
}

func GetAllQuotaDates(startTime int64, endTime int64, username string) (quotaData []*QuotaData, err error) {
	if username != "" {
		return GetQuotaDataByUsername(username, startTime, endTime)
	}
	var quotaDatas []*QuotaData
	// 从quota_data表中查询数据
	// only select model_name, sum(count) as count, sum(quota) as quota, model_name, created_at from quota_data group by model_name, created_at;
	//err = DB.Table("quota_data").Where("created_at >= ? and created_at <= ?", startTime, endTime).Find(&quotaDatas).Error
	err = DB.Table("quota_data").Select("model_name, sum(count) as count, sum(quota) as quota, sum(token_used) as token_used, sum(cache_read_tokens) as cache_read_tokens, sum(cache_creation_tokens) as cache_creation_tokens, created_at").Where("created_at >= ? and created_at <= ?", startTime, endTime).Group("model_name, created_at").Find(&quotaDatas).Error
	return quotaDatas, err
}
