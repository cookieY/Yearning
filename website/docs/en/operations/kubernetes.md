# Kubernetes Multi-Replica Production Deployment

Yearning v4 is a completely stateless Go monolith backed by PostgreSQL for shared leases, sessions, scheduled jobs, and SSE fan-out, ready for multi-replica Kubernetes deployments with zero Pod-local state.

Standard manifests live under `backend/deploy/k8s/`:

- `backend/deploy/k8s/control-plane.yaml`: Stateless multi-replica Deployment, Service, probes, and hardened securityContext
- `backend/deploy/k8s/migration-workloads.yaml`: Isolated schema migration Job and temporary migration review Deployment
- `backend/deploy/k8s/network-policy.yaml`: Least-privilege NetworkPolicy

## Security Hardening & 3-Role PostgreSQL Isolation

- **Distroless Non-Root Containers**: `linux/amd64` and `linux/arm64`, UID `65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`.
- **Mandatory 3-Role Isolation (`db/roles/grants.sql`)**:
  - `yearning_migrator`: Used exclusively by `yearning-schema-migration` Job to apply `db/migrations/*.sql` (no keyring access).
  - `yearning_app`: Non-superuser runtime role used by `yearning` Deployment (verified on boot by `database_role_self_check` in production mode).
  - `yearning_retention`: Injected via `YEARNING_RETENTION_DATABASE_DSN` for 90-day audit and 7-day raw payload retention cleanup.

## Four Production Readiness Probes

Pods receive traffic only when all four readiness checks succeed:

1. `metadata_postgresql`: Live PostgreSQL pool health.
2. `key_provider`: Valid 6-purpose AEAD `keyring.json` readable.
3. `schema_ledger`: `schema_migrations` exists with zero failed rows.
4. `exact_schema_version` + `database_role_self_check`: Applied schema version exactly matches the image's `/usr/share/yearning/db-migrations` and the runtime role satisfies non-superuser least-privilege checks.

## Standard Rollout Sequence

```bash
kubectl apply -f backend/deploy/k8s/network-policy.yaml
kubectl apply -f backend/deploy/k8s/migration-workloads.yaml
kubectl wait --for=condition=complete job/yearning-schema-migration -n yearning --timeout=300s
kubectl apply -f backend/deploy/k8s/control-plane.yaml
kubectl rollout status deployment/yearning -n yearning --timeout=300s
```
