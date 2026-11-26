import { useState } from 'react';
import { useLang, withBase } from '@rspress/core/runtime';
import { HeroIsometricCanvas } from './HeroIsometricCanvas';
import { SqlGovernanceLab } from './SqlGovernanceLab';
import { ProductShowcase } from './ProductShowcase';

const QUICK_CMD =
  'git clone --single-branch --branch v4 https://github.com/cookieY/Yearning.git && cd Yearning && docker compose up -d';

const AGENT_CMD = `export YEARNING_BASE_URL="http://localhost:8080"
export YEARNING_USERNAME="admin"
export YEARNING_PASSWORD_FILE="/path/to/password"

# Discover granted change flows & run AI Agentic precheck
python3 skills/yearning/scripts/yearning_api.py flows
python3 skills/yearning/scripts/yearning_api.py precheck \\
  --flow-id <flow_id> \\
  --sql "ALTER TABLE demo.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"`;

const DEPLOY_SNIPPETS = {
  compose: `git clone --single-branch --branch v4 https://github.com/cookieY/Yearning.git
cd Yearning
docker compose up -d
# Open http://<host-ip>:8080  (admin / Yearning@2026!)`,
  k8s: `# Apply hardened Distroless multi-replica manifests
kubectl apply -f backend/deploy/k8s/network-policy.yaml
kubectl apply -f backend/deploy/k8s/migration-workloads.yaml
kubectl apply -f backend/deploy/k8s/control-plane.yaml`,
  migrate: `# Versioned downtime cutover from Yearning v3 (MySQL) -> v4 (PostgreSQL)
./Yearning migrate preflight --config migration-config.json
./Yearning migrate dry-run   --config migration-config.json --report-dir ./reports
./Yearning serve --migration-mode --config migration-config.json
./Yearning migrate apply     --config migration-config.json --run-id <approved_uuid>`,
} as const;

const ENGINES = [
  {
    name: 'MySQL 5.7+ / 8.0 / 8.4',
    modeZh: '原生 MySQL',
    modeEn: 'Native MySQL',
    ghost: true,
    diag: 'performance_schema',
  },
  {
    name: 'PostgreSQL 14+',
    modeZh: '原生 PostgreSQL',
    modeEn: 'Native PostgreSQL',
    ghost: false,
    diag: 'pg_stat_statements',
  },
  {
    name: 'TiDB',
    modeZh: 'MySQL 兼容模式',
    modeEn: 'MySQL Mode',
    ghost: false,
    diag: 'performance_schema',
  },
  {
    name: 'OceanBase',
    modeZh: 'MySQL 兼容模式',
    modeEn: 'MySQL Mode',
    ghost: false,
    diag: 'MySQL Digest',
  },
  {
    name: 'PolarDB',
    modeZh: 'MySQL & PG 模式',
    modeEn: 'MySQL & PG Modes',
    ghost: false,
    diag: 'Native Digest',
  },
];

export function YearningHome() {
  const lang = useLang();
  const isEn = lang.startsWith('en');
  const prefix = isEn ? '/en' : '';

  const [copiedKey, setCopiedKey] = useState('');
  const [deployTab, setDeployTab] = useState<'compose' | 'k8s' | 'migrate'>('compose');

  const copyText = async (key: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopiedKey(key);
      window.setTimeout(() => {
        setCopiedKey((curr) => (curr === key ? '' : curr));
      }, 1800);
    } catch {
      setCopiedKey('');
    }
  };

  return (
    <div className="yn-home">
      {/* 1. HERO: AI Agentic SQL Review Platform Identity */}
      <section className="yn-hero">
        <HeroIsometricCanvas />
        <div className="yn-hero-vignette" aria-hidden="true" />

        <div className="yn-container yn-hero-inner">
          <div className="yn-hero-copy">
            <div className="yn-hero-chips">
              <div className="yn-version-chip">
                <span className="yn-pulse-dot" aria-hidden="true" />
                <span>
                  {isEn
                    ? 'YEARNING V4 · AI AGENTIC SQL REVIEW PLATFORM'
                    : 'YEARNING V4 · AI AGENTIC SQL 审核平台'}
                </span>
              </div>
              <span className="yn-beta-chip">
                {isEn ? 'PUBLIC BETA' : '公测版本 · PUBLIC BETA'}
              </span>
            </div>

            <h1 className="yn-hero-title">
              {isEn ? (
                <>
                  The <span className="yn-title-accent">AI Agentic</span>
                  <br />
                  SQL Review Platform.
                </>
              ) : (
                <>
                  <span className="yn-title-accent">AI Agentic</span> 驱动的
                  <br />
                  新一代 SQL 审核平台
                </>
              )}
            </h1>

            <p className="yn-hero-sub">
              {isEn
                ? 'Autonomous metadata-probing AI SQL review paired with a scoped Database AI Assistant, frozen multi-stage approvals, in-process gh-ost, cross-engine rollback, and Top-SQL diagnostics.'
                : '以自主探测表结构与执行计划的 AI Agentic 审核引擎为核心，集成数据库 AI 助手、多阶段冻结审批、进程内 gh-ost 与跨引擎自动回滚，让 SQL 从编写、审核到上线一气呵成。'}
            </p>

            <div className="yn-hero-actions">
              <a
                href="https://demo.yearning.io"
                target="_blank"
                rel="noreferrer"
                className="yn-btn-primary"
              >
                {isEn ? 'Live Demo ↗' : '在线演示 ↗'}
              </a>
              <a href={withBase(`${prefix}/guide/quick-start`)} className="yn-btn-secondary">
                {isEn ? 'Deploy Public Beta' : '3 分钟部署公测版'}
              </a>
              <a href={withBase(`${prefix}/features/ai-precheck`)} className="yn-btn-ghost">
                {isEn ? 'AI Agentic Precheck' : 'AI Agentic 预审'}
              </a>
            </div>

            <div className="yn-cmd-strip">
              <code className="yn-cmd-code">{QUICK_CMD}</code>
              <button
                type="button"
                className="yn-copy-btn"
                onClick={() => copyText('hero', QUICK_CMD)}
              >
                {copiedKey === 'hero' ? (isEn ? 'Copied' : '已复制') : isEn ? 'Copy' : '复制'}
              </button>
            </div>

            <div className="yn-beta-notice" role="status">
              <span className="yn-beta-notice-tag">
                {isEn ? 'BETA NOTICE' : '公测提示'}
              </span>
              <p>
                {isEn ? (
                  <>
                    Yearning v4.0 is currently in <strong>Public Beta</strong>. Please evaluate in
                    staging or test environments first and keep backups before any cutover.{' '}
                    <a
                      href="https://github.com/cookieY/Yearning/issues"
                      target="_blank"
                      rel="noreferrer"
                    >
                      Share feedback on GitHub Issues →
                    </a>
                  </>
                ) : (
                  <>
                    Yearning v4.0 当前处于 <strong>公测阶段（Public Beta）</strong>
                    ，功能与细节仍在持续迭代打磨中。建议优先在测试或预发环境试用验证，正式接入生产前务必做好数据备份。{' '}
                    <a
                      href="https://github.com/cookieY/Yearning/issues"
                      target="_blank"
                      rel="noreferrer"
                    >
                      前往 GitHub Issues 反馈建议 →
                    </a>
                  </>
                )}
              </p>
            </div>
          </div>

          <dl className="yn-telemetry-bar">
            <div className="yn-telemetry-cell">
              <dt>{isEn ? 'Agentic Probes & Rules' : 'AI 探测与内置规则'}</dt>
              <dd>
                5 <span className="yn-unit">{isEn ? 'tools · 48 rules' : '项工具 · 48 条规则'}</span>
              </dd>
            </div>
            <div className="yn-telemetry-cell">
              <dt>{isEn ? 'Max SQL Batch' : '单工单批量上限'}</dt>
              <dd>
                100,000 <span className="yn-unit">{isEn ? 'stmts / 32 MiB' : '条 · 32 MiB'}</span>
              </dd>
            </div>
            <div className="yn-telemetry-cell">
              <dt>{isEn ? 'Approval Chain' : '冻结审批链路'}</dt>
              <dd>
                1..N <span className="yn-unit">{isEn ? 'stages · 1..10 steps' : '阶段 · 1..10 级'}</span>
              </dd>
            </div>
            <div className="yn-telemetry-cell">
              <dt>{isEn ? 'Rollback Capture' : '无 Binlog 回滚上限'}</dt>
              <dd>
                100,000 <span className="yn-unit">{isEn ? 'rows in TX' : '行事务前像'}</span>
              </dd>
            </div>
          </dl>
        </div>
      </section>

      {/* 2. YEARNING 4.0 SIX CORE CAPABILITIES SPOTLIGHT */}
      <section className="yn-section yn-core-band">
        <div className="yn-container">
          <div className="yn-section-head">
            <h2 className="yn-h2">
              {isEn ? 'Yearning 4.0 Core Capabilities' : 'Yearning 4.0 六大核心能力'}
            </h2>
            <p className="yn-section-lead">
              {isEn
                ? 'Rebuilt from scratch in Go and React 19 on PostgreSQL, uniting an autonomous Agentic SQL review engine, a Database AI Assistant, and deterministic production execution.'
                : '基于 Go 模块化单体、React 19 与 PostgreSQL 彻底重构，围绕「AI Agentic 自主审核 + 数据库 AI 助手 + 冻结审批执行 + 跨引擎回滚与诊断」打造完整 SQL 变更闭环。'}
            </p>
          </div>

          <div className="yn-core-grid">
            <article className="yn-core-card is-featured">
              <div className="yn-core-top">
                <span className="yn-core-badge is-crimson">4.0 CORE · AGENTIC REVIEW</span>
                <span className="yn-core-metric">5 probes · 48 rules</span>
              </div>
              <h3>{isEn ? 'AI Agentic SQL Review Engine' : 'AI Agentic 自主探测 SQL 审核引擎'}</h3>
              <p>
                {isEn
                  ? 'Autonomously invokes 5 read-only database tools (tables, columns, indexes, stats, safe EXPLAIN) across up to 100,000 statements / 32 MiB, combining 48 built-in rules, custom skills, and team knowledge with dual-track cloud/LAN privacy.'
                  : '告别静态正则死规则：AI 在审核回路中自主调用 5 项只读元数据工具（查表/列/索引/统计信息/安全 EXPLAIN），结合 48 条内置规则、自定义技能与团队经验库完成最高 100,000 条批量预审，支持云端脱敏与内网本地模型双轨接入。'}
              </p>
              <a href={withBase(`${prefix}/features/ai-precheck`)} className="yn-core-link">
                {isEn ? 'AI Precheck & 48 Rules Guide →' : '查看 AI 预审与 48 条规则实战 →'}
              </a>
            </article>

            <article className="yn-core-card is-featured">
              <div className="yn-core-top">
                <span className="yn-core-badge is-crimson">4.0 CORE · AI ASSISTANT</span>
                <span className="yn-core-metric">NL-to-SQL · AST Verified</span>
              </div>
              <h3>{isEn ? 'Scoped Database AI Assistant' : '受控数据库 AI 助手（咨询 · 写 SQL · 优化）'}</h3>
              <p>
                {isEn
                  ? 'Floating workspace assistant grounded in authorized read-only metadata: answers platform docs, drafts SELECT/DML/DDL from natural language with dialect AST verification, and bridges straight into formal precheck and change orders.'
                  : '常驻工作台右下角，基于真实授权元数据提供平台使用咨询、自然语言生成 SELECT/DML/DDL 与慢 SQL 改写优化；方言 AST 校验把关、缺必填列主动追问，两阶段直连正式预审提单，绝不越权执行。'}
              </p>
              <a href={withBase(`${prefix}/features/ai-assistant`)} className="yn-core-link">
                {isEn ? 'Database AI Assistant Handbook →' : '阅读数据库 AI 助手实战指南 →'}
              </a>
            </article>

            <article className="yn-core-card">
              <div className="yn-core-top">
                <span className="yn-core-badge">GOVERNANCE</span>
                <span className="yn-core-metric">1..N stages · 18 gh-ost params</span>
              </div>
              <h3>{isEn ? 'Frozen Multi-Stage Approvals & In-Process gh-ost' : '冻结式多阶段审批与进程内 gh-ost'}</h3>
              <p>
                {isEn
                  ? 'Freezes rules, stages, actors, and snapshots at submission with zero admin override. Embeds Go gh-ost in-process (18 parameters, pt-osc retired) with exclusive datasource leases and result_unknown verification.'
                  : '提交瞬间冻结规则、阶段、审批人与执行人（含 admin 均不可越权）；内嵌 Go 进程内 gh-ost 在线改表（18 项参数可覆写，退役 pt-osc），带水印 SQL Reveal 与断网 result_unknown 人工核验。'}
              </p>
              <a href={withBase(`${prefix}/features/change-orders`)} className="yn-core-link">
                {isEn ? 'Approval & gh-ost Tuning →' : '查看审批与 gh-ost 调优手册 →'}
              </a>
            </article>

            <article className="yn-core-card">
              <div className="yn-core-top">
                <span className="yn-core-badge">RECOVERY</span>
                <span className="yn-core-metric">0 Binlog/WAL · 100k rows</span>
              </div>
              <h3>{isEn ? 'Binlog-Free Cross-Engine Automatic Rollback' : '跨引擎无 Binlog/WAL 自动 SQL 回滚'}</h3>
              <p>
                {isEn
                  ? 'Captures in-transaction DML before-images (up to 100k rows fail-closed) and reversible DDL/gh-ost schema snapshots across MySQL and PostgreSQL, encrypted at rest and derived into governed rollback orders.'
                  : '无需开启 Binlog 或 WAL 逻辑复制，MySQL 与 PostgreSQL 同事务采集最多 100,000 行前像（超限自动回滚正向事务）及 DDL/gh-ost 逆向快照，AES-256-GCM 加密并一键派生受治理回滚工单。'}
              </p>
              <a href={withBase(`${prefix}/features/sql-rollback`)} className="yn-core-link">
                {isEn ? 'Rollback Protection Guide →' : '查看自动回滚与派生工单实战 →'}
              </a>
            </article>

            <article className="yn-core-card">
              <div className="yn-core-top">
                <span className="yn-core-badge">DATA ACCESS</span>
                <span className="yn-core-metric">Server Masking · SSE Lock</span>
              </div>
              <h3>{isEn ? 'Governed Read-Only Query & PII Masking' : '受控只读查询与服务端敏感列脱敏'}</h3>
              <p>
                {isEn
                  ? 'Time-bounded Query Grants with read-only AST guards, server-side PII masking before rows leave the backend, 6h HMAC cursor pagination, controlled CSV/JSONL/XLSX export, and instant SSE revocation lock.'
                  : '限时 Query Grant 审批、方言只读 AST 拦截写与加锁读、敏感列在离开服务端前即完成脱敏、6 小时 HMAC 游标分页与受控导出，撤销授权经 SSE 毫秒级锁定前端查询台。'}
              </p>
              <a href={withBase(`${prefix}/features/query-workspace`)} className="yn-core-link">
                {isEn ? 'Query Workspace Guide →' : '查看只读查询与脱敏指南 →'}
              </a>
            </article>

            <article className="yn-core-card">
              <div className="yn-core-top">
                <span className="yn-core-badge">OBSERVABILITY & AGENT</span>
                <span className="yn-core-metric">6 Charts · SKILL.md</span>
              </div>
              <h3>{isEn ? 'Top-SQL Diagnostics & AI Agent Skill' : '6 图 Top SQL 性能诊断与 Agent Skill'}</h3>
              <p>
                {isEn
                  ? 'Read-only performance_schema / pg_stat_statements 6-chart overview with Brush drill-down and 4-section AI root-cause reports converting index DDL straight to orders, plus zero-dependency skills/yearning for Claude/Codex.'
                  : '复用只读连接池采集 performance_schema / pg_stat_statements，6 图联动 + Brush 框选毛刺 + 四段式 AI 根因报告一键建索引工单；内置零依赖 skills/yearning 直连 /api/v4/*。'}
              </p>
              <a href={withBase(`${prefix}/features/sql-diagnostics`)} className="yn-core-link">
                {isEn ? 'Diagnostics & Agent Skill →' : '查看性能诊断与 Agent 集成 →'}
              </a>
            </article>
          </div>
        </div>
      </section>

      {/* 3. INTERACTIVE AI AGENTIC GOVERNANCE PIPELINE LAB */}
      <section className="yn-section yn-section-alt">
        <div className="yn-container">
          <div className="yn-section-head">
            <h2 className="yn-h2">
              {isEn ? 'Inspect the AI Agentic Review & Governance Pipeline Live' : '实景体验 AI Agentic 审核与治理流水线'}
            </h2>
            <p className="yn-section-lead">
              {isEn
                ? 'AI agents autonomously probe metadata and synthesize evidence; deterministic governance controls every gate, approval, lease, and rollback. Switch all five scenarios below.'
                : 'AI Agent 自主探测元数据并综合风险证据，结构化契约掌控每一次门禁、审批、执行租约与回滚闭环。点选下方五个典型场景查看真实处理轨迹。'}
            </p>
          </div>
          <SqlGovernanceLab isEn={isEn} />
        </div>
      </section>

      {/* 4. AUTHENTIC YEARNING V4 INTERFACE SHOWCASE */}
      <section className="yn-section">
        <div className="yn-container">
          <div className="yn-section-head">
            <h2 className="yn-h2">
              {isEn ? 'Built for Daily DBA & Engineering Craft' : '面向 DBA 与研发团队的真实工作台'}
            </h2>
            <p className="yn-section-lead">
              {isEn
                ? 'Rewritten from Vue to React 19, TypeScript, and shadcn/ui with real-time SSE state updates, watermarked SQL Reveal, and 6-chart Top-SQL diagnostics.'
                : '全面告别旧版 Vue 与 RPC 依赖，基于 React 19 与 shadcn/ui 重构，内置实时 SSE 状态推送、水印 SQL Reveal 与 6 图联动性能诊断。'}
            </p>
          </div>
          <ProductShowcase isEn={isEn} />
        </div>
      </section>

      {/* 5. QUICK PLAYBOOK LAUNCHPAD: Concrete Deploy + Admin + Usage Entry Points */}
      <section className="yn-section yn-section-alt yn-playbook-band">
        <div className="yn-container">
          <div className="yn-section-head">
            <h2 className="yn-h2">
              {isEn ? 'Start by Role: Deploy, Configure, Operate' : '按角色快速直达：从部署落地到日常使用'}
            </h2>
            <p className="yn-section-lead">
              {isEn
                ? 'Skip marketing fluff. Jump straight into runnable configs, least-privilege DB grant SQL, 48 built-in rules, and daily DBA workflows.'
                : '直接拿走可复制的部署编排、Nginx SSE 反代配置、目标库最小权限建权 SQL 与研发/DBA 实操步骤。'}
            </p>
          </div>

          <div className="yn-playbook-grid">
            <div className="yn-playbook-col">
              <div className="yn-playbook-head">
                <span className="yn-playbook-num">01</span>
                <h3>{isEn ? 'Deployment & Cutover' : '安装部署与升级'}</h3>
              </div>
              <ul className="yn-playbook-links">
                <li>
                  <a href={withBase(`${prefix}/guide/quick-start`)}>
                    {isEn ? 'Docker Compose + Nginx HTTPS/SSE Proxy' : 'Docker Compose 一键部署 + Nginx SSE 反代配置'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/operations/binary-deploy`)}>
                    {isEn ? 'Binary + Standalone PG 3-Role + systemd' : '单二进制 + 独立 PG 三角色授权 + systemd 托管'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/operations/kubernetes`)}>
                    {isEn ? 'Kubernetes 5 Secrets + Multi-Replica Rollout' : 'Kubernetes 5 份 Secret 准备与多副本滚动发布'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/operations/migration-v3-to-v4`)}>
                    {isEn ? 'Yearning v3 -> v4 Downtime Migration' : 'Yearning v3 → v4 停机迁移与 SecretKey 重加密'}
                  </a>
                </li>
              </ul>
            </div>

            <div className="yn-playbook-col">
              <div className="yn-playbook-head">
                <span className="yn-playbook-num">02</span>
                <h3>{isEn ? 'Admin 15-Min Setup' : '管理员 15 分钟初始化'}</h3>
              </div>
              <ul className="yn-playbook-links">
                <li>
                  <a href={withBase(`${prefix}/admin/first-time-setup`)}>
                    {isEn ? '6-Step First-Boot Setup Checklist' : '首次登录 6 步初始化清单（避坑 1001 报错）'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/admin/datasources-and-grants`)}>
                    {isEn ? 'MySQL & PostgreSQL Least-Privilege Grant SQL' : 'MySQL / PostgreSQL 最小权限账号建权 SQL'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/admin/review-engine`)}>
                    {isEn ? 'Cloud/LAN AI Providers & 48 Built-In Rules' : '公有云/内网 vLLM 接入与 48 条内置审核规则'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/admin/governance`)}>
                    {isEn ? 'AD/OpenLDAP, OIDC PKCE & IM Templates' : 'AD/OpenLDAP、OIDC 回调与钉钉/飞书自定义模板'}
                  </a>
                </li>
              </ul>
            </div>

            <div className="yn-playbook-col">
              <div className="yn-playbook-head">
                <span className="yn-playbook-num">03</span>
                <h3>{isEn ? 'Daily Dev & DBA Usage' : '研发与 DBA 日常使用'}</h3>
              </div>
              <ul className="yn-playbook-links">
                <li>
                  <a href={withBase(`${prefix}/features/ai-precheck`)}>
                    {isEn ? 'AI Agentic Precheck, Bulk .sql & Fixing 2008' : '提单、大批量 .sql 导入与处理 2008 阻断'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/features/ai-assistant`)}>
                    {isEn ? 'Database AI Assistant: NL-to-SQL & Safe Order Bridge' : '数据库 AI 助手：自然语言写 SQL 与两阶段提单'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/features/change-orders`)}>
                    {isEn ? 'Approvals, 18 gh-ost Params & result_unknown' : '审批、18 项 gh-ost 调优与断网人工核验 SOP'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/features/sql-rollback`)}>
                    {isEn ? 'Rollback Protection & Deriving Rollback Orders' : '开启回滚保护与一键派生回滚工单实操'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/features/sql-diagnostics`)}>
                    {isEn ? '6-Chart Top-SQL Diagnostics & AI Root Cause' : '开启慢 SQL 诊断、6 图下钻与 AI 根因报告'}
                  </a>
                </li>
              </ul>
            </div>

            <div className="yn-playbook-col">
              <div className="yn-playbook-head">
                <span className="yn-playbook-num">04</span>
                <h3>{isEn ? 'Security, Ops & Automation' : '安全合规、排障与集成'}</h3>
              </div>
              <ul className="yn-playbook-links">
                <li>
                  <a href={withBase(`${prefix}/guide/security-and-ai-trust`)}>
                    {isEn ? 'Enterprise Security & AI Trust Whitepaper' : '企业级安全架构：防泄露 · 零侵入 · 抗幻觉'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/operations/cli-and-config`)}>
                    {isEn ? 'Locked Admin Reset & All YEARNING_* Env Vars' : 'admin 锁死重置密码命令与全量环境变量表'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/reference/error-codes`)}>
                    {isEn ? 'Startup Faults & 10xx–70xx Error Code Lookup' : '启动报错速查与 10xx–70xx 业务错误码表'}
                  </a>
                </li>
                <li>
                  <a href={withBase(`${prefix}/integration/agent-skill-openapi`)}>
                    {isEn ? 'Claude/Codex Agent Skill & curl API Recipes' : '接入 Claude/Codex Skill 与 curl 调 API 示例'}
                  </a>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </section>

      {/* 6. MULTI-ENGINE COMPATIBILITY MATRIX */}
      <section className="yn-section">
        <div className="yn-container">
          <div className="yn-section-head">
            <h2 className="yn-h2">
              {isEn ? 'Five Database Families, One Governance Plane' : '五大数据库引擎矩阵，一套治理基线'}
            </h2>
            <p className="yn-section-lead">
              {isEn
                ? 'Strict separation between read-only review/query credentials and read-write execution credentials, sealed with AES-256-GCM AEAD and verified TLS.'
                : '统一分离只读（预审/查询）与读写（执行）凭据，全量经 AES-256-GCM AEAD 加密存储并强制校验 TLS 证书。'}
            </p>
          </div>

          <div className="yn-matrix-wrap">
            <table className="yn-matrix">
              <thead>
                <tr>
                  <th>{isEn ? 'Database Engine' : '数据库引擎'}</th>
                  <th>{isEn ? 'Mode' : '兼容模式'}</th>
                  <th>{isEn ? 'AI Precheck' : 'AI 预审'}</th>
                  <th>{isEn ? 'Execution' : '安全执行'}</th>
                  <th>{isEn ? 'In-Process gh-ost' : '进程内 gh-ost'}</th>
                  <th>{isEn ? 'Auto Rollback' : '自动回滚'}</th>
                  <th>{isEn ? 'Governed Query' : '受控查询'}</th>
                  <th>{isEn ? 'SQL Diagnostics' : '性能诊断数据源'}</th>
                </tr>
              </thead>
              <tbody>
                {ENGINES.map((row) => (
                  <tr key={row.name}>
                    <td className="yn-matrix-name">{row.name}</td>
                    <td className="yn-matrix-muted">{isEn ? row.modeEn : row.modeZh}</td>
                    <td>
                      <span className="yn-check is-yes">{isEn ? 'Yes' : '支持'}</span>
                    </td>
                    <td>
                      <span className="yn-check is-yes">{isEn ? 'Yes' : '支持'}</span>
                    </td>
                    <td>
                      <span className={`yn-check ${row.ghost ? 'is-yes' : 'is-na'}`}>
                        {row.ghost ? (isEn ? 'Native' : '原生内嵌') : 'N/A'}
                      </span>
                    </td>
                    <td>
                      <span className="yn-check is-yes">{isEn ? 'Yes' : '支持'}</span>
                    </td>
                    <td>
                      <span className="yn-check is-yes">{isEn ? 'Yes' : '支持'}</span>
                    </td>
                    <td className="yn-matrix-mono">{row.diag}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {/* 7. ARCHITECTURE & EXTERNAL AI AGENT SKILL */}
      <section className="yn-section yn-section-alt">
        <div className="yn-container yn-split-band">
          <div className="yn-arch-col">
            <h2 className="yn-h2">
              {isEn ? 'Stateless Go Monolith. AI-Agent Ready.' : '无状态 Go 单体，原生拥抱外部 AI Agent'}
            </h2>
            <p className="yn-arch-desc">
              {isEn
                ? 'Zero RPC sidecars and zero Pod-local state. Every API obeys OpenAPI v4, double-submit CSRF, Idempotency-Key, If-Match optimistic locks, and SSE realtime events. Connect Claude Code, Codex, or Antigravity via the zero-dependency yearning Skill without deploying an MCP server.'
                : '彻底移除 RPC 与本地 Pod 状态，依托 PostgreSQL 实现 Kubernetes 多副本水平扩展。内置 OpenAPI v4、实时 SSE 事件总线与零依赖 yearning Agent Skill，让 Claude Code、Codex、Antigravity 等助手在严格门禁下直接协作。'}
            </p>

            <div className="yn-arch-schematic" aria-label="Yearning v4 Architecture Schematic">
              <svg viewBox="0 0 560 220" fill="none" className="yn-arch-svg">
                <rect x="10" y="20" width="136" height="58" rx="8" className="yn-svg-box" />
                <text x="78" y="45" textAnchor="middle" className="yn-svg-title">
                  Web UI / Agent
                </text>
                <text x="78" y="63" textAnchor="middle" className="yn-svg-sub">
                  React 19 · SKILL.md
                </text>

                <rect x="204" y="20" width="152" height="58" rx="8" className="yn-svg-box is-accent" />
                <text x="280" y="45" textAnchor="middle" className="yn-svg-title">
                  Go Control Plane
                </text>
                <text x="280" y="63" textAnchor="middle" className="yn-svg-sub">
                  AST · Agentic AI
                </text>

                <rect x="414" y="20" width="136" height="58" rx="8" className="yn-svg-box" />
                <text x="482" y="45" textAnchor="middle" className="yn-svg-title">
                  Target Clusters
                </text>
                <text x="482" y="63" textAnchor="middle" className="yn-svg-sub">
                  MySQL · PG · TiDB
                </text>

                <rect x="10" y="136" width="160" height="62" rx="8" className="yn-svg-box" />
                <text x="90" y="162" textAnchor="middle" className="yn-svg-title">
                  PostgreSQL Meta
                </text>
                <text x="90" y="180" textAnchor="middle" className="yn-svg-sub">
                  3 Roles · AEAD Keyring
                </text>

                <rect x="204" y="136" width="152" height="62" rx="8" className="yn-svg-box" />
                <text x="280" y="162" textAnchor="middle" className="yn-svg-title">
                  AI Providers
                </text>
                <text x="280" y="180" textAnchor="middle" className="yn-svg-sub">
                  default vs san LAN
                </text>

                <rect x="390" y="136" width="160" height="62" rx="8" className="yn-svg-box" />
                <text x="470" y="162" textAnchor="middle" className="yn-svg-title">
                  Outbox &amp; SSE
                </text>
                <text x="470" y="180" textAnchor="middle" className="yn-svg-sub">
                  Audit · IM · Events
                </text>

                <line x1="146" y1="49" x2="204" y2="49" className="yn-svg-wire is-accent" />
                <line x1="356" y1="49" x2="414" y2="49" className="yn-svg-wire is-accent" />
                <line x1="248" y1="78" x2="120" y2="136" className="yn-svg-wire" />
                <line x1="280" y1="78" x2="280" y2="136" className="yn-svg-wire is-accent" />
                <line x1="312" y1="78" x2="442" y2="136" className="yn-svg-wire" />
              </svg>
            </div>

            <div className="yn-arch-links">
              <a href={withBase(`${prefix}/integration/agent-skill-openapi`)} className="yn-text-link">
                {isEn
                  ? 'Read AI Agent Skill & OpenAPI Guide →'
                  : '阅读 AI Agent Skill 与 OpenAPI 集成指南 →'}
              </a>
              <a href={withBase(`${prefix}/features/sql-diagnostics`)} className="yn-text-link">
                {isEn
                  ? 'Explore 6-Chart SQL Performance Diagnostics →'
                  : '查看 6 图联动 SQL 性能诊断与根因报告 →'}
              </a>
            </div>
          </div>

          <div className="yn-skill-col">
            <div className="yn-skill-card">
              <div className="yn-skill-head">
                <span className="yn-skill-title">skills/yearning/scripts/yearning_api.py</span>
                <button
                  type="button"
                  className="yn-copy-btn"
                  onClick={() => copyText('agent', AGENT_CMD)}
                >
                  {copiedKey === 'agent' ? (isEn ? 'Copied' : '已复制') : isEn ? 'Copy' : '复制'}
                </button>
              </div>
              <pre className="yn-pre-dark">
                <code>{AGENT_CMD}</code>
              </pre>
              <div className="yn-skill-foot">
                <span>{isEn ? 'Zero dependencies (Python 3 stdlib)' : '零外部依赖（Python 3 标准库）'}</span>
                <span>CSRF · Idempotency-Key · If-Match</span>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* 8. ENTERPRISE SECURITY & AI TRUST FOUNDATION (SUPPORTING ASSURANCE) */}
      <section className="yn-section yn-security-band">
        <div className="yn-container">
          <div className="yn-security-header">
            <div className="yn-section-head yn-security-head-copy">
              <h2 className="yn-h2">
                {isEn
                  ? 'Enterprise Security & AI Guardrails Built Into the Runtime'
                  : '面向生产落地的企业级安全与 AI 可信保障'}
              </h2>
              <p className="yn-section-lead">
                {isEn
                  ? 'So security teams can safely adopt Agentic AI in production: enforced by Go runtime contracts rather than prompt conventions.'
                  : '让企业放心将 AI Agentic 引入核心数据库流程：防泄露、零侵入、抗幻觉与平台加密全部通过 Go 运行时硬契约强制落地。'}
              </p>
            </div>
            <a href={withBase(`${prefix}/guide/security-and-ai-trust`)} className="yn-btn-secondary">
              {isEn ? 'Read Security & AI Trust Whitepaper →' : '阅读完整安全与 AI 防护白皮书 →'}
            </a>
          </div>

          <div className="yn-trust-grid is-compact">
            <div className="yn-trust-item">
              <div className="yn-trust-tag is-pass">{isEn ? 'ZERO-LEAK PRIVACY' : '防数据泄露'}</div>
              <h3>{isEn ? 'Per-Request Anonymization & Local LAN Track' : '字面值剥离 · 单轮内存脱敏 · 本地模型零出网'}</h3>
              <p>
                {isEn
                  ? 'Cloud providers strip literals/comments and anonymize identifiers in per-request memory with fail-closed AssertZeroLeak (2005/6003). Self-hosted LAN vLLM/Ollama run on an isolated san track that never falls back raw payloads to cloud.'
                  : '公有云 default 轨剥离字面值并在单次请求内存完成表列名匿名化，经 AssertZeroLeak 失败关闭（2005/6003）；内网 vLLM/Ollama 独立走 san 轨零字节出网且严禁明文回退。'}
              </p>
            </div>

            <div className="yn-trust-item">
              <div className="yn-trust-tag is-pass">{isEn ? 'NON-INTRUSIVE AI' : 'AI 零侵入'}</div>
              <h3>{isEn ? 'Read-Only Metadata Only, Zero Execution Power' : '只读元数据 · 不查业务行 · 无审批执行权'}</h3>
              <p>
                {isEn
                  ? 'AI Precheck uses 5 read-only metadata tools; the AI Assistant executes zero SQL (not even SELECT/EXPLAIN); Diagnostics never touches execution credentials or database parameters. AI can never approve or run changes.'
                  : 'AI 预审仅限 5 项只读元数据探测；AI 助手连 SELECT/EXPLAIN 都不执行；诊断绝不改参数或动用执行凭据。所有 AI 建议均须经过正式预审与冻结审批，AI 绝不越权执行。'}
              </p>
            </div>

            <div className="yn-trust-item">
              <div className="yn-trust-tag is-warn">{isEn ? 'ANTI-HALLUCINATION' : '抗 AI 幻觉'}</div>
              <h3>{isEn ? 'Deterministic Rules, Medium Cap & AST Checks' : '确定性规则兜底 · 自由建议封顶 · AST 二次验真'}</h3>
              <p>
                {isEn
                  ? 'Built-in rules and runtime-invalid SQL block deterministically as Critical, while free LLM observations cap at Medium so hallucinations never block valid releases. Missing metadata stays partial, and assistant SQL must pass dialect AST verification (6004).'
                  : '48 条规则与运行时无效 SQL 确定性 Critical 阻断，模型自由观察封顶 Medium 防止幻觉误杀；元数据缺失强制 partial 阻断，AI 助手 SQL 须经方言 AST 验真（6004）且缺必填列只追问不捏造。'}
              </p>
            </div>

            <div className="yn-trust-item">
              <div className="yn-trust-tag">{isEn ? 'DEFENSE IN DEPTH' : '平台纵深加固'}</div>
              <h3>{isEn ? '6-Purpose AEAD, Least Privilege & 90-Day Audit' : '六用途 AEAD · 三角色隔离 · 水印审计'}</h3>
              <p>
                {isEn
                  ? 'Six-purpose AES-256-GCM keyring with AAD binding, 3-role PostgreSQL metadata isolation, Argon2id + TTY-only admin reset, mandatory LDAPS/OIDC PKCE, watermarked SQL Reveal, 90-day append-only audit logs, and Distroless containers.'
                  : '6 用途 AES-256-GCM 密钥环绑定 AAD 防密文替换、元库三角色与目标库读写分离、Argon2id 与仅本机 TTY 重置 admin、LDAPS/OIDC PKCE、水印 SQL Reveal 与 90 天不可篡改审计日志。'}
              </p>
            </div>
          </div>
        </div>
      </section>

      {/* 9. CLOSING LAUNCH & DEPLOYMENT BAND */}
      <section className="yn-section yn-section-alt yn-closing">
        <div className="yn-container">
          <div className="yn-deploy-shell">
            <div className="yn-deploy-top">
              <div>
                <div className="yn-deploy-heading-row">
                  <h2 className="yn-h2">
                    {isEn ? 'Try v4 Public Beta in Three Minutes' : '三分钟部署体验 v4 公测版'}
                  </h2>
                  <span className="yn-beta-chip">
                    {isEn ? 'PUBLIC BETA' : '公测版本'}
                  </span>
                </div>
                <p className="yn-deploy-sub">
                  {isEn
                    ? 'Deploy on Docker Compose, standalone Linux binary, or Kubernetes in staging first, and share any feedback via GitHub Issues.'
                    : '支持 Docker Compose、Linux 单二进制与 Kubernetes 多副本部署；当前为公测版本，建议先在测试 / 预发环境验证。'}
                </p>
              </div>

              <div className="yn-deploy-tabs" role="tablist">
                <button
                  type="button"
                  role="tab"
                  aria-selected={deployTab === 'compose'}
                  className={`yn-deploy-tab${deployTab === 'compose' ? ' is-active' : ''}`}
                  onClick={() => setDeployTab('compose')}
                >
                  Docker Compose
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={deployTab === 'k8s'}
                  className={`yn-deploy-tab${deployTab === 'k8s' ? ' is-active' : ''}`}
                  onClick={() => setDeployTab('k8s')}
                >
                  Kubernetes
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={deployTab === 'migrate'}
                  className={`yn-deploy-tab${deployTab === 'migrate' ? ' is-active' : ''}`}
                  onClick={() => setDeployTab('migrate')}
                >
                  {isEn ? 'v3 Migration' : 'v3 升级迁移'}
                </button>
              </div>
            </div>

            <div className="yn-deploy-codebox">
              <pre className="yn-pre-dark">
                <code>{DEPLOY_SNIPPETS[deployTab]}</code>
              </pre>
              <button
                type="button"
                className="yn-copy-btn yn-deploy-copy"
                onClick={() => copyText(`deploy-${deployTab}`, DEPLOY_SNIPPETS[deployTab])}
              >
                {copiedKey === `deploy-${deployTab}`
                  ? isEn
                    ? 'Copied'
                    : '已复制'
                  : isEn
                    ? 'Copy'
                    : '复制'}
              </button>
            </div>

            <div className="yn-deploy-footer">
              <div className="yn-deploy-actions">
                <a
                  href="https://demo.yearning.io"
                  target="_blank"
                  rel="noreferrer"
                  className="yn-btn-primary"
                >
                  {isEn ? 'Live Demo ↗' : '在线演示 ↗'}
                </a>
                <a href={withBase(`${prefix}/guide/quick-start`)} className="yn-btn-secondary">
                  {isEn ? 'Deployment Guide' : '查看部署手册'}
                </a>
                <a href={withBase(`${prefix}/operations/migration-v3-to-v4`)} className="yn-btn-ghost">
                  {isEn ? 'Upgrade from v3' : '从 v3 升级迁移'}
                </a>
              </div>
              <p className="yn-legal-note">
                v4.0 Public Beta · AGPL-3.0-or-later · Go 1.25 · React 19 · PostgreSQL 14–18
              </p>
              {isEn ? (
                <nav className="yn-legal-links" aria-label="Pricing and legal links">
                  <a href={withBase('/en/about/pricing')}>Pricing</a>
                  <a href={withBase('/en/about/terms')}>Terms of Service</a>
                  <a href={withBase('/en/about/refund-policy')}>Refund Policy</a>
                  <a href={withBase('/en/about/privacy')}>Privacy Policy</a>
                  <a href={withBase('/en/about/license')}>License</a>
                </nav>
              ) : null}
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}
