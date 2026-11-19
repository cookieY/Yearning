.PHONY: backend-build backend-test frontend-install frontend-build frontend-test docker-build

GO_TAGS ?= nomsgpack

backend-build:
	cd backend && go build -tags $(GO_TAGS) -o ../bin/yearning ./cmd/yearning

backend-test:
	cd backend && go test -tags $(GO_TAGS) ./...

frontend-install:
	cd frontend && pnpm install --frozen-lockfile

frontend-build:
	cd frontend && pnpm build

frontend-test:
	cd frontend && pnpm test

docker-build: frontend-build
	docker build -f docker/release/Dockerfile -t yearning:v4-local .
