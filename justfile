default:
    @just --list

setup:
    pnpm install --frozen-lockfile

[group('test')]
test-auth:
    cd shared/auth/oidc && go test ./...
    cd services/hub && go test ./...
    cd services/mcp && go test ./...
    pnpm test

[group('lint')]
lint-auth:
    cd shared/auth/oidc && golangci-lint run --config ../../../services/mcp/.golangci.yml ./...
    cd services/hub && golangci-lint run ./...
    cd services/mcp && golangci-lint run ./...
    pnpm lint
    shellcheck frontend/docker-entrypoint-runtime-config.sh

[group('validate')]
chart:
    helm lint chart/
    helm template chart/ > /dev/null
    helm template chart/ -f chart/values-example.yaml > /dev/null

[group('validate')]
build:
    for module in $(go list -m -f '{{ '{{.Dir}}' }}'); do (cd "$module" && go build ./...) || exit; done
    pnpm build

[group('validate')]
check: lint-auth test-auth test-deployment chart build

[group('test')]
test-deployment:
    python3 scripts/test-oidc-deployment.py
