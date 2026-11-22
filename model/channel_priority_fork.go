package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// [FORK] 按供应关系设优先级：console 里手工填的小整数区间，与 maxSortablePriority
// （渠道级重排用的无损排序区间）是两套口径，不要合并。
const (
	minAbilityPriority int64 = 1
	maxAbilityPriority int64 = 999
)

// [FORK] 两条错误都以 message_key 的形式到达前端，因此它们的文本是 locale 文件里的 key。
var ErrAbilityPriorityRange = common.NewMessage("Priority must be between 1 and 999")
var ErrAbilityPriorityNotFound = common.NewMessage("The supply relation does not exist")

// [FORK] UpdateAbilityPriority 只改一条供应关系——(group, model, channelId) 对应的
// abilities 行的 priority，其它行原样不动。与 ReorderChannelPriority 的区别是它不写
// channels 行，渠道在其它模型/分组下仍保持自己的优先级。
func UpdateAbilityPriority(modelName string, channelId int, group string, priority int64) error {
	if priority < minAbilityPriority || priority > maxAbilityPriority {
		return ErrAbilityPriorityRange
	}
	if group == "" {
		group = "default"
	}
	query := func() *gorm.DB {
		return DB.Model(&Ability{}).Where(commonGroupCol+" = ? AND model = ? AND channel_id = ?", group, modelName, channelId)
	}
	result := query().Update("priority", priority)
	if result.Error != nil {
		return result.Error
	}
	// 未变化的写入在 MySQL 上同样报 0 行，所以先补一次计数再回答「不存在」。
	if result.RowsAffected == 0 {
		var count int64
		if err := query().Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrAbilityPriorityNotFound
		}
	}
	BumpSyncTopic(SyncTopicChannels)
	return nil
}
