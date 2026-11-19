begin;

-- 000009_builtin_skill_package B8 ruling (2026-08-27 revision 98a385c
-- two-layer division): business review rules are carried exclusively by Prompt Tools with the database
-- as the single persistence store -- this migration seeds the built-in review skill package into prompt_tools
-- (full port of legacy AuditRule excluding pt-osc/gh-ost IsOSC/OSCExpr/OscSize; see BE-B8-GATE
-- notes/old-rule-porting-map.md for execution-domain and per-deployment column/prefix rules).
-- Three-part governance: built-in rows cannot be deleted (is_builtin), definitions are immutable
-- (rejected on definition hash mismatch during replace), and parameters are tunable (persisted in
-- parameters jsonb; admin updates change config_hash and invalidate frozen Runs). engine distinguishes
-- target databases (all|mysql|postgresql) and filters injection by stage datasource engine.
-- Knowledge text ref-links thresholds via {{param_name}} and renders current in-row values on injection.
-- config_hash is the sha256 of canonical Go json.Marshal output (struct field order + sorted map keys);
-- seed integrity is pinned by TestB8MigrationSkillPackageIntegrity. Zero UUID in created_by marks system seed rows.

alter table prompt_tools
    add column engine text not null default 'all' check (engine in ('all', 'mysql', 'postgresql')),
    add column parameters jsonb not null default '{}'::jsonb,
    add column is_builtin boolean not null default false;

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-require-where', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：UPDATE或DELETE必须携带WHERE条件。判定依据：组的representative_shape中where=false（statistics.unbounded_mutation=true）且op为update或delete时输出本发现；事实缺失不输出。","finding_template":{"category":"correctness","finding_key":"builtin.dml-require-where","message":"该同构语句组未携带任何WHERE限定，构成全表变更风险","severity":"high","suggestion":"补充WHERE条件并参数化字面值后重新预审","title":"UPDATE/DELETE未携带WHERE条件"},"severity_whitelist":["high"],"version":1}'::jsonb, '44a22c1e65d5cdb6360cac1a2d7c6fbdcd3aac57b3b8b6898da97ca71594ac84', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-forbid-limit', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：UPDATE/DELETE语句禁止使用LIMIT子句（旧DMLAllowLimitSTMT）。判定依据：statistics.dml_has_limit=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.dml-forbid-limit","message":"MySQL的UPDATE/DELETE LIMIT修改行集不确定，主从与回放语义不安全","severity":"medium","suggestion":"移除LIMIT并补充确定性WHERE条件","title":"UPDATE/DELETE使用LIMIT"},"severity_whitelist":["medium"],"version":1}'::jsonb, '35340040c2d6efb78369d6b9ac3ad4fd4d1bb9ceb2892fac49b1960d0881aaf8', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-forbid-order-by', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：UPDATE/DELETE语句禁止ORDER BY子句（旧DMLOrder）。判定依据：statistics.dml_has_order_by=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.dml-forbid-order-by","message":"UPDATE/DELETE携带ORDER BY改动顺序敏感且执行计划代价高","severity":"medium","suggestion":"移除ORDER BY或改为确定性分批","title":"UPDATE/DELETE使用ORDER BY"},"severity_whitelist":["medium"],"version":1}'::jsonb, '48ce5539d5ac54e8fb9ea2e29632d929fac5c212d7e103642d18348b6c90b682', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-forbid-subselect', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：DML语句禁止内嵌SELECT子查询（旧DMLSelect）。判定依据：statistics.dml_has_subselect=true时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.dml-forbid-subselect","message":"DML语句内嵌SELECT子查询，锁定与执行计划风险升高","severity":"medium","suggestion":"拆分为独立查询或改写为JOIN","title":"DML包含SELECT子查询"},"severity_whitelist":["medium"],"version":1}'::jsonb, '3df365033402204d407c3b8b190d0682ece13b122ad4f8d7270aa1b891682dc7', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-insert-explicit-columns', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：INSERT必须显式声明目标列（旧DMLInsertColumns与DMLInsertMustExplicitly语义合并）。判定依据：statistics.insert_has_columns=false时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.dml-insert-explicit-columns","message":"INSERT未列出目标列，表结构变化时将错位写入","severity":"medium","suggestion":"显式声明列名后重新提交","title":"INSERT未显式声明列名"},"severity_whitelist":["medium"],"version":1}'::jsonb, '4c29a0d82bccfb6bb42905bd383e411779047fdb1ac280b548714a6e7b0324f8', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-insert-max-batch-rows', 'enabled', 'all', '{"max_batch_rows":1000}'::jsonb,
        '{"knowledge_text":"规则：单条INSERT批量行数/单行字段数不得超过{{max_batch_rows}}（旧DMLMaxInsertRows）。判定依据：statistics.values_row_count_max大于{{max_batch_rows}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.dml-insert-max-batch-rows","message":"单条语句批量行数超出规则上限1000（含单行字段数语义，旧DMLMaxInsertRows）","severity":"medium","suggestion":"分批提交并核对影响行数","title":"批量INSERT行数超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '145dc53eac4c962f4f6447da1acfd5385d5130a758c82cb59bbfc4c89b860486', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-where-not-null-literal', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：WHERE条件值不得为NULL字面值比较（旧DMLWhereExprValueIsNull）。判定依据：statistics.where_literal_null=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.dml-where-not-null-literal","message":"WHERE条件使用=NULL类比较，恒不为真导致条件失效","severity":"medium","suggestion":"改用IS NULL/IS NOT NULL语义","title":"WHERE条件与NULL字面值比较"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'fc555770436e7a7064168c4ac7b5bf0f08c3a4615154b3777f015da602ba7240', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.dml-insert-forbid-null', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：INSERT禁止显式插入NULL字面值（旧DMLAllowInsertNull）。判定依据：statistics.insert_has_null_literal=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.dml-insert-forbid-null","message":"INSERT显式写入NULL可能破坏NOT NULL约束与非空业务语义","severity":"medium","suggestion":"去除NULL或提供确定默认值","title":"INSERT显式插入NULL"},"severity_whitelist":["medium"],"version":1}'::jsonb, '0e2e20d665ea679d7fc1273333f7c31f052c843ddb36be1df9596f1a4086f864', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.max-in-value-count', 'enabled', 'all', '{"max_in_values":1000}'::jsonb,
        '{"knowledge_text":"规则：单条语句IN列表字面值数量不得超过{{max_in_values}}。判定依据：statistics.in_value_count_max大于{{max_in_values}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.max-in-value-count","message":"单条语句IN列表字面值数量超出规则上限1000","severity":"medium","suggestion":"拆分为多条参数化语句或改用临时表加载","title":"IN列表字面值数量超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '8cfefbcf1f27039701d35d1192c33ac731674dc12ffcbacc090df56428e2a813', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.max-group-statement-count', 'enabled', 'all', '{"max_group_statements":10000}'::jsonb,
        '{"knowledge_text":"规则：同构语句组合并后的语句数量不得超过{{max_group_statements}}。判定依据：statement_count大于{{max_group_statements}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.max-group-statement-count","message":"同构语句组合并后的语句数量超出规则上限10000","severity":"medium","suggestion":"使用批量执行通道并核对影响行数","title":"同构语句数量超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '67a2415a40a30c0f3c8cbcbdbb72e348d586e27863a5709e3522e7acdb02cf88', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.max-statement-size', 'enabled', 'all', '{"max_statement_bytes":262144}'::jsonb,
        '{"knowledge_text":"规则：单条语句尺寸不得超过{{max_statement_bytes}}字节。判定依据：statistics.max_statement_bytes大于{{max_statement_bytes}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.max-statement-size","message":"组内最大单条语句尺寸超出规则上限262144字节","severity":"medium","suggestion":"拆分语句或精简批量载荷后重新预审","title":"单条语句尺寸超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '875836beb4557d606587a82132215f08d1d0f5895739beee6dbd942d63ad6879', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-require-primary-key', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：CREATE TABLE必须声明主键（旧DDLEnablePrimaryKey）。判定依据：op为create且非视图、statistics.ddl_has_primary_key=false时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-require-primary-key","message":"CREATE TABLE未声明主键，行标识与复制语义缺失","severity":"high","suggestion":"补充主键后重新提交","title":"建表缺少主键"},"severity_whitelist":["high"],"version":1}'::jsonb, '2df605277d2730c294f78000f7b8f96365516452b8ef04832251ab170817f3df', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-require-table-comment', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：CREATE TABLE必须携带表注释（旧DDLCheckTableComment）。判定依据：statistics.ddl_table_comment_present=false时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-require-table-comment","message":"CREATE TABLE未携带表级COMMENT","severity":"medium","suggestion":"补充表注释后重新提交","title":"建表缺少表注释"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'dbcd9dd48bcf0d4f9e7d294bccfe2754d0eb6d2d4edfc6d91440b5d86e09b78d', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-require-column-comments', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：全部列必须携带列注释（旧DDlCheckColumnComment）。判定依据：statistics.ddl_column_comment_count小于ddl_column_count时输出本发现；任一事实缺失不输出。","finding_template":{"category":"governance","finding_key":"builtin.ddl-require-column-comments","message":"存在未携带COMMENT的列（注释数小于列数）","severity":"medium","suggestion":"为全部列补充注释","title":"列注释不完整"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'ca0f2ed944168207d94630da88913a57ea0d496e3fe49190b5b794497c9b25b6', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-column-nullable-check', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：列应显式声明NOT NULL（旧DDLCheckColumnNullable）。判定依据：ddl_column_count减ddl_not_null_count大于0时输出本发现；事实缺失不输出。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-column-nullable-check","message":"建表存在未声明NOT NULL的列（可空列计数=列数-非空计数）","severity":"medium","suggestion":"评估可空性并补充NOT NULL","title":"存在可空列"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'c8e724591ba786df9c39573e98e7edea1fd4893294672e2a64b3f13c60e6033a', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-column-default-check', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：列应携带默认值（旧DDLCheckColumnDefault）。判定依据：ddl_default_count小于ddl_column_count时输出本发现；事实缺失不输出。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-column-default-check","message":"存在未声明DEFAULT的列（默认值计数小于列数）","severity":"medium","suggestion":"为必要列补充DEFAULT","title":"列缺少默认值"},"severity_whitelist":["medium"],"version":1}'::jsonb, '4a507335b3dd8ca5241b7f9f38808c336c81ed4b86591247ecd568cc1032ce1b', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-max-char-length', 'enabled', 'all', '{"max_char_length":10,"max_varchar_length":1024}'::jsonb,
        '{"knowledge_text":"规则：CHAR字段长度不得超过{{max_char_length}}（旧DDLMaxCharLength默认）。判定依据：ddl_max_char_length大于{{max_char_length}}时输出本发现；varchar超长（ddl_max_varchar_length大于{{max_varchar_length}}）合并本发现输出。","finding_template":{"category":"performance","finding_key":"builtin.ddl-max-char-length","message":"CHAR字段最大长度超出规则上限10（旧默认；varchar最大长度一并审视）","severity":"medium","suggestion":"改用varchar或缩短字段","title":"CHAR长度超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '9a9c67f3706007caa15d650774dd231154b075a500ca27a6f0b37a968afbcb79', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-max-table-name-length', 'enabled', 'all', '{"max_table_name_length":10}'::jsonb,
        '{"knowledge_text":"规则：表名长度不得超过{{max_table_name_length}}（旧MaxTableNameLen默认）。判定依据：ddl_table_name_length大于{{max_table_name_length}}时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-max-table-name-length","message":"表名长度超出规则上限10（旧默认）","severity":"low","suggestion":"缩短表名","title":"表名长度超出上限"},"severity_whitelist":["low"],"version":1}'::jsonb, 'a3714fc02b5ac31906e38267220fa858b301338923dd1f6b8074b5b648e57f85', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-identifier-keyword', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：标识符不得使用SQL关键词（旧CheckIdentifier）。判定依据：ddl_table_identifier_not_keyword=false时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-identifier-keyword","message":"表标识符命中SQL保留词（代码词法判定布尔）","severity":"low","suggestion":"更换标识符","title":"标识符使用SQL关键词"},"severity_whitelist":["low"],"version":1}'::jsonb, 'e667542a55496fced9e1ba8f19bd9223e4d2ee21e2ff6129f398f41fa8757612', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-pk-named-id', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：主键列必须命名为id（旧DDLPrimaryKeyMust）。判定依据：ddl_pk_named_id=false时输出本发现；主键列名不可解析时不输出。","finding_template":{"category":"governance","finding_key":"builtin.ddl-pk-named-id","message":"主键列名不符合id约定（代码词法判定布尔）","severity":"low","suggestion":"按约定重命名主键","title":"主键未命名为id"},"severity_whitelist":["low"],"version":1}'::jsonb, 'f678f739f3903e210601dc414f339a8b0c5ffe1128f084295dd87b05d5162d80', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-pk-integer', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：主键列必须为整数类型（旧DDLAllowPRINotInt）。判定依据：ddl_pk_is_integer=false时输出本发现；类型不可解析时不输出。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-pk-integer","message":"主键列类型非整数族（代码词法判定布尔）","severity":"low","suggestion":"改用整数主键","title":"主键非整数类型"},"severity_whitelist":["low"],"version":1}'::jsonb, '526749e62e9c0afe6dcc45a636a0effca0a89403fb78680daf5487b988bde82f', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-autoincrement-init-one', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：自增初始值必须为1（旧DDLEnableAutoincrementInit）。判定依据：ddl_autoincrement_init_one=false时输出本发现；未显式设置初始值（键缺失）不输出。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-autoincrement-init-one","message":"AUTO_INCREMENT表选项初始值不为1","severity":"medium","suggestion":"移除初始值或置为1","title":"自增初始值非1"},"severity_whitelist":["medium"],"version":1}'::jsonb, '6e0e959177efca508910b439eb90c0a5c1ed56f87292d403a7c451bfdc98005a', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-pk-auto-increment', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：主键必须为自增列（旧DDLEnableAutoIncrement）。判定依据：ddl_has_primary_key=true且ddl_has_auto_increment=false时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-pk-auto-increment","message":"主键列未声明AUTO_INCREMENT","severity":"medium","suggestion":"主键补充AUTO_INCREMENT","title":"主键未使用自增"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'b7b848708752bbdf79894d8e1707728c53b92e0b4a931e97b13fe6bd5c08d256', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-autoincrement-unsigned', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：自增列必须声明UNSIGNED（旧DDLEnableAutoincrementUnsigned）。判定依据：ddl_has_auto_increment=true且ddl_autoincrement_unsigned=false时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-autoincrement-unsigned","message":"AUTO_INCREMENT列缺少UNSIGNED，取值域减半","severity":"medium","suggestion":"补充UNSIGNED","title":"自增列未声明UNSIGNED"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'be2f454bcd5205f9c2b87f73202c9046ad8539709497973b394f27d6229b17f9', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-across-db-rename', 'enabled', 'mysql', '{}'::jsonb,
        '{"knowledge_text":"规则：禁止跨库RENAME TABLE（旧DDLEnableAcrossDBRename）。判定依据：ddl_across_db_rename=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-forbid-across-db-rename","message":"RENAME TABLE跨数据库边界迁移对象","severity":"high","suggestion":"拆分为分库内操作并评估锁影响","title":"跨库RENAME TABLE"},"severity_whitelist":["high"],"version":1}'::jsonb, '35c38a5c2eec6e10b090ca1a91c2fabfa1ceb71fe02a42a9b5e972d93286adf4', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-charset-allowlist', 'disabled', 'mysql', '{"allowed_charsets":["utf8mb4","utf8"]}'::jsonb,
        '{"knowledge_text":"规则：字符集必须在允许列表内（旧SupportCharset；默认禁用，启用后按列表判定）。判定依据：ddl_charset存在且不在{{allowed_charsets}}内时输出本发现；未声明字符集不输出。","finding_template":{"category":"governance","finding_key":"builtin.ddl-charset-allowlist","message":"语句声明的字符集不在允许列表[utf8mb4,utf8]（旧SupportCharset默认未启用）","severity":"medium","suggestion":"改用允许的字符集","title":"字符集不在允许列表"},"severity_whitelist":["medium"],"version":1}'::jsonb, '886c65181fddb7ec6f0df16a301b6b0be752628716eca417655ffa58a58b7a50', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-collation-allowlist', 'disabled', 'mysql', '{"allowed_collations":["utf8mb4_general_ci","utf8mb4_bin","utf8_general_ci","utf8_bin"]}'::jsonb,
        '{"knowledge_text":"规则：排序规则必须在允许列表内（旧SupportCollation；默认禁用）。判定依据：ddl_collation存在且不在{{allowed_collations}}内时输出本发现；未声明不输出。","finding_template":{"category":"governance","finding_key":"builtin.ddl-collation-allowlist","message":"语句声明的COLLATE不在允许列表（旧SupportCollation默认未启用）","severity":"medium","suggestion":"改用允许的排序规则","title":"排序规则不在允许列表"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'e175d5a51092b16524a1381bbe8f7b63788a3c764f244ddb6b4507cb0523f72d', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-drop-table', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：禁止DROP TABLE（旧DDLEnableDropTable）。判定依据：ddl_drop_target=table时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-forbid-drop-table","message":"该语句组包含DROP TABLE，删除对象不可自动恢复","severity":"high","suggestion":"确认备份与恢复方案后由人工复核","title":"DROP TABLE操作"},"severity_whitelist":["high"],"version":1}'::jsonb, '286da14c7269fe01e0d3025728ae88ac1fd72072edbceaf1320fdc5b5c166a8e', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-drop-database', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：禁止DROP DATABASE（旧DDLEnableDropDatabase）。判定依据：ddl_drop_target=database时输出本发现。","finding_template":{"category":"availability","finding_key":"builtin.ddl-forbid-drop-database","message":"该语句组包含DROP DATABASE，整库对象将被删除","severity":"critical","suggestion":"终止并改为对象级变更","title":"DROP DATABASE操作"},"severity_whitelist":["critical"],"version":1}'::jsonb, 'de771edd1f58ccd0e542042dd28d4772ba7c2c48c6a18646176bcb48ec71f373', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-drop-other', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：DROP视图/索引/触发器/存储过程/函数需阻断复核。判定依据：ddl_drop_target为view/index/trigger/procedure/function/other时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-forbid-drop-other","message":"该语句组包含视图/索引/触发器/存储过程等对象的DROP","severity":"high","suggestion":"确认影响后由人工复核","title":"DROP其他对象操作"},"severity_whitelist":["high"],"version":1}'::jsonb, 'd75f0cd7065f0df22ad4a35f007fbd74fb4fc9a2aac812cf18f6e804476e9d6d', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.forbid-truncate', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：禁止TRUNCATE语句。判定依据：representative_shape中op=truncate时输出本发现。","finding_template":{"category":"availability","finding_key":"builtin.forbid-truncate","message":"该语句组包含TRUNCATE，无法按行回滚，将立即清空目标表","severity":"critical","suggestion":"确认目标表与业务窗口后由人工复核","title":"TRUNCATE清空表"},"severity_whitelist":["critical"],"version":1}'::jsonb, 'd09ba684fe5f4c49fea65aabf187e67098b7aa15273d3c12127298f8bd192ba5', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-column-type-change', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：ALTER禁止MODIFY/CHANGE列类型（旧DDLAllowColumnType）。判定依据：ddl_alters_column_type=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-forbid-column-type-change","message":"ALTER语句通过MODIFY/CHANGE修改列类型，存在锁表与隐式转换风险","severity":"medium","suggestion":"评估锁窗口与兼容性后重新提交","title":"ALTER修改列类型"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'cfe6bd3a001cec794800533248e7137ebc5c4f4f30a13adcdfa848c6220ad75e', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-column-position-change', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：ALTER禁止AFTER/FIRST列位置调整（旧DDLAllowChangeColumnPosition）。判定依据：ddl_changes_column_position=true时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-forbid-column-position-change","message":"ALTER使用AFTER/FIRST调整列位置，全表重建","severity":"low","suggestion":"去除位置调整","title":"ALTER调整列位置"},"severity_whitelist":["low"],"version":1}'::jsonb, '0fbc085d544f7491078c5fdbe7b50e9ba585ec26fc97aaa031164a221ad03884', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-float-double-decimal', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：float/double应改为decimal（旧DDLCheckFloatDouble）。判定依据：ddl_has_float_double=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-float-double-decimal","message":"存在float/double列，精度语义不稳定","severity":"medium","suggestion":"改用decimal","title":"使用float/double类型"},"severity_whitelist":["medium"],"version":1}'::jsonb, '28b4beeaf396091f594956a167c61f4c3d6d9312dcc95e6ebaa9ea462eddb7f2', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-special-type', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：特殊类型需复核（旧AllowSpecialType）。判定依据：ddl_has_special_type=true时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-special-type","message":"存在enum/set/geometry/point/blob/text/json等特殊类型列","severity":"medium","suggestion":"确认必要性或改用常规类型","title":"使用特殊类型"},"severity_whitelist":["medium"],"version":1}'::jsonb, '8b88412559ddb894f81c2c0b9b1289508b318f780b923d9016d896d7bdc86adc', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-foreign-key', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：禁止外键（旧DDLEnableForeignKey）。判定依据：ddl_has_foreign_key=true时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-forbid-foreign-key","message":"语句定义FOREIGN KEY，写入路径与迁移兼容性风险高","severity":"high","suggestion":"去除外键并在应用层维护引用完整性","title":"定义外键约束"},"severity_whitelist":["high"],"version":1}'::jsonb, 'ce39b7d1e8cdef115fd0c4aa75c37834f1829aea1985aee8d1095ec6482135bf', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-allow-create-view', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：CREATE VIEW需阻断复核（旧AllowCreateView）。判定依据：ddl_is_create_view=true时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-allow-create-view","message":"该语句组创建视图","severity":"medium","suggestion":"确认视图定义与授权边界","title":"CREATE VIEW操作"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'ef27371ad6385ab835e75aa5c817018ed019c6f168b031057724b43085d7df3c', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-view-forbid-select-star', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：视图禁止SELECT *（旧AllowCrateViewWithSelectStar）。判定依据：ddl_is_create_view=true且ddl_view_select_star=true时输出本发现。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-view-forbid-select-star","message":"视图定义使用SELECT *，基表结构变化时视图语义漂移","severity":"medium","suggestion":"显式列举视图列","title":"视图使用SELECT *"},"severity_whitelist":["medium"],"version":1}'::jsonb, '4ff493f2b3b0bc265347046dac13b8bb87df3f847781d569ec7e70237eb6dee3', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-allow-partition', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：分区表需复核（旧AllowCreatePartition）。判定依据：ddl_has_partition=true时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-allow-partition","message":"语句使用PARTITION BY分区","severity":"medium","suggestion":"确认分区键与运维方案","title":"创建分区表"},"severity_whitelist":["medium"],"version":1}'::jsonb, '1d072a0ecfb406d8baf959cea4f978c7ed7aa769ab99e8245e31252890244d64', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-forbid-unnamed-index', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：索引必须显式命名（旧DDLEnableNullIndexName）。判定依据：ddl_index_unnamed_count大于0时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-forbid-unnamed-index","message":"建表内联索引存在空名项（代码词法判定计数）","severity":"medium","suggestion":"为索引显式命名","title":"存在未命名索引"},"severity_whitelist":["medium"],"version":1}'::jsonb, '99e9892afb4003fb3ba3e2f30782c72dd3fe1c7683dde17781a46c451128b68f', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-index-name-prefix', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：索引名必须以idx_或uniq_开头（旧DDLIndexNameSpec）。判定依据：ddl_index_prefix_conformant=false时输出本发现。","finding_template":{"category":"governance","finding_key":"builtin.ddl-index-name-prefix","message":"索引名未遵循idx_/uniq_前缀约定（代码词法判定布尔）","severity":"low","suggestion":"按规范重命名索引","title":"索引命名不符合前缀规范"},"severity_whitelist":["low"],"version":1}'::jsonb, '96dd895cbf522ff7de406558c2a9156dea482e49e41f91fb6eb22ef6f733c345', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-max-index-key-parts', 'enabled', 'all', '{"max_index_key_parts":5}'::jsonb,
        '{"knowledge_text":"规则：单索引字段数不得超过{{max_index_key_parts}}。判定依据：ddl_index_max_key_parts大于{{max_index_key_parts}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-max-index-key-parts","message":"单个索引包含字段数超出规则上限5（旧DDLMaxKeyParts默认）","severity":"medium","suggestion":"缩减索引字段","title":"单索引字段数超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, '4f12018fa9d86872aecdc21713500951d981ee8054a909813c26ec6f5350a727', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-max-index-count', 'enabled', 'all', '{"max_index_count":5}'::jsonb,
        '{"knowledge_text":"规则：单表索引数不得超过{{max_index_count}}。判定依据：ddl_index_count大于{{max_index_count}}时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-max-index-count","message":"单表索引数量超出规则上限5（旧DDLMaxKey默认）","severity":"medium","suggestion":"精简索引设计","title":"单表索引数超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'eac65e44ce051e7488409373363a80824df37f9f970a4ac43dc0897c931e14a1', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-multi-alter', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：单个工单禁止多条ALTER（旧DDLAllowMultiAlter）。判定依据：上下文中全部alter组的statement_count之和大于1时输出本发现（对每个alter组各输出一次）。","finding_template":{"category":"performance","finding_key":"builtin.ddl-multi-alter","message":"工单内包含多条ALTER语句（上下文全部alter组statement_count总和大于1）","severity":"low","suggestion":"合并为单条ALTER","title":"多ALTER语句"},"severity_whitelist":["low"],"version":1}'::jsonb, '985fb06bda23ae1d7864e55a1e3d43f75dc37325bd90694c915f944bed552771', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-multi-ddl', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：单个工单仅允许一条DDL（旧DDLMultiToCommit）。判定依据：上下文中全部ddl组（含alter/create/drop等）的statement_count之和大于1时输出本发现。","finding_template":{"category":"performance","finding_key":"builtin.ddl-multi-ddl","message":"工单内包含多条DDL语句（上下文全部ddl组statement_count总和大于1）","severity":"low","suggestion":"评估分批与执行窗口","title":"多条DDL语句"},"severity_whitelist":["low"],"version":1}'::jsonb, 'dcb15d804394850141a6553f382f671d2e85b89b70ec88435daf283e4656ce22', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.max-affect-rows', 'enabled', 'all', '{"max_affect_rows":1000}'::jsonb,
        '{"knowledge_text":"规则：语句预估影响行数不得超过{{max_affect_rows}}。判定依据：调用safe_explain工具取得EstimatedRows事实；EstimatedRows大于{{max_affect_rows}}时输出本发现；无法取得事实时不输出并注明unknown。","finding_template":{"category":"performance","finding_key":"builtin.max-affect-rows","message":"safe_explain预估影响行数超出规则上限1000（旧MaxAffectRows默认）","severity":"high","suggestion":"缩小影响范围或分批执行","title":"预估影响行数超出上限"},"severity_whitelist":["high"],"version":1}'::jsonb, '6a22794fdfa280facc521848ff6ccbe87052013cba4c8870f9e04ba6854c1c77', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-max-affect-rows', 'enabled', 'all', '{"max_ddl_affect_rows":1000}'::jsonb,
        '{"knowledge_text":"规则：DDL作用表行数不得超过{{max_ddl_affect_rows}}。判定依据：调用table_statistics工具取得目标表行数；行数大于{{max_ddl_affect_rows}}时输出本发现；无法取得事实时不输出并注明unknown。","finding_template":{"category":"performance","finding_key":"builtin.ddl-max-affect-rows","message":"DDL作用表的行数统计超出规则上限1000（旧MaxDDLAffectRows默认）","severity":"medium","suggestion":"评估锁窗口与在线变更方案","title":"DDL对象表规模超出上限"},"severity_whitelist":["medium"],"version":1}'::jsonb, 'bd76320837618ef0542531ff5a2f7a7c536505e84deac8bcc74013179c247f7e', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

insert into prompt_tools (id, name, state, engine, parameters, definition, config_hash, version, is_builtin, created_by_user_id, created_at, updated_at)
values (gen_random_uuid(), 'builtin.ddl-implicit-type-conversion', 'enabled', 'all', '{}'::jsonb,
        '{"knowledge_text":"规则：WHERE条件字面值类型必须与列类型一致（旧DDLImplicitTypeConversion）。判定依据：调用schema_metadata工具核对涉及列类型，与语句字面值类型（字符串/数字）不一致时输出本发现；事实不可得不输出。","finding_template":{"category":"correctness","finding_key":"builtin.ddl-implicit-type-conversion","message":"WHERE条件字面值类型与列类型不一致（需schema_metadata核对）","severity":"medium","suggestion":"对齐字面值与列类型","title":"WHERE存在隐式类型转换风险"},"severity_whitelist":["medium"],"version":1}'::jsonb, '795f6ffbdd6a062ae21a8cb887a897bfb16de71aa29a849c127225134e0460fd', 1, true, '00000000-0000-0000-0000-000000000000', now(), now());

commit;
