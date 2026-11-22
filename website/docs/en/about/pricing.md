# Pricing

Yearning has been open-source since 2017. The **Community Edition** is free forever and covers the full SQL review, approval, execution, rollback, and query-audit workflow—most teams never need to pay a cent.

**Yearning v4 Pro** is for teams that want extra day-to-day productivity, lightweight compliance, and direct SQL client access—while helping fund ongoing maintenance.

## Plans at a Glance

| Plan | Price | What It Covers |
| :--- | :--- | :--- |
| **Community Edition** | **$0** (Free forever) | Unlimited users & datasources · Open-source (`AGPL-3.0-or-later`) |
| **Yearning v4 Pro — Annual** | **$599 / year** | **1 self-hosted cluster** · Unlimited users & datasources · All `v4.x` updates |
| **Yearning v4 Pro — v4 License** | **$1,499** (One-time) | **1 self-hosted cluster** · Perpetual `v4.x` use + **2 years** of updates |
| **Commercial / Custom** | **Contact us** | License without AGPL-3.0 copyleft, custom dialects, or formal SLA (`henry@yearning.io`) |

> **Availability:** Yearning v4 Pro is currently onboarding in early access alongside v4.0 Public Beta. Email **`henry@yearning.io`** to request early access or reserve a license.

## Community vs. Pro

### Community Edition (`$0`, Open Source)

- Full SQL change review, frozen multi-stage approvals, and audit trails
- MySQL 5.7/8.0, PostgreSQL 14–18, TiDB (MySQL), OceanBase (MySQL), and PolarDB (MySQL/PG)
- Privacy-preserving AI precheck, 48 built-in rules, custom prompt tools, and knowledge base
- Scoped Database AI Assistant and SQL performance diagnostics (Bring-Your-Own-Key)
- Cross-engine AST rollback evidence and in-process `gh-ost` online DDL
- Read-only SQL query workspace with anti-bypass masking and client-side CSV, JSONL, and XLSX export
- LDAP/LDAPS, OIDC SSO, multi-channel notifications, and Kubernetes multi-replica HA

### Yearning v4 Pro (`$599 / year` or `$1,499` v4 License)

Everything in Community Edition, plus commercial Pro modules for one cluster:

- **GitOps PR Precheck**: Scoped service/personal access tokens (PATs) for GitHub Actions and GitLab CI PR review bots and automated order creation
- **Lightweight Compliance & UX**: TOTP MFA (with emergency CLI reset for `admin`), dynamic anti-screenshot watermarking, custom branding, and mobile-friendly approval views
- **Query Workspace Upgrades**: Team-shared SQL snippets, additional export formats (JSON, SQL `INSERT`), and flexible masking options (partial masks, deterministic hashing, rule-based exemptions)
- **Read-Only Client Connectivity**: TLS-enforced MySQL-compatible query gateway reusing Yearning grants, AST safety checks, masking, and full audit logging
- **Distribution Perks**: Pre-built multi-arch Pro images, offline deployment bundles, production Helm charts, and early-access builds

## How Delivery & Billing Work

- **100% Self-Hosted & BYOK**: Yearning runs entirely on your own servers. We never host your databases, proxy your production traffic, or resell AI tokens—you plug in your own AI provider key.
- **Instant Digital Delivery**: After payment, you receive an email with your offline Ed25519-signed license key and Pro download/activation instructions. Nothing physical is shipped.
- **No Seat Counting**: One license covers one production cluster (including multiple Kubernetes replicas sharing the same metadata database)—no per-user or per-database fees.
- **Checkout & Tax**: Online payments, invoices, and VAT/sales taxes are handled by **Creem**, our authorized **Merchant of Record (MoR)**.

By purchasing Pro, you agree to our [Terms & License](/en/about/terms), [Refund Policy](/en/about/refund-policy), and [Privacy Policy](/en/about/privacy). Questions? Email **`henry@yearning.io`**.
