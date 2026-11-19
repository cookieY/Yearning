begin;

-- §18.80 addendum 1 (owner ruling 2026-10-20): Keep {{param}} placeholders intact in descriptions --
-- the frontend dynamically interpolates current row parameter values at render time so table and rule-set
-- descriptions update immediately after parameter tuning; missing parameters retain placeholders.
-- Pure display metadata, excluded from config_hash and definition hash; seeded from the first sentence of knowledge_text (<=100 chars).

alter table prompt_tools
    add column description text not null default '';

update prompt_tools set description = 'CREATE VIEW需阻断复核' where name = 'builtin.ddl-allow-create-view' and is_builtin;
update prompt_tools set description = '分区表需复核' where name = 'builtin.ddl-allow-partition' and is_builtin;
update prompt_tools set description = '自增初始值必须为1' where name = 'builtin.ddl-autoincrement-init-one' and is_builtin;
update prompt_tools set description = '自增列必须声明UNSIGNED' where name = 'builtin.ddl-autoincrement-unsigned' and is_builtin;
update prompt_tools set description = '字符集必须在允许列表内' where name = 'builtin.ddl-charset-allowlist' and is_builtin;
update prompt_tools set description = '排序规则必须在允许列表内' where name = 'builtin.ddl-collation-allowlist' and is_builtin;
update prompt_tools set description = '列应携带默认值' where name = 'builtin.ddl-column-default-check' and is_builtin;
update prompt_tools set description = '列应显式声明NOT NULL' where name = 'builtin.ddl-column-nullable-check' and is_builtin;
update prompt_tools set description = 'float/double应改为decimal' where name = 'builtin.ddl-float-double-decimal' and is_builtin;
update prompt_tools set description = '禁止跨库RENAME TABLE' where name = 'builtin.ddl-forbid-across-db-rename' and is_builtin;
update prompt_tools set description = 'ALTER禁止AFTER/FIRST列位置调整' where name = 'builtin.ddl-forbid-column-position-change' and is_builtin;
update prompt_tools set description = 'ALTER禁止MODIFY/CHANGE列类型' where name = 'builtin.ddl-forbid-column-type-change' and is_builtin;
update prompt_tools set description = '禁止DROP DATABASE' where name = 'builtin.ddl-forbid-drop-database' and is_builtin;
update prompt_tools set description = 'DROP视图/索引/触发器/存储过程/函数需阻断复核' where name = 'builtin.ddl-forbid-drop-other' and is_builtin;
update prompt_tools set description = '禁止DROP TABLE' where name = 'builtin.ddl-forbid-drop-table' and is_builtin;
update prompt_tools set description = '禁止外键' where name = 'builtin.ddl-forbid-foreign-key' and is_builtin;
update prompt_tools set description = '索引必须显式命名' where name = 'builtin.ddl-forbid-unnamed-index' and is_builtin;
update prompt_tools set description = '标识符不得使用SQL关键词' where name = 'builtin.ddl-identifier-keyword' and is_builtin;
update prompt_tools set description = 'WHERE条件字面值类型必须与列类型一致' where name = 'builtin.ddl-implicit-type-conversion' and is_builtin;
update prompt_tools set description = '索引名必须以idx_或uniq_开头' where name = 'builtin.ddl-index-name-prefix' and is_builtin;
update prompt_tools set description = 'DDL作用表行数不得超过{{max_ddl_affect_rows}}' where name = 'builtin.ddl-max-affect-rows' and is_builtin;
update prompt_tools set description = 'CHAR字段长度不得超过{{max_char_length}}' where name = 'builtin.ddl-max-char-length' and is_builtin;
update prompt_tools set description = '单表索引数不得超过{{max_index_count}}' where name = 'builtin.ddl-max-index-count' and is_builtin;
update prompt_tools set description = '单索引字段数不得超过{{max_index_key_parts}}' where name = 'builtin.ddl-max-index-key-parts' and is_builtin;
update prompt_tools set description = '表名长度不得超过{{max_table_name_length}}' where name = 'builtin.ddl-max-table-name-length' and is_builtin;
update prompt_tools set description = '单个工单禁止多条ALTER' where name = 'builtin.ddl-multi-alter' and is_builtin;
update prompt_tools set description = '单个工单仅允许一条DDL' where name = 'builtin.ddl-multi-ddl' and is_builtin;
update prompt_tools set description = '主键必须为自增列' where name = 'builtin.ddl-pk-auto-increment' and is_builtin;
update prompt_tools set description = '主键列必须为整数类型' where name = 'builtin.ddl-pk-integer' and is_builtin;
update prompt_tools set description = '主键列必须命名为id' where name = 'builtin.ddl-pk-named-id' and is_builtin;
update prompt_tools set description = '全部列必须携带列注释' where name = 'builtin.ddl-require-column-comments' and is_builtin;
update prompt_tools set description = 'CREATE TABLE必须声明主键' where name = 'builtin.ddl-require-primary-key' and is_builtin;
update prompt_tools set description = 'CREATE TABLE必须携带表注释' where name = 'builtin.ddl-require-table-comment' and is_builtin;
update prompt_tools set description = '特殊类型需复核' where name = 'builtin.ddl-special-type' and is_builtin;
update prompt_tools set description = '视图禁止SELECT *' where name = 'builtin.ddl-view-forbid-select-star' and is_builtin;
update prompt_tools set description = 'UPDATE/DELETE语句禁止使用LIMIT子句' where name = 'builtin.dml-forbid-limit' and is_builtin;
update prompt_tools set description = 'UPDATE/DELETE语句禁止ORDER BY子句' where name = 'builtin.dml-forbid-order-by' and is_builtin;
update prompt_tools set description = 'DML语句禁止内嵌SELECT子查询' where name = 'builtin.dml-forbid-subselect' and is_builtin;
update prompt_tools set description = 'INSERT必须显式声明目标列' where name = 'builtin.dml-insert-explicit-columns' and is_builtin;
update prompt_tools set description = 'INSERT禁止显式插入NULL字面值' where name = 'builtin.dml-insert-forbid-null' and is_builtin;
update prompt_tools set description = '单条INSERT批量行数/单行字段数不得超过{{max_batch_rows}}' where name = 'builtin.dml-insert-max-batch-rows' and is_builtin;
update prompt_tools set description = 'UPDATE或DELETE必须携带WHERE条件' where name = 'builtin.dml-require-where' and is_builtin;
update prompt_tools set description = 'WHERE条件值不得为NULL字面值比较' where name = 'builtin.dml-where-not-null-literal' and is_builtin;
update prompt_tools set description = '禁止TRUNCATE语句' where name = 'builtin.forbid-truncate' and is_builtin;
update prompt_tools set description = '语句预估影响行数不得超过{{max_affect_rows}}' where name = 'builtin.max-affect-rows' and is_builtin;
update prompt_tools set description = '同构语句组合并后的语句数量不得超过{{max_group_statements}}' where name = 'builtin.max-group-statement-count' and is_builtin;
update prompt_tools set description = '单条语句IN列表字面值数量不得超过{{max_in_values}}' where name = 'builtin.max-in-value-count' and is_builtin;
update prompt_tools set description = '单条语句尺寸不得超过{{max_statement_bytes}}字节' where name = 'builtin.max-statement-size' and is_builtin;

commit;
