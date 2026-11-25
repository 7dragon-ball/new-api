package model

import (
	"sort"
	"strings"
)

// [FORK] 模型标签字典：去重、按字典序的标签全集。
//
// 为什么需要它：列表页改成服务端分页后，「标签」筛选框的候选只能从当前页 20 条
// 模型的 tags 里现算。某个关键词命中超过 20 条模型时，只在第 21 条之后出现的标签
// 就看不到建议了（手打仍能筛到，只是没提示）。这里直接给出全集。
//
// 与 SearchModels 的 tag 筛选同一口径：子串匹配；大小写由 SQL 的 LIKE 决定
// （PG 上敏感）。下面内存里的 ToLower 只用于按「单个标签」收窄，不放宽粗筛。
//
// tags 在上游是逗号分隔的字符串列（models.tags varchar(255)），没有独立的标签表，
// 所以只能把列拉回来在内存里拆。
func ListModelTags(keyword string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	var raw []string
	// 空 tags 的行不产出标签；NULL 也要排掉，否则 Pluck 进 []string 会报错
	db := DB.Model(&Model{}).Where("tags IS NOT NULL AND tags <> ''")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		// 粗筛：先让数据库按子串收窄，下面再按「单个标签」精确收窄
		db = db.Where("tags LIKE ?", "%"+keyword+"%")
	}
	if err := db.Pluck("tags", &raw).Error; err != nil {
		return nil, err
	}

	needle := strings.ToLower(keyword)
	seen := make(map[string]struct{}, len(raw))
	tags := make([]string, 0, len(raw))
	for _, row := range raw {
		for _, tag := range strings.Split(row, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			// 数据库的 LIKE 是整串匹配（"vision,旗舰" 命中 keyword="vision"），
			// 但用户要的是「单个标签含这个词」，这里再收一次
			if needle != "" && !strings.Contains(strings.ToLower(tag), needle) {
				continue
			}
			key := strings.ToLower(tag)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	if len(tags) > limit {
		tags = tags[:limit]
	}
	return tags, nil
}
