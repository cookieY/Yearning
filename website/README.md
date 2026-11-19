# Yearning v4 Documentation Website (`v4.yearning.io`)

Built with **Rspress 2 (React 19 + TypeScript + MDX)** for static hosting on **GitHub Pages** directly from the `cookieY/Yearning` repository (`v4` branch).

## Local Preview

```bash
cd website
pnpm install
pnpm dev
```

Open `http://localhost:5178` (bound to `0.0.0.0` for LAN access).

## Production Build

```bash
# Custom domain (https://v4.yearning.io)
pnpm build

# GitHub Pages subpath (https://<user>.github.io/<repo>/)
DOCS_BASE=/<repo>/ pnpm build
```

Static output is emitted to `website/doc_build/`.

## GitHub Pages Deployment (`cookieY/Yearning`)

The site is automatically built and deployed by `.github/workflows/deploy-docs.yml` in `cookieY/Yearning` whenever files under `website/**` change on the `v4` branch (or via manual `workflow_dispatch`):

1. In `cookieY/Yearning` **Settings -> Pages -> Build and deployment**, set **Source** to **GitHub Actions**.
2. Point DNS CNAME for `v4.yearning.io` to `cookiey.github.io` (`website/docs/public/CNAME` is already configured to `v4.yearning.io`).
3. Ensure the `github-pages` environment deployment branch policy in GitHub Settings allows the `v4` branch.

## Directory Layout

- `docs/zh/` - Chinese docs (default locale, served at `/`, `/guide/*`, `/features/*`, ...)
- `docs/en/` - English docs (served at `/en/`, `/en/guide/*`, `/en/features/*`, ...)
- `docs/public/` - Brand SVG/PNG marks, 24 real 1440px Yearning v4 screenshots, `CNAME`, `.nojekyll`
- `components/` - React 19 interactive homepage engine, SQL Governance Lab, real screenshot showcase
- `theme/` - Rspress theme tokens (`index.css`) and custom 404 layout (`index.tsx`)
