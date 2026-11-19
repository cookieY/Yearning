# Docker Compose Quick Start

:::warning Public Beta Notice
Yearning v4.0 is currently in **Public Beta**. Please deploy in staging or evaluation environments first, and always back up the PostgreSQL data volume and `keyring.json` before testing with important data.
:::

Yearning v4 ships an all-in-one `docker-compose.yml` that bootstraps **PostgreSQL 18**, internal PostgreSQL TLS certificates, a 6-purpose **AES-256-GCM AEAD keyring**, automated versioned schema migrations, and the Yearning v4 control plane with embedded React UI in a single command.

## Prerequisites

- Docker Engine 24+ and Docker Compose v2
- Available port `8080` (override with `YEARNING_PORT`)
- Minimum 2 vCPU / 2 GiB RAM (4 GiB+ recommended for large batch SQL fingerprinting)

## Launch in Three Commands

```bash
# 1. Clone the v4 branch
git clone --single-branch --branch v4 https://github.com/cookieY/Yearning.git
cd Yearning

# 2. (Optional) Customize the initial admin password (min 12 chars) and port
export YEARNING_ADMIN_PASSWORD="YourStrongPassword@2026!"
export YEARNING_PORT="8080"

# 3. Start the full stack
docker compose up -d
```

Verify startup health:

```bash
docker compose ps
docker compose logs -f yearning
```

Open `http://<your-host-ip>:8080`:

- **Default admin username**: `admin`
- **Default initial password**: `Yearning@2026!` (or your `YEARNING_ADMIN_PASSWORD`)

:::tip Recommended First-Boot Checklist
1. Add and test at least one OpenAI-compatible AI Provider in **Review Engine -> Providers** (`/admin/review-engine/providers`).
2. Compose a **Rule Set** in `/admin/rule-sets` from built-in or custom skills.
3. Register target databases in **Admin -> Datasources** (`/admin/datasources`) with separated read-only and execution credentials.
4. Create `change_review` and `query_access` flows in `/admin/flows` and assign them via **Permission Groups** (`/admin/permission-groups`).
:::

## Service Topology

| Service | Purpose | Secret & Privilege Boundary |
| :--- | :--- | :--- |
| `bootstrap` | Idempotent one-shot initializer | Generates internal PG TLS certs, 6-purpose `keyring.json`, and initial admin password file |
| `postgres` | PostgreSQL 18 metadata store | Mounts TLS server cert/key only (`ssl=on`); zero access to the application keyring |
| `migrate` | One-shot versioned migration Job | Runs `yearning migrate apply` non-root with read-only rootfs and validates the column catalog |
| `yearning` | Control plane + embedded Web UI | Runs `yearning serve` in a Distroless read-only container mounting read-only secrets |

:::danger Never run `docker compose down -v` during upgrades
Deleting the `yearning_app_secrets` volume permanently destroys your **AEAD keyring (`keyring.json`)**, making all encrypted datasource passwords, AI provider keys, order SQL payloads, and rollback artifacts undecryptable.
:::

## Production Hardening Checklist

1. Terminate HTTPS in front of Yearning (Nginx / Envoy / Ingress) and set `YEARNING_ENVIRONMENT=production` plus `YEARNING_SESSION_SECURE=true`.
2. Configure `YEARNING_TRUSTED_PROXIES` so audit trails capture real client IP addresses.
3. Provision PostgreSQL 3-role isolation (`yearning_app`, `yearning_migrator`, `yearning_retention`) and supply `YEARNING_RETENTION_DATABASE_DSN`. See [Kubernetes Production Deployment](/en/operations/kubernetes).
4. Back up `keyring.json` in an external KMS or encrypted vault alongside PostgreSQL backups.
