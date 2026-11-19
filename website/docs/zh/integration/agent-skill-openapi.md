# AI Agent Skill 与 OpenAPI v4 实战

Yearning v4 既可以通过零依赖的 **`yearning` Agent Skill** 直接接入 Claude Code、Codex CLI、Cursor、Antigravity 等 AI 编程助手（无需额外搭建 MCP Server），也可以通过标准 HTTP API 集成到企业 CI/CD 发布流水线。

## 1. 给 AI 编程助手装上 `yearning` Skill（30 秒接入）

1. 将 Yearning v4 仓库中的 `skills/yearning/` 目录复制到你的 Agent Skills 目录（例如 `~/.claude/skills/yearning/` 或项目 `.cursor/skills/yearning/`）。
2. 在运行终端配置登录环境变量（推荐用 `0600` 密码文件，避免明文密码留存在 Shell 历史里）：

```bash
chmod 600 ~/.yearning_password
export YEARNING_BASE_URL="https://yearning.example.com"
export YEARNING_USERNAME="dev_alice"
export YEARNING_PASSWORD_FILE="$HOME/.yearning_password"
# 若企业使用 LDAP 登录，追加：
# export YEARNING_AUTH_PROVIDER="ldap"
```

3. 直接用自然语言指挥 AI 助手（或手工跑 Python 助手脚本）：
   - *"帮我查一下 Yearning 里有哪些待我审批的工单"*
   - *"先查 `prod-mysql` 上 `orders` 表的索引结构，然后把下面这条 `ALTER TABLE` 放到 `prod-change` 流程里跑一次 AI 预审，通过后帮我创建工单"*

## 2. `yearning_api.py` 高频命令速查（零外部依赖，Python 3 标准库即可运行）

脚本自动管理 `yearning_session` Cookie、双提交 `X-CSRF-Token`、UUIDv4 `Idempotency-Key`、`If-Match` 乐观锁以及预审轮询：

```bash
SKILL="skills/yearning/scripts/yearning_api.py"

# 1. 查看待我审批 / 待我执行 / 我提交的工单
python3 "$SKILL" me
python3 "$SKILL" orders --pending-my-approval
python3 "$SKILL" orders --pending-my-execution

# 2. 查看工单元数据 vs. 显式审计查看明文 SQL / 回滚 SQL
python3 "$SKILL" order <order_id>
python3 "$SKILL" order <order_id> --reveal-sql --reveal-rollback-sql

# 3. 发现授权流程 -> 查目标表元数据 -> 跑 AI 预审 -> 绑定 run_id 提单
python3 "$SKILL" flows
python3 "$SKILL" metadata --flow-id <flow_id> --datasource-id <ds_id> --schema-name app --table-name users
python3 "$SKILL" precheck \
  --flow-id <flow_id> \
  --sql "ALTER TABLE app.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"
python3 "$SKILL" submit-order \
  --flow-id <flow_id> \
  --run-id <run_id> \
  --title "Add status column to app.users" \
  --sql "ALTER TABLE app.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"

# 4. 审批、执行（支持指定 gh_ost）与派生回滚草稿
python3 "$SKILL" decide-order <order_id> approve --comment "Index verified, LGTM"
python3 "$SKILL" execute-order <order_id> --ddl-execution-mode gh_ost
python3 "$SKILL" rollback-draft <order_id> --reveal-sql

# 5. 受控只读查询与分页
python3 "$SKILL" query-context
python3 "$SKILL" query \
  --datasource-id <ds_id> --schema-name app \
  --sql "SELECT id, email, created_at FROM users LIMIT 20"

# 6. 管理员 Top SQL 性能诊断与 AI 根因报告
python3 "$SKILL" diagnostics overview --datasource-id <ds_id> --sort-by total_exec_time_ms
python3 "$SKILL" diagnostics ai-report --datasource-id <ds_id> --statement-id <stmt_id>
```

## 3. CI/CD 直调 `/api/v4/*` 示例（`curl` 完整登录 + CSRF + 订阅 SSE）

```bash
BASE="https://yearning.example.com"

# 1. 登录并保存 Cookie（含 yearning_session 与 yearning_csrf）
curl -fsS -c /tmp/yn.cookie -X POST "$BASE/api/v4/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"ci_bot","password":"YOUR_PASSWORD"}'

# 提取双提交 CSRF Token
CSRF=$(awk '$6=="yearning_csrf"{print $7}' /tmp/yn.cookie)

# 2. 调用写接口必须带 X-CSRF-Token 与 UUIDv4 Idempotency-Key
curl -fsS -b /tmp/yn.cookie -X POST "$BASE/api/v4/review-runs" \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"flow_id":"<flow_id>","sql":"ALTER TABLE app.users ADD INDEX idx_status (status);"}'

# 3. 订阅实时 SSE 领域事件流（每 25 秒收到 : keepalive 心跳，无 SQL 明文）
curl -N -b /tmp/yn.cookie -H "Accept: text/event-stream" "$BASE/api/v4/events/stream"
```

完整 179 个接口 Schema 与请求/响应定义见仓库 `api/openapi/yearning-v4.yaml`，可直接用于生成 Go / TypeScript / Python / Java 强类型 SDK。
