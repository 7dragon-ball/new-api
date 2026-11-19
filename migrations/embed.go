// [FORK] 迁移 SQL 的载体包。放在仓库最外层而不是 model/ 下，是因为迁移是独立于 Go 代码
// 的资产：目录结构（方言 → 版本号 → SQL）自己就说明了执行顺序，读的人不必穿过 model 包。
//
// Go 的 //go:embed 不能引用上层目录，所以 SQL 必须和 embed 声明的包在同一棵树里。这也正是
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed postgres/*.sql
var postgresFiles embed.FS

// Postgres 是 postgres 方言的迁移目录，直接交给 goose 的 Provider 使用。
// 文件名即版本号（见 fork/README.md「迁移文件名用 YYYYMMDDHHMMSS_<动词>_<对象>.sql」）。
var Postgres = func() fs.FS {
	sub, err := fs.Sub(postgresFiles, "postgres")
	if err != nil {
		panic("migrations: select postgres: " + err.Error())
	}
	return sub
}()
