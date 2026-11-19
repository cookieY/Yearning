import { useEffect, useState } from 'react';
import { useDark, withBase } from '@rspress/core/runtime';

interface ScreenItem {
  key: 'dashboard' | 'precheck' | 'detail' | 'query' | 'admin';
  titleZh: string;
  titleEn: string;
  descZh: string;
  descEn: string;
  route: string;
}

const SCREENS: ScreenItem[] = [
  {
    key: 'dashboard',
    titleZh: '个人工作台与系统健康',
    titleEn: 'Workbench & System Health',
    descZh: '聚合待审批、待执行、30 日执行成功率与 6 组件实时健康灯，并内置右下角受控数据库 AI 助手。',
    descEn: 'Unified personal queues, 30-day execution metrics, 6-component health dots, and the scoped Database AI Assistant.',
    route: '/workspace',
  },
  {
    key: 'precheck',
    titleZh: 'AI Agentic SQL 预审门禁',
    titleEn: 'AI Agentic SQL Precheck',
    descZh: '最高 100,000 语句 / 32 MiB 批量导入，AI 自主探测真实元数据并结合 48 条内置规则输出风险发现与建议。',
    descEn: 'Up to 100,000 statements / 32 MiB batch review with autonomous metadata probes, 48 built-in rules, and structured findings.',
    route: '/changes/new',
  },
  {
    key: 'detail',
    titleZh: '冻结工单详情与执行控制台',
    titleEn: 'Frozen Order & Execution Console',
    descZh: '展示提交时冻结的多阶段审批快照、带水印 SQL Reveal 开关、进程内 gh-ost 进度与一键派生回滚工单。',
    descEn: 'Inspect frozen multi-stage snapshots, watermarked SQL Reveal, in-process gh-ost progress, and governed rollback derivation.',
    route: '/changes/orders/:id',
  },
  {
    key: 'query',
    titleZh: '受控只读查询与敏感脱敏',
    titleEn: 'Governed Query & PII Masking',
    descZh: '基于限时 Query Grant 与只读 AST 校验，服务端自动脱敏敏感列，支持流式游标分页与 CSV/JSONL 导出。',
    descEn: 'Time-bounded Query Grants, read-only AST guards, server-side PII masking, cursor pagination, and streaming exports.',
    route: '/query',
  },
  {
    key: 'admin',
    titleZh: '多引擎治理与管理后台',
    titleEn: 'Multi-Engine Governance Plane',
    descZh: '统一配置 MySQL / PG / TiDB / OceanBase / PolarDB 数据源、审批流、审核技能、AI 预算、OIDC/LDAPS 与通知规则。',
    descEn: 'Manage MySQL, PostgreSQL, TiDB, OceanBase, and PolarDB datasources, flows, review skills, AI budgets, SSO, and notifications.',
    route: '/admin/datasources',
  },
];

export function ProductShowcase({ isEn }: { isEn: boolean }) {
  const isDark = useDark();
  const [mounted, setMounted] = useState(false);
  const [selected, setSelected] = useState(0);

  useEffect(() => {
    setMounted(true);
  }, []);

  const activeScreen = SCREENS[selected];
  const loc = isEn ? 'en-US' : 'zh-CN';
  const theme = mounted ? (isDark ? 'dark' : 'light') : 'dark';
  const screenshotSrc = withBase(`/screenshots/${activeScreen.key}-${loc}-${theme}.png`);

  return (
    <div className="yn-showcase">
      <div
        className="yn-showcase-rail"
        role="tablist"
        aria-label={isEn ? 'Yearning v4 Screens' : 'Yearning v4 真实界面切换'}
      >
        {SCREENS.map((item, idx) => (
          <button
            key={item.key}
            type="button"
            role="tab"
            aria-selected={selected === idx}
            className={`yn-rail-item${selected === idx ? ' is-active' : ''}`}
            onClick={() => setSelected(idx)}
          >
            <div className="yn-rail-top">
              <span className="yn-rail-title">{isEn ? item.titleEn : item.titleZh}</span>
              <span className="yn-rail-route">{item.route}</span>
            </div>
            <p className="yn-rail-desc">{isEn ? item.descEn : item.descZh}</p>
          </button>
        ))}
      </div>

      <figure className="yn-showcase-stage">
        <div className="yn-stage-img-wrap">
          <img
            key={screenshotSrc}
            src={screenshotSrc}
            alt={isEn ? activeScreen.titleEn : activeScreen.titleZh}
            width={1440}
            height={900}
            loading="lazy"
            decoding="async"
            className="yn-stage-img"
          />
        </div>
        <figcaption className="yn-stage-caption">
          <span className="yn-caption-route">{activeScreen.route}</span>
          <span>
            {isEn
              ? 'Authentic Yearning v4 1440p UI capture (auto-switches with site theme and language)'
              : 'Yearning v4 1440p 真实界面实拍（自动跟随当前站点明暗主题与语言切换）'}
          </span>
        </figcaption>
      </figure>
    </div>
  );
}
