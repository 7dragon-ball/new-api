package model

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/pressly/goose/v3"
)

// [FORK] PostgreSQL 上需要 AutoMigrate 之外的 schema 变更，统一走这里的内嵌 goose
// 迁移。第一份迁移是部门表；后续任何需要改约束、回填数据的表也往同一目录加文件。
//
// 为什么要有这一层：AutoMigrate 只会「加」，不会改约束、不会回填数据，也读不出
// 历史意图。部门表下一步要加约束与数据回填，AutoMigrate 帮不上忙，所以一次性把
// 这张表交给迁移工具，后续 schema 变更不用再纠结用哪套机制。
//
// 为什么内嵌成 Go 库而不是独立 goose 容器：本 fork 是单镜像部署，entrypoint 直接
// 拉起 /new-api。引入外部迁移容器会让「表是否存在」取决于 compose 的启动顺序，
// 比 AutoMigrate 更脆弱。内嵌后迁移随二进制发布，还在原来的启动时点执行。
//
// 只写 PostgreSQL 一份 SQL。本 fork 实际只跑 PG（见 fork/README.md「数据库」），
// 所以不写 SQLite / MySQL 分支，department 也从 AutoMigrate 列表里移除了。
// 测试库例外：测试自己 AutoMigrate 建表，不走这里。

//go:embed migrations/postgres/*.sql
var forkMigrations embed.FS

// runForkMigrations 执行内嵌的全部 fork 迁移，按文件名版本号升序。调用方只在主库为
// PostgreSQL 时调用；这里再挡一次，避免将来有人挪动调用点后在一次 SQLite 启动里
// 执行 PG 方言 SQL。
//
// 直接复用 gorm 的连接池（DB.DB()），不另开连接。迁移本身幂等，多节点同时启动也
// 不会互相破坏：goose 先查版本表再写，后到者读到已应用即跳过。
func runForkMigrations() error {
	if !common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		return fmt.Errorf("goose: fork migrations are PostgreSQL-only, got %q", common.MainDatabaseType())
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("goose: get sql.DB: %w", err)
	}

	migrations, err := fs.Sub(forkMigrations, "migrations/postgres")
	if err != nil {
		return fmt.Errorf("goose: select migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		return fmt.Errorf("goose: new provider: %w", err)
	}
	// 不调 provider.Close()：它关的是 gorm 的连接池，不是 goose 自己的资源。

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// results 只包含本次真正执行的迁移；版本已记录时为空，所以日志不会每次启动都刷。
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("goose: up: %w", err)
	}
	for _, result := range results {
		common.SysLog(common.LogText("database migration applied: %s", result.Source.Path))
	}
	return nil
}
