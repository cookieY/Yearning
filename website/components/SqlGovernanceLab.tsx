import { useState } from 'react';

interface Scenario {
  id: string;
  tabZh: string;
  tabEn: string;
  tagZh: string;
  tagEn: string;
  summaryZh: string;
  summaryEn: string;
  inputSql: string;
  transformedLabelZh: string;
  transformedLabelEn: string;
  transformedPayload: string;
  stages: {
    nameZh: string;
    nameEn: string;
    detailZh: string;
    detailEn: string;
    tone: 'neutral' | 'pass' | 'block' | 'warn';
  }[];
  verdictTone: 'pass' | 'block' | 'warn';
  verdictCode: string;
  verdictZh: string;
  verdictEn: string;
  proofMetaZh: string;
  proofMetaEn: string;
}

const SCENARIOS: Scenario[] = [
  {
    id: 'agentic-precheck',
    tabZh: 'AI Agentic 预审',
    tabEn: 'AI Agentic Precheck',
    tagZh: '自主元数据探测 · 48 条规则',
    tagEn: 'Autonomous Probes · 48 Rules',
    summaryZh:
      '提交前，AI Agent 自主调用 5 项只读元数据工具探测真实表结构、索引与执行计划，结合 48 条内置规则与团队经验输出结构化阻断结论；调用公有云模型时在内存中自动完成脱敏与零泄漏断言。',
    summaryEn:
      'Before submission, the AI Agent autonomously calls 5 read-only database probes (tables, columns, indexes, stats, safe EXPLAIN) against 48 built-in rules and team knowledge, with automatic in-memory anonymization for cloud providers.',
    inputSql: `UPDATE billing.orders
SET amount_cents = 99900, remark = 'vip-promo-q4'
WHERE customer_phone = '13800138000'
  AND status = 'pending';`,
    transformedLabelZh: '双指纹折叠 + 只读探测证据（云端自动内存脱敏）',
    transformedLabelEn: 'Dual Fingerprint + Read-Only Probes (Auto Cloud Anonymization)',
    transformedPayload: `-- Step 1: Dual fingerprint collapses 1,240 homologous stmts -> 1 review group
-- Step 2: AI Agent autonomous read-only metadata tool calls:
-> get_table_schema(table_1)     => 14 cols, PK(id), est_rows = 4,820,000
-> list_indexes(table_1)         => idx_status_created(status, created_at)
-> safe_explain(UPDATE table_1)  => type=ALL, key=NULL, rows=4,820,000
-- Step 3: Cloud default track strips literals/identifiers (AssertZeroLeak=PASS)`,
    stages: [
      {
        nameZh: '词法解析与双指纹折叠',
        nameEn: 'AST Lexing & Dual Fingerprint',
        detailZh: 'Canonical + Dialect 双指纹将同构语句合并为 1 组（单单上限 100,000 语句 / 1,000 组）',
        detailEn: 'Canonical + Dialect fingerprints collapse homologous statements into 1 group (up to 100k stmts / 1,000 groups)',
        tone: 'pass',
      },
      {
        nameZh: '5 项只读元数据工具自主探测',
        nameEn: '5 Autonomous Read-Only Metadata Probes',
        detailZh: 'AI Agent 自动调用 list_indexes + safe_explain 实证 WHERE 列缺失索引、触发 482 万行全表扫描',
        detailEn: 'AI Agent autonomously calls list_indexes + safe_explain to prove the WHERE column lacks an index (4.82M full scan)',
        tone: 'warn',
      },
      {
        nameZh: '内置规则 + 自定义技能联合裁决',
        nameEn: 'Built-In Rules + Custom Skill Verdict',
        detailZh: '结合 48 条内置规则、自定义技能与团队经验库评估风险；公有云轨道自动完成内存脱敏与零泄漏断言',
        detailEn: 'Evaluates 48 built-in rules, custom skills, and team knowledge; cloud track automatically runs in-memory anonymization',
        tone: 'pass',
      },
      {
        nameZh: '确定性门禁阻断与授权还原',
        nameEn: 'Deterministic Gate Block & Restore',
        detailZh: '规则命中自动归为 Critical 阻断提交（大模型自由建议封顶 Medium 防误杀），为授权人还原真实表列名',
        detailEn: 'Rule violation deterministically blocks as Critical (free LLM notes cap at Medium); UI restores real identifiers for authorized reviewers',
        tone: 'block',
      },
    ],
    verdictTone: 'block',
    verdictCode: '2008 REVIEW_BLOCKED_BY_RISK',
    verdictZh: '门禁阻断：billing.orders.customer_phone 无索引且影响行数超过 max_affect_rows 阈值，必须修正后重跑预审。',
    verdictEn: 'Gate Blocked: billing.orders.customer_phone is unindexed and exceeds max_affect_rows threshold. Revise SQL and re-run precheck.',
    proofMetaZh: '5 项只读工具 · PlanHash 已绑定 · 云端 default / 内网 san 双轨隐私',
    proofMetaEn: '5 read-only probes · PlanHash bound · default cloud / san LAN tracks',
  },
  {
    id: 'database-ai-assistant',
    tabZh: '数据库 AI 助手',
    tabEn: 'Database AI Assistant',
    tagZh: '自然语言脱敏写 SQL → 提单',
    tagEn: 'Zero-Leak NL-to-SQL -> Order',
    summaryZh:
      '工作台右下角受控助手读取授权只读元数据，在单次请求内存中完成表列名与敏感业务文本脱敏，经方言 AST 校验还原草稿后，两阶段衔接正式 AI 预审与审批提单，绝不直接执行任何 SQL。',
    summaryEn:
      'Workspace assistant probes authorized read-only metadata, anonymizes identifiers and sensitive values in per-request memory, restores SQL via dialect AST checks, and bridges into formal AI Precheck and approval.',
    inputSql: `用户自然语言指令（目标数据源 prod-erp / erp）：
「给 warehouses 表插一条华东一号冷链仓记录，
编码 WH-EC-001，联系人张明，电话 13800138000，
帮我生成 SQL 并准备走变更工单。」`,
    transformedLabelZh: '单轮内存脱敏载荷 -> 方言 AST 校验还原草稿',
    transformedLabelEn: 'Per-Turn Anonymized Prompt -> Dialect AST Restored Draft',
    transformedPayload: `-- 1. 发往公有云模型的脱敏 Prompt（映射仅存单次请求内存，绝不落库）：
INSERT INTO schema_1.table_1 (col_1, col_2, col_3, col_4)
VALUES (:val_str_1, :val_str_2, :val_str_3, :val_str_4);

-- 2. 服务端方言 AST 解析 + 对象/占位符校验通过后安全还原：
INSERT INTO \`erp\`.\`warehouses\` (\`name\`, \`code\`, \`contact_name\`, \`phone\`)
VALUES ('华东一号冷链仓', 'WH-EC-001', '张明', '13800138000');
-- draft_id: drf_9c18 · revision: 1 · sql_hmac bound · 绝不直接执行`,
    stages: [
      {
        nameZh: '读写权限校验 + 只读元数据读取',
        nameEn: 'RBAC Check + Read-Only Metadata',
        detailZh: '核验持有 change_review 流程权（仅 Query 权禁止 DML 报 6002），拉取列类型/非空/默认值；缺必填列主动追问',
        detailEn: 'Verifies change_review grant (6002 on query-only), loads real columns/defaults; asks to clarify missing NOT NULL fields',
        tone: 'pass',
      },
      {
        nameZh: '单轮内存脱敏（RunMapper + TurnValueMapper）',
        nameEn: 'Per-Request Memory Anonymization',
        detailZh: '表列名与手机号/仓库名全部替换为类型化占位符并过 AssertNoLeak，请求结束立即释放内存映射',
        detailEn: 'Replaces table/column names and business values with typed tokens; memory map discarded immediately after turn',
        tone: 'pass',
      },
      {
        nameZh: '方言 AST 二次验真与结构化还原',
        nameEn: 'Dialect AST Verification & Restore',
        detailZh: '解析语法、校验授权表列与占位符绑定，用 IdentifierQuoter 安全还原，严禁字符串替换拼装',
        detailEn: 'Parses dialect AST, validates table/column scope and typed placeholders via IdentifierQuoter (fail-closed 6004)',
        tone: 'pass',
      },
      {
        nameZh: '两阶段衔接正式 AI 预审与审批提单',
        nameEn: 'Two-Stage Formal Precheck & Order',
        detailZh: '点击「执行正式预审」过 StartEphemeralRun 达 ready 后方可幂等 SubmitOrderV2，修改草稿即失效 (6005)',
        detailEn: 'Runs formal StartEphemeralRun -> ready before idempotent SubmitOrderV2; any draft edit invalidates binding (6005)',
        tone: 'warn',
      },
    ],
    verdictTone: 'pass',
    verdictCode: 'DRAFT READY -> FORMAL PRECHECK REQUIRED',
    verdictZh: '草稿校验还原完成：连 SELECT/EXPLAIN 都不执行；点击「执行正式 AI 预审」通过后一键提交多级审批单。',
    verdictEn: 'Draft AST-verified and restored: zero SQL executed directly. Click Run Formal AI Precheck to enter governed approval.',
    proofMetaZh: 'AES-256-GCM 加密会话 30 天 · 单用户 50 会话 · 超预算自动脱敏压缩',
    proofMetaEn: 'AES-256-GCM 30d history · 50 sessions/user · auto desensitized context compression',
  },
  {
    id: 'local-san-track',
    tabZh: '内网 SAN 原生轨道',
    tabEn: 'Local SAN Native Track',
    tagZh: 'san 本地模型轨道',
    tagEn: 'san Self-Hosted Track',
    summaryZh:
      '面向内网 vLLM / Ollama 部署的本地模型启用精简 Raw-SQL 原生契约，保留真实表结构语义与注释，并在架构层禁止跨轨道回退至公有云。',
    summaryEn:
      'Self-hosted vLLM/Ollama endpoints use compact versionless Raw-SQL prompts on RFC1918 LANs with strict cross-profile isolation preventing cloud fallback leaks.',
    inputSql: `ALTER TABLE ledger.settlements
ADD INDEX idx_merchant_day (merchant_id, settle_date);`,
    transformedLabelZh: '发往内网 vLLM（SAN 轨道）的运行契约',
    transformedLabelEn: 'Runtime Contract Sent to Internal vLLM (SAN Track)',
    transformedPayload: `[SAN_HEAD=ai-review]
endpoint: http://10.20.4.18:8000/v1 (RFC1918 · addrguard SSRF verified)
profile:  san · PrivacyContractHashSAN
guard:    non-anonymizing volume guard (>256-char literals truncated)
fallback: cloud 'default' fallback blocked from receiving raw payload`,
    stages: [
      {
        nameZh: 'Provider Profile 自动解析',
        nameEn: 'Provider Profile Resolution',
        detailZh: '依据配置或模型名路由至 san 原生轨道，同时兼容旧版 v3.1 模型走脱敏轨道',
        detailEn: 'Routes SAN / SQL-Audit-Nano models to native track while keeping legacy v3.1 on default anonymized track',
        tone: 'pass',
      },
      {
        nameZh: 'addrguard SSRF 与内网校验',
        nameEn: 'addrguard SSRF & LAN Validation',
        detailZh: '内网 RFC1918 允许 HTTP/HTTPS 直连，公网地址强制 HTTPS 且拦截 DNS 重绑定',
        detailEn: 'Permits HTTP/HTTPS on private RFC1918 LANs while enforcing HTTPS & anti-rebinding on public hosts',
        tone: 'pass',
      },
      {
        nameZh: '动态规则与内部经验热注入',
        nameEn: 'Hot-Updatable Skills & Knowledge',
        detailZh: '全局 / 数据源 / 表级 DBA 内部经验免重训即时注入，校验索引列数上限',
        detailEn: 'Global, datasource, and table DBA guidelines inject deterministically without retraining',
        tone: 'pass',
      },
      {
        nameZh: '30 分钟冻结预审快照',
        nameEn: '30-Minute Frozen Precheck Snapshot',
        detailZh: '预审通过生成 ready 快照，提交后冻结阶段链与执行人，含 admin 在内不可篡改',
        detailEn: 'Passing run yields a 30m ready snapshot; submission freezes stages and actors against all overrides',
        tone: 'pass',
      },
    ],
    verdictTone: 'pass',
    verdictCode: 'READY · 30m TTL',
    verdictZh: '预审通过：符合索引列数规范且无冗余索引冲突，已允许提交至多阶段冻结审批流。',
    verdictEn: 'Precheck Ready: Compliant with composite index rules and zero duplicate index conflicts. Ready for frozen approval.',
    proofMetaZh: 'PrivacyContractHashSAN · 零云端回退泄漏 · 单日全局 AI 预算受控',
    proofMetaEn: 'PrivacyContractHashSAN · zero cloud fallback leak · global daily AI budget enforced',
  },
  {
    id: 'in-process-ghost',
    tabZh: '进程内 gh-ost 改表',
    tabEn: 'In-Process gh-ost DDL',
    tagZh: '零外部二进制在线 DDL',
    tagEn: 'Zero-Binary Online DDL',
    summaryZh:
      'Go 进程内嵌 gh-ost 直接执行 MySQL 5.7/8.0 大表 ALTER TABLE，持单数据源排他租约、实时上报拷贝进度，并自动采集逆向 DDL 结构快照。',
    summaryEn:
      'Embedded Go gh-ost migrator runs MySQL 5.7/8.0 online ALTER TABLE without spawning binaries, under a single-datasource lease with live copy telemetry.',
    inputSql: `ALTER TABLE core.transactions
ADD COLUMN risk_score SMALLINT NOT NULL DEFAULT 0
AFTER status;`,
    transformedLabelZh: '进程内 gh-ost 执行参数与租约状态',
    transformedLabelEn: 'In-Process gh-ost Parameters & Lease Telemetry',
    transformedPayload: `mode:      ddl_execution_mode = "gh_ost" (pt-osc retired)
lease:     datasource/mysql-prod-core (exclusive single-order lock)
throttle:  chunk_size=1000 · dml_batch_size=100 · max_lag=1500ms
cut_over:  cut_over_lock_timeout_seconds=3 · retries=60
rollback:  captured pre/post schema -> DROP COLUMN risk_score`,
    stages: [
      {
        nameZh: '冻结执行人显式触发',
        nameEn: 'Explicit Frozen Executor Trigger',
        detailZh: '审批通过绝不自动执行；仅当前阶段冻结执行人可立即执行或预约 15 分钟窗口',
        detailEn: 'Approval never auto-executes; only frozen stage executors run immediately or schedule within a 15m grace window',
        tone: 'pass',
      },
      {
        nameZh: '排他租约与执行时段检查',
        nameEn: 'Exclusive Lease & Window Check',
        detailZh: '同数据源同一时刻仅允许 1 笔变更运行，并校验每周允许执行时段',
        detailEn: 'Only 1 order executes per datasource at a time, enforced alongside weekly maintenance windows',
        tone: 'pass',
      },
      {
        nameZh: '18 项类型化参数限流拷贝',
        nameEn: '18 Typed Throttle Parameters',
        detailZh: '内置 Hooks 与原子计数器实时回传拷贝百分比与主从延迟，支持安全中止',
        detailEn: 'In-process hooks stream live copy percentage and replica lag with clean abort handling',
        tone: 'warn',
      },
      {
        nameZh: '前后元数据自动入库加密',
        nameEn: 'Pre/Post Schema Sealed for Rollback',
        detailZh: '切换完成后自动生成逆向 ALTER TABLE 并以 AES-256-GCM 加密归档',
        detailEn: 'Cut-over seals inverse ALTER TABLE inside an AES-256-GCM envelope for governed rollback',
        tone: 'pass',
      },
    ],
    verdictTone: 'pass',
    verdictCode: 'EXECUTING · 84% COPIED',
    verdictZh: '在线改表进行中：无锁阻塞，当前从库延迟 120ms，完成后自动解锁下一阶段与受治理回滚草稿。',
    verdictEn: 'Online DDL active: zero blocking locks, 120ms replica lag, next stage and rollback draft unlock upon completion.',
    proofMetaZh: 'github.com/github/gh-ost/go/logic · 18 项参数可覆写 · SSE 实时推送',
    proofMetaEn: 'github.com/github/gh-ost/go/logic · 18 overridable flags · SSE realtime updates',
  },
  {
    id: 'binlog-free-rollback',
    tabZh: '无 Binlog 自动回滚',
    tabEn: 'Binlog-Free Rollback',
    tagZh: 'MySQL + PostgreSQL 跨引擎',
    tagEn: 'MySQL + PostgreSQL Native',
    summaryZh:
      '彻底告别 MySQL Binlog 抓取与 WAL 复制依赖，在原事务内精准采集行前像并生成逆向 SQL，经加密存储与受控 Reveal 后派生受治理回滚工单。',
    summaryEn:
      'Eliminates MySQL Binlog scraping and PostgreSQL WAL dependency via in-transaction before-image capture, encrypted evidence storage, and governed rollback drafts.',
    inputSql: `DELETE FROM app.order_archive
WHERE batch_tag = 'legacy_2024_q3'
  AND archived = true;`,
    transformedLabelZh: '事务内前像采样与加密逆向回滚证据',
    transformedLabelEn: 'In-Transaction Before-Image & Encrypted Rollback Evidence',
    transformedPayload: `-- Engine capture: PG 18 RETURNING OLD.* / MySQL SELECT ... FOR UPDATE
-- Captured rows:  1,842 / 100,000 hard limit (aborts forward TX if exceeded)
-- Storage:        order_rollback_evidences (AES-256-GCM AEAD envelope)
-- Derived SQL (after audited Reveal):
INSERT INTO app.order_archive (id, batch_tag, archived, payload_hash)
VALUES (91042, 'legacy_2024_q3', true, 'sha256:4a91...');`,
    stages: [
      {
        nameZh: '主键/唯一键与可逆性前置检查',
        nameEn: 'PK/UK & Reversibility Preflight',
        detailZh: '验证目标表具备主键或非空唯一键，确保每条受影响记录均可精确定位',
        detailEn: 'Verifies primary or non-null unique key so every mutated row is deterministically addressable',
        tone: 'pass',
      },
      {
        nameZh: '同事务前像采集（10 万行上限）',
        nameEn: 'Same-TX Before-Image Capture',
        detailZh: '开启回滚保护时若超 100,000 行立即中止并回滚正向事务，绝不裸奔提交',
        detailEn: 'Exceeding 100,000 rows aborts and rolls back the forward transaction immediately (fail-closed)',
        tone: 'pass',
      },
      {
        nameZh: 'AES-256-GCM 信封加密落盘',
        nameEn: 'AES-256-GCM Envelope Sealing',
        detailZh: '逆向 SQL 加密保存于元库，查看明文须显式触发带水印 SQL Reveal',
        detailEn: 'Inverse SQL is envelope-encrypted at rest; inspection requires audited watermarked SQL Reveal',
        tone: 'pass',
      },
      {
        nameZh: '派生受治理回滚工单',
        nameEn: 'Governed Rollback Order Derivation',
        detailZh: '一键生成关联回滚草稿，重新走完 AI 预审、多级审批与漂移校验后执行',
        detailEn: 'One click creates a linked rollback draft routed through formal AI precheck, approval, and drift checks',
        tone: 'pass',
      },
    ],
    verdictTone: 'pass',
    verdictCode: 'ROLLBACK_STATUS = AVAILABLE',
    verdictZh: '回滚证据完整就绪：覆盖 1,842 行前像，支持一键派生回滚工单（严禁跳过审批直接回滚）。',
    verdictEn: 'Rollback evidence ready: 1,842 before-image rows sealed. Derive a governed rollback draft in one click.',
    proofMetaZh: '无需 Binlog / WAL · 单单 100,000 行上限 · 7 态回滚状态机',
    proofMetaEn: 'Zero Binlog/WAL · 100,000-row cap · 7-state rollback lifecycle',
  },
];

export function SqlGovernanceLab({ isEn }: { isEn: boolean }) {
  const [activeIndex, setActiveIndex] = useState(0);
  const active = SCENARIOS[activeIndex];

  return (
    <div className="yn-lab">
      <div
        className="yn-lab-tabs"
        role="tablist"
        aria-label={isEn ? 'SQL Governance Scenarios' : 'SQL 治理场景切换'}
      >
        {SCENARIOS.map((item, idx) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={activeIndex === idx}
            className={`yn-lab-tab${activeIndex === idx ? ' is-active' : ''}`}
            onClick={() => setActiveIndex(idx)}
          >
            <span className="yn-lab-tab-index">0{idx + 1}</span>
            <span className="yn-lab-tab-label">{isEn ? item.tabEn : item.tabZh}</span>
          </button>
        ))}
      </div>

      <div className="yn-lab-body">
        <div className="yn-lab-left">
          <div className="yn-lab-meta-row">
            <span className="yn-lab-tag">{isEn ? active.tagEn : active.tagZh}</span>
            <span className="yn-lab-mono-note">{isEn ? active.proofMetaEn : active.proofMetaZh}</span>
          </div>

          <p className="yn-lab-summary">{isEn ? active.summaryEn : active.summaryZh}</p>

          <ol className="yn-stage-list">
            {active.stages.map((stage, sIdx) => (
              <li key={stage.nameEn} className="yn-stage-item">
                <div className={`yn-stage-dot tone-${stage.tone}`}>
                  <span>{sIdx + 1}</span>
                </div>
                <div className="yn-stage-copy">
                  <div className="yn-stage-title">{isEn ? stage.nameEn : stage.nameZh}</div>
                  <div className="yn-stage-detail">{isEn ? stage.detailEn : stage.detailZh}</div>
                </div>
              </li>
            ))}
          </ol>
        </div>

        <div className="yn-lab-right">
          <div className="yn-code-panel">
            <div className="yn-panel-head">
              <span>{isEn ? 'Developer Input SQL' : '提交的原始 SQL'}</span>
              <span className="yn-panel-badge">SQL</span>
            </div>
            <pre className="yn-pre">
              <code>{active.inputSql}</code>
            </pre>
          </div>

          <div className="yn-conduit-arrow" aria-hidden="true">
            <span className="yn-conduit-line" />
            <span className="yn-conduit-label">
              {isEn ? 'Yearning v4 Governance Engine' : 'Yearning v4 治理流水线处理'}
            </span>
            <span className="yn-conduit-line" />
          </div>

          <div className="yn-code-panel is-accent">
            <div className="yn-panel-head">
              <span>{isEn ? active.transformedLabelEn : active.transformedLabelZh}</span>
              <span className="yn-panel-badge is-crimson">VERIFIED</span>
            </div>
            <pre className="yn-pre">
              <code>{active.transformedPayload}</code>
            </pre>
          </div>

          <div className={`yn-verdict-bar tone-${active.verdictTone}`}>
            <div className="yn-verdict-code">{active.verdictCode}</div>
            <p className="yn-verdict-text">{isEn ? active.verdictEn : active.verdictZh}</p>
          </div>
        </div>
      </div>
    </div>
  );
}
