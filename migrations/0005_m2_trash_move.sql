-- M2: 回收站需要的软删除元数据（移动/重命名复用 updated_at 做修订号）。
-- 只加列与索引；state 枚举沿用 0002 的 active/trashed/purged，不改取值。

ALTER TABLE nodes ADD COLUMN trashed_from INTEGER REFERENCES nodes(id);
ALTER TABLE nodes ADD COLUMN trashed_at INTEGER;

CREATE INDEX nodes_state_owner ON nodes(owner_id, state);
