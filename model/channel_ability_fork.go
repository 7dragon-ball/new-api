package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// [FORK] 模型上架页「添加 / 删除供应关系」的错误。与优先级写接口同一种写法：
// 用 common.NewMessage 让 message 同时是前端 locale 文件里的 key（message_key）。
var (
	ErrAbilityAlreadyExists   = common.NewMessage("The supply relation already exists")
	ErrAbilityChannelNotFound = common.NewMessage("Channel does not exist")
	ErrAbilityGroupNotFound   = common.NewMessage("The channel does not provide this group")
	ErrAbilityMappingInvalid  = common.NewMessage("The channel model mapping is not valid JSON")
)

// [FORK] AddAbility 在模型上架页新增一条供应关系（平台模型 × 渠道）。
//
// abilities 是 channels 的派生产物，只插一行会在两处失效：InitChannelCache 的候选
// 列表直接遍历 channels.models（不读 abilities），选路根本看不到这一行；任何一次
// UpdateAbilities（改渠道、批量打标签、删模型元数据、上游模型同步）都会先删光该渠道
// 的 abilities 行再按 channels.models 重建，这一行随即消失。所以这里以渠道的
// models 列为权威：把模型名写进去（给了 upstream_model 时同时写 model_mapping），
// 再按渠道当前状态重建 abilities，最后把目标行的 priority 改成调用方要求的值。
func AddAbility(modelName string, channelId int, group string, upstreamModel string, priority int64) error {
	if priority < minAbilityPriority || priority > maxAbilityPriority {
		return ErrAbilityPriorityRange
	}
	upstreamModel = strings.TrimSpace(upstreamModel)
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel, err := lockAbilityChannel(tx, channelId)
		if err != nil {
			return err
		}
		rawGroup, resolvedModel, ok := resolveAbilityRelation(channel, group, modelName)
		if !ok {
			return ErrAbilityGroupNotFound
		}
		// 重复新增不静默成功：前端靠这条 400 判断「这条供应关系已经有了」。
		// 存在性看 channels.models，不看 abilities 行：后者是重建产物，可能留下
		// 孤儿行（models 里没有该模型），用行数判断会把新增挡在 400。
		if channelHasAbilityModel(channel, resolvedModel) {
			return ErrAbilityAlreadyExists
		}
		if err := applyAbilityChannelModel(tx, channel, resolvedModel, upstreamModel, true); err != nil {
			return err
		}
		if err := channel.UpdateAbilities(tx); err != nil {
			return err
		}
		// UpdateAbilities 给新行的 priority 是 channel.Priority，这里改成调用方要求的值
		// （缺省 1）。先确认重建后确实有这一行，避免把「模型列没写进去」当成成功。
		var count int64
		if err := abilityRowQuery(tx, rawGroup, resolvedModel, channelId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrAbilityPriorityNotFound
		}
		return abilityRowQuery(tx, rawGroup, resolvedModel, channelId).Update("priority", priority).Error
	})
	if err != nil {
		return err
	}
	BumpSyncTopic(SyncTopicChannels)
	return nil
}

// [FORK] DeleteAbility 删除一条供应关系。与 AddAbility 对称：从 channels.models 里
// 摘掉模型名（同时清掉 model_mapping 里对应的键），再重建该渠道的 abilities 行。
//
// 已知边界：channels.models 是渠道级而不是 group 级的列，多 group 渠道删一条会
// 连带移除其它 group 下的同名行——这与「在渠道接入改模型清单」的行为一致。
func DeleteAbility(modelName string, channelId int, group string) error {
	err := DB.Transaction(func(tx *gorm.DB) error {
		channel, err := lockAbilityChannel(tx, channelId)
		if err != nil {
			return err
		}
		_, resolvedModel, ok := resolveAbilityRelation(channel, group, modelName)
		if !ok {
			return ErrAbilityGroupNotFound
		}
		// 存在性同样看 channels.models：abilities 行可能正处在「已删、models 未改」
		// 的重建中间态，用它判断会让一条仍在选路的关系删不掉，前端拿到 404 但渠道
		// 仍被选路。模型不在 models 里就说明这条关系本来就不存在。
		if !channelHasAbilityModel(channel, resolvedModel) {
			return ErrAbilityPriorityNotFound
		}
		if err := applyAbilityChannelModel(tx, channel, resolvedModel, "", false); err != nil {
			return err
		}
		return channel.UpdateAbilities(tx)
	})
	if err != nil {
		return err
	}
	BumpSyncTopic(SyncTopicChannels)
	return nil
}

// [FORK] 查一条供应关系的 abilities 行；group 必须传渠道 group 列里的原始 token。
func abilityRowQuery(tx *gorm.DB, group, modelName string, channelId int) *gorm.DB {
	return tx.Model(&Ability{}).Where(commonGroupCol+" = ? AND model = ? AND channel_id = ?", group, modelName, channelId)
}

// [FORK] 锁住渠道行并读回。供应关系的两条写路径都要先锁：读 models 列再写回是
// 读改写，不加锁时两个并发新增会丢一条。
func lockAbilityChannel(tx *gorm.DB, channelId int) (*Channel, error) {
	var channel Channel
	err := lockForUpdate(tx).First(&channel, "id = ?", channelId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAbilityChannelNotFound
	}
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

// [FORK] 三个写接口（新增 / 删除 / 改优先级）共用的关系定位：统一 group 缺省值、
// 渠道原始 group token 与 model 的空白归一化。
//
// abilities.group 写的是 channels.group 的原始 token（UpdateAbilities 不 trim），
// abilities.model 写的是 trim 过的模型名，所以三个入口必须用同一套口径，否则同一个
// 逻辑关系在不同接口里有的能查到、有的查不到（例如 channels.group = "default, vip"
// 时原始 token 是 " vip"）。
func resolveAbilityRelation(channel *Channel, group, modelName string) (rawGroup string, resolvedModel string, ok bool) {
	if group == "" {
		group = "default"
	}
	rawGroup, ok = resolveAbilityGroup(channel, group)
	return rawGroup, strings.TrimSpace(modelName), ok
}

// [FORK] 渠道是否供应某个模型。判据只看 channels.models：缓存选路的候选列表由这一列
// 派生，abilities 只是它的重建产物，可能在重建过程中短暂缺失。用 abilities 行判存在性
// 会让 DELETE 删不掉一条仍在选路的关系，也会让新增被一条孤儿行挡住。
func channelHasAbilityModel(channel *Channel, modelName string) bool {
	for _, name := range channel.GetModels() {
		if strings.TrimSpace(name) == modelName {
			return true
		}
	}
	return false
}

// [FORK] 在渠道的 group 列表里找到与调用方给定 group 对得上的原始 token。
// UpdateAbilities 重建 abilities 时按 channels.group 的原始 token 写 group 列
// （不做 trim），所以这里也必须返回原始 token，否则刚重建出来的行查不到。
func resolveAbilityGroup(channel *Channel, group string) (string, bool) {
	for _, token := range strings.Split(channel.Group, ",") {
		if strings.TrimSpace(token) == group {
			return token, true
		}
	}
	return "", false
}

// [FORK] 把模型名加进 / 移出渠道的 models 列，并同步 model_mapping。
//
// 只改这一个模型名，其它模型与其它映射键原样保留。mapping 解析失败时直接报错，
// 不静默丢掉已有的映射——那条丢失没有任何地方会提示。
func applyAbilityChannelModel(tx *gorm.DB, channel *Channel, modelName, upstreamModel string, add bool) error {
	mapping := map[string]string{}
	raw := strings.TrimSpace(channel.GetModelMapping())
	if raw != "" && raw != "{}" {
		if err := common.UnmarshalJsonStr(raw, &mapping); err != nil {
			return ErrAbilityMappingInvalid
		}
	}
	// JSON 字面量 null 也解析成功（"null" → nil map），后面写映射会 panic。
	if mapping == nil {
		mapping = map[string]string{}
	}

	models := channel.GetModels()
	mappingChanged := false
	if add {
		present := false
		kept := make([]string, 0, len(models)+1)
		for _, name := range models {
			if name == "" {
				continue
			}
			kept = append(kept, name)
			if strings.TrimSpace(name) == modelName {
				present = true
			}
		}
		if !present {
			kept = append(kept, modelName)
		}
		models = kept
		// 只有调用方明确给了上游模型名才动映射：空值表示「直连同名」，
		// 此时不能删掉渠道接入里已配好的映射（那是管理员改过的路由）。
		if upstreamModel != "" {
			if upstreamModel == modelName {
				if _, ok := mapping[modelName]; ok {
					delete(mapping, modelName)
					mappingChanged = true
				}
			} else {
				mapping[modelName] = upstreamModel
				mappingChanged = true
			}
		}
	} else {
		kept := make([]string, 0, len(models))
		for _, name := range models {
			if name != "" && strings.TrimSpace(name) != modelName {
				kept = append(kept, name)
			}
		}
		models = kept
		if _, ok := mapping[modelName]; ok {
			delete(mapping, modelName)
			mappingChanged = true
		}
	}

	channel.Models = strings.Join(models, ",")
	updates := map[string]any{"models": channel.Models}
	if mappingChanged {
		if len(mapping) == 0 {
			channel.ModelMapping = common.GetPointer("")
		} else {
			encoded, err := common.Marshal(mapping)
			if err != nil {
				return err
			}
			channel.ModelMapping = common.GetPointer(string(encoded))
		}
		updates["model_mapping"] = *channel.ModelMapping
	}
	return tx.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates).Error
}
