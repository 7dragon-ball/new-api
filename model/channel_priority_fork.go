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
	// [FORK] 先锁渠道行再写 abilities 行，与 UpdateAbilities 的重建串行化（锁序
	// 「channel 行 → abilities 行」，与 AddAbility / DeleteAbility 一致）。否则重建的
	// 「读旧值 → 删 → 插」会把这次写入覆盖回旧值，或在删行的瞬间让本次 UPDATE 影响
	// 0 行、误报 404。关系定位与那两个入口共用 resolveAbilityRelation：group 用渠道
	// 原始 token、model trim，否则多 group 渠道或带空白的模型名会查不到同一行。
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel, err := lockAbilityChannel(tx, channelId)
		if err != nil {
			return err
		}
		rawGroup, resolvedModel, ok := resolveAbilityRelation(channel, group, modelName)
		if !ok {
			return ErrAbilityGroupNotFound
		}
		query := func() *gorm.DB {
			return abilityRowQuery(tx, rawGroup, resolvedModel, channelId)
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
		return nil
	})
	if err != nil {
		return err
	}
	BumpSyncTopic(SyncTopicChannels)
	return nil
}
