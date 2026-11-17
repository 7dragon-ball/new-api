-- +goose Up
-- [FORK] 部门表由 goose 迁移创建，不再交给 GORM AutoMigrate。
-- 已部署的库中该表由旧版 AutoMigrate 建出，IF NOT EXISTS 让本迁移在那些库上变成空操作；
-- 索引同理。两者都是幂等的，所以首次切换到 goose 不需要 baseline 步骤。
CREATE TABLE IF NOT EXISTS department (
  id         BIGSERIAL PRIMARY KEY,
  name       VARCHAR(30) NOT NULL,
  created_at BIGINT,
  updated_at BIGINT
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_department_name ON department (name);

-- +goose Down
-- 不回滚：删表会连部门数据一起丢。需要撤销时人工执行。
