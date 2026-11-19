# Yearning v4 Backend

Go modular monolith backend for Yearning v4.

## Common Commands

```bash
go build -tags nomsgpack -o ../bin/yearning ./cmd/yearning
go test -tags nomsgpack ./...
```

## Related Directories

- OpenAPI specification: `../api/openapi/yearning-v4.yaml`
- PostgreSQL migrations and roles: `../db/`
- Kubernetes manifests: `deploy/k8s/`
