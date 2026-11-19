import { useLang, withBase } from '@rspress/core/runtime';

export function NotFoundView() {
  const lang = useLang();
  const isEn = lang.startsWith('en');
  const prefix = isEn ? '/en' : '';

  return (
    <div className="yn-404">
      <div className="yn-404-card">
        <div className="yn-404-code">404 · ROUTE_NOT_FOUND</div>
        <h1>{isEn ? 'Page Not Found in Yearning v4 Docs' : '未找到该 Yearning v4 文档页面'}</h1>
        <p>
          {isEn
            ? 'This legacy v3 link may have moved during the Yearning v4 rebuild. Jump back to the homepage, quick start, or press ⌘K to search.'
            : '如果你是从旧版 Yearning v3 链接跳转过来，部分路径已随 v4 架构重构更新。可返回首页、快速开始或按 ⌘K 检索文档。'}
        </p>
        <div className="yn-404-actions">
          <a href={withBase(`${prefix}/`)} className="yn-btn-primary">
            {isEn ? 'Back to Home' : '返回首页'}
          </a>
          <a href={withBase(`${prefix}/guide/quick-start`)} className="yn-btn-secondary">
            {isEn ? 'Quick Start' : '快速开始'}
          </a>
          <a href={withBase(`${prefix}/operations/migration-v3-to-v4`)} className="yn-btn-secondary">
            {isEn ? 'v3 Migration' : 'v3 迁移指南'}
          </a>
        </div>
      </div>
    </div>
  );
}
