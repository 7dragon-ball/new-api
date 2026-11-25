package controller

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [FORK] 模型标签字典。
//
// 列表页的「标签」筛选框在改服务端分页后，候选只能从当页 20 条模型的 tags 里
// 现算 —— 某个 needle 命中超过 20 条模型时，只在第 21 条之后出现的标签就看不到了
// （手打仍能筛到，只是没建议）。这个接口直接返回去重后的标签全集。
func TestModelTagsFork(t *testing.T) {
	db := modelManagementDB(t, "sqlite", "")

	records := []model.Model{
		{ModelName: "tag-fork-a", Tags: "vision,旗舰", Status: 1},
		{ModelName: "tag-fork-b", Tags: "vision", Status: 1},
		{ModelName: "tag-fork-c", Tags: "海外,旗舰-v2", Status: 1},
		{ModelName: "tag-fork-empty", Tags: "", Status: 1},
	}
	for i := range records {
		require.NoError(t, records[i].Insert())
	}
	ids := make([]int, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.Id)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Unscoped().Where("id IN ?", ids).Delete(&model.Model{}).Error)
	})

	list := func(path string) []string {
		var resp struct {
			Success bool
			Data    struct{ Items []string }
		}
		modelManagementRequest(t, ListModelTagsFork, "GET", path, nil, &resp)
		require.True(t, resp.Success)
		return resp.Data.Items
	}

	t.Run("dedupes and sorts", func(t *testing.T) {
		// vision 挂在两条模型上，只留一个；空 tags 不产出任何标签。
		// 顺序是 Go sort.Strings 的字节序（按码点），不是拼音序：
		// 旗 U+65D7 < 海 U+6D77，所以「旗舰」排在「海外」前面。
		assert.Equal(t, []string{"vision", "旗舰", "旗舰-v2", "海外"}, list("/api/models/tags"))
	})

	t.Run("keyword narrows", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"旗舰", "旗舰-v2"}, list("/api/models/tags?keyword=旗舰"))
	})

	t.Run("narrows by single tag, not whole cell", func(t *testing.T) {
		// 「vision,旗舰」整格命中 keyword=vision，但「旗舰」本身不含 vision。
		// 数据库的 LIKE 是整串匹配，必须在内存里按单个标签再收一次，
		// 否则候选里会冒出与输入无关的标签。
		assert.Equal(t, []string{"vision"}, list("/api/models/tags?keyword=vision"))
	})

	t.Run("limit caps", func(t *testing.T) {
		assert.Len(t, list("/api/models/tags?limit=1"), 1)
	})

	t.Run("zero limit falls back to default", func(t *testing.T) {
		assert.NotEmpty(t, list("/api/models/tags?limit=0"), "limit=0 应回退默认值而不是返回空")
	})
}

// [FORK] 标签大小写：字典按小写去重只留一种拼写，筛选必须同样不区分大小写。
// PG 的 LIKE 区分大小写，同一标签出现 Vision / vision 两种拼写时，点选字典返回的
// 那种会被筛成只剩一半模型；SQLite / MySQL 的 LIKE 默认不敏感，本地跑盖不住这个差异。
func TestModelTagCaseInsensitiveFilter(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			db := modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			if dialect.kind == "sqlite" {
				// SQLite 的 LIKE 默认对 ASCII 不区分大小写，跟 PG 不一样。打开这个 pragma
				// 才能把「按库里拼写做 LIKE」的漏检暴露出来。pragma 是连接级设置，
				// 所以把池子限到 1 条连接再设，后面的查询都走这条。
				sqlDB, err := db.DB()
				require.NoError(t, err)
				sqlDB.SetMaxOpenConns(1)
				require.NoError(t, db.Exec("PRAGMA case_sensitive_like=ON").Error)
			}

			records := []model.Model{
				{ModelName: "tag-case-upper", Tags: "Vision", Status: 1},
				{ModelName: "tag-case-lower", Tags: "vision", Status: 1},
				{ModelName: "tag-case-mixed", Tags: "VISION,旗舰", Status: 1},
			}
			for i := range records {
				require.NoError(t, records[i].Insert())
			}
			ids := make([]int, 0, len(records))
			for _, r := range records {
				ids = append(ids, r.Id)
			}
			t.Cleanup(func() {
				require.NoError(t, db.Unscoped().Where("id IN ?", ids).Delete(&model.Model{}).Error)
			})

			candidates, err := model.ListModelTags("", 0)
			require.NoError(t, err)
			require.Len(t, candidates, 2, "三种拼写只留一种：%v", candidates)
			spelling := ""
			for _, tag := range candidates {
				if strings.EqualFold(tag, "vision") {
					spelling = tag
				}
			}
			require.NotEmpty(t, spelling, "字典里应有 vision 的某一种拼写：%v", candidates)

			// 粗筛也不能因为大小写不同丢掉整格
			narrowed, err := model.ListModelTags("VISION", 0)
			require.NoError(t, err)
			assert.Equal(t, []string{spelling}, narrowed)

			// 点选字典返回的那种拼写，必须筛到全部大小写变体
			found, total, err := model.SearchModels("", "", "", "", spelling, 0, 100)
			require.NoError(t, err)
			names := make([]string, 0, len(found))
			for _, item := range found {
				names = append(names, item.ModelName)
			}
			assert.ElementsMatch(t, []string{"tag-case-upper", "tag-case-lower", "tag-case-mixed"}, names)
			assert.EqualValues(t, 3, total)
		})
	}
}
