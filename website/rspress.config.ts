import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from '@rspress/core';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const base = process.env.DOCS_BASE || '/';
const normalizedBase = base.endsWith('/') ? base : `${base}/`;
const autoLocaleScript = `(function(){try{if(/bot|crawler|spider|slurp|bingpreview|googlebot|baiduspider/i.test(navigator.userAgent||''))return;var k='yearning_docs_lang_auto_v1';if(localStorage.getItem(k))return;var b=${JSON.stringify(normalizedBase)};var p=window.location.pathname||'/';var r=p.indexOf(b)===0?p.slice(b.length):p.replace(/^\\/+/, '');if(r==='en'||r.indexOf('en/')===0){localStorage.setItem(k,'en');return;}var ls=(navigator.languages&&navigator.languages.length?navigator.languages:[navigator.language||'']).filter(Boolean);var lang=String(ls[0]||'').toLowerCase();if(!lang)return;localStorage.setItem(k,lang.indexOf('zh')===0?'zh':'en');if(lang.indexOf('zh')!==0){window.location.replace(b+'en/'+r.replace(/^\\/+/, '')+window.location.search+window.location.hash);}}catch(e){}})();`;

const zhSidebar = [
  {
    text: '部署与安全基线',
    items: [
      { text: '认识 Yearning v4 与 v3 对比', link: '/guide/what-is-yearning' },
      { text: '企业级安全与 AI 可信架构', link: '/guide/security-and-ai-trust' },
      { text: 'Docker Compose 部署与 Nginx 反代', link: '/guide/quick-start' },
      { text: '二进制与独立 PG 手动部署', link: '/operations/binary-deploy' },
      { text: 'Kubernetes 生产多副本部署', link: '/operations/kubernetes' },
      { text: '从 Yearning v3 停机迁移实战', link: '/operations/migration-v3-to-v4' },
    ],
  },
  {
    text: '管理员配置手册',
    items: [
      { text: '首次登录 15 分钟初始化教程', link: '/admin/first-time-setup' },
      { text: '数据源接入与目标库建权 SQL', link: '/admin/datasources-and-grants' },
      { text: 'AI Provider、48 条内置规则与经验库', link: '/admin/review-engine' },
      { text: '流程编排、SSO 登录与通知中心', link: '/admin/governance' },
    ],
  },
  {
    text: '研发与 DBA 使用手册',
    items: [
      { text: '提单、大批量导入与 AI 预审实战', link: '/features/ai-precheck' },
      { text: '数据库 AI 助手实战（4.0 重点）', link: '/features/ai-assistant' },
      { text: '审批、执行与 gh-ost 调优实战', link: '/features/change-orders' },
      { text: '回滚保护与一键派生回滚工单', link: '/features/sql-rollback' },
      { text: '受控只读查询、脱敏与流式导出', link: '/features/query-workspace' },
      { text: 'Top SQL 性能诊断实战（管理员）', link: '/features/sql-diagnostics' },
    ],
  },
  {
    text: '运维参考与自动化集成',
    items: [
      { text: 'CLI 命令、密码重置与配置参考', link: '/operations/cli-and-config' },
      { text: 'AI Agent Skill 与 OpenAPI v4 实战', link: '/integration/agent-skill-openapi' },
      { text: '4 位错误码与高频排障手册', link: '/reference/error-codes' },
      { text: '架构原理与安全隐私设计', link: '/guide/architecture' },
      { text: '开源许可与安全策略', link: '/about/license' },
    ],
  },
];

const enSidebar = [
  {
    text: 'Deployment & Security',
    items: [
      { text: 'Overview & v3 to v4 Changes', link: '/en/guide/what-is-yearning' },
      { text: 'Enterprise Security & AI Trust', link: '/en/guide/security-and-ai-trust' },
      { text: 'Docker Compose & Nginx Proxy', link: '/en/guide/quick-start' },
      { text: 'Binary & Standalone PG Setup', link: '/en/operations/binary-deploy' },
      { text: 'Kubernetes Multi-Replica Setup', link: '/en/operations/kubernetes' },
      { text: 'Migrating from Yearning v3', link: '/en/operations/migration-v3-to-v4' },
    ],
  },
  {
    text: 'Admin Setup Guide',
    items: [
      { text: '15-Minute First-Boot Setup', link: '/en/admin/first-time-setup' },
      { text: 'Datasources & DB Grant SQL', link: '/en/admin/datasources-and-grants' },
      { text: 'AI Providers, 48 Rules & Knowledge', link: '/en/admin/review-engine' },
      { text: 'Flows, LDAP/OIDC & Notifications', link: '/en/admin/governance' },
    ],
  },
  {
    text: 'User & DBA Handbook',
    items: [
      { text: 'Submitting Orders & AI Precheck', link: '/en/features/ai-precheck' },
      { text: 'Database AI Assistant (4.0 Core)', link: '/en/features/ai-assistant' },
      { text: 'Approvals, gh-ost & Verification', link: '/en/features/change-orders' },
      { text: 'Rollback Protection & Rollback Orders', link: '/en/features/sql-rollback' },
      { text: 'Read-Only Query & PII Masking', link: '/en/features/query-workspace' },
      { text: 'Top SQL Performance Diagnostics', link: '/en/features/sql-diagnostics' },
    ],
  },
  {
    text: 'Operations & Integration',
    items: [
      { text: 'CLI, Admin Reset & Config Reference', link: '/en/operations/cli-and-config' },
      { text: 'AI Agent Skill & OpenAPI v4', link: '/en/integration/agent-skill-openapi' },
      { text: 'Error Codes & Troubleshooting', link: '/en/reference/error-codes' },
      { text: 'Architecture & Privacy Pipeline', link: '/en/guide/architecture' },
      { text: 'License & Security Policy', link: '/en/about/license' },
    ],
  },
];

export default defineConfig({
  root: path.join(__dirname, 'docs'),
  outDir: 'doc_build',
  base,
  sitemap: {
    siteUrl: 'https://v4.yearning.io',
  },
  title: 'Yearning v4 Beta',
  description: 'Yearning v4 公测版官方文档 · 基于 AI Agentic 的新一代企业级 SQL 审核平台',
  icon: '/brand/yearning-cube.svg',
  logo: '/brand/yearning-cube.svg',
  logoText: 'Yearning v4 Beta',
  lang: 'zh',
  locales: [
    {
      lang: 'zh',
      label: '简体中文',
      title: 'Yearning v4 Beta',
      description: 'Yearning v4 公测版官方文档 · 基于 AI Agentic 的新一代企业级 SQL 审核平台',
    },
    {
      lang: 'en',
      label: 'English',
      title: 'Yearning v4 Beta',
      description: 'Yearning v4 Public Beta Docs · The AI Agentic SQL Review Platform',
    },
  ],
  head: [
    ['meta', { name: 'theme-color', content: '#07080D' }],
    `<script>${autoLocaleScript}</script>`,
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
    [
      'link',
      {
        rel: 'stylesheet',
        href: 'https://fonts.googleapis.com/css2?family=Geist:wght@300;400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600&display=swap',
      },
    ],
  ],
  themeConfig: {
    darkMode: 'dark' as unknown as boolean,
    socialLinks: [
      {
        icon: 'github',
        mode: 'link',
        content: 'https://github.com/cookieY/Yearning',
      },
    ],
    sidebar: {
      '/guide': zhSidebar,
      '/features': zhSidebar,
      '/admin': zhSidebar,
      '/operations': zhSidebar,
      '/integration': zhSidebar,
      '/reference': zhSidebar,
      '/about': zhSidebar,
      '/en/guide': enSidebar,
      '/en/features': enSidebar,
      '/en/admin': enSidebar,
      '/en/operations': enSidebar,
      '/en/integration': enSidebar,
      '/en/reference': enSidebar,
      '/en/about': enSidebar,
    },
    footer: {
      message: 'Released under AGPL-3.0-or-later · Copyright © 2017–2026 Henry Yee · henry@yearning.io',
    },
  },
});
