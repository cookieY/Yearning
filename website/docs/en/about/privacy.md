# Privacy Policy

*Last updated: November 22, 2026*

**Short version:** Self-hosted Yearning sends zero telemetry home. We never see your SQL, database schemas, credentials, or credit card numbers.

## 1. Your Self-Hosted Yearning Instance

Both Community Edition and Yearning v4 Pro run 100% inside your own network:

- **Zero telemetry**: Yearning does not send usage analytics, queries, schemas, results, or user lists to us.
- **Offline license checks**: Yearning v4 Pro verifies Ed25519-signed license keys completely offline—no phoning home or heartbeat pings.
- **Only what you configure**: Any outbound traffic (such as BYOK AI APIs, LDAP/OIDC, webhooks, or optional GitHub release checks) goes straight to the services you configure.

## 2. Website, Checkout & Support

When you browse `https://v4.yearning.io`, buy a license, or email us, we only handle the bare minimum needed to deliver your license and help you:

- **Checkout & billing (Creem)**: **Creem** acts as our Merchant of Record and handles payment cards, billing addresses, tax IDs, and VAT/sales tax. We never receive or store your full card details.
- **Order & license records**: From Creem order webhooks, we keep your name or organization name, email address, order ID, license ID, plan type, expiry/update window, and any cluster ID/URL you share for soft binding or re-binding.
- **Support emails**: If you write to `henry@yearning.io`, we keep the thread so we can help resolve your issue.
- **Website basics**: Our hosting/CDN keeps standard temporary access logs for security and abuse prevention, and the docs site stores a `yearning_docs_lang_auto_v1` key in your browser's `localStorage` to remember your language preference.

## 3. How We Use It

We only use this information to issue, re-bind, and support your license, send critical security or renewal notices, and meet legal tax/accounting obligations.

We **never sell or rent** your data, and we never use customer data to train AI models. Data is shared only with essential infrastructure providers (like Creem and email delivery) or when legally required.

## 4. Retention & Your Rights

We keep order records while your license is active and as needed for tax and anti-fraud records. You can ask to view, update, or delete your personal contact info anytime by emailing **`henry@yearning.io`**.

See also [Pricing](/en/about/pricing) and [Terms & License](/en/about/terms).
