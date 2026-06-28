# Development Guide

## Toolchain

- Go version from `go.mod`
- controller-gen
- protoc for provider protobuf generation
- Docker for image builds
- kind and kubectl for smoke tests
- Helm for chart rendering/linting

## Common Commands

```bash
make generate
make manifests
go test ./...
make build
make build-builder
make build-uploader
```

Security and release gates:

```bash
make test-manifests
make test-core-e2e
make security-check
make helm-lint
make helm-template
```

## Code Generation

Run after changing `api/v1alpha1` Go types:

```bash
make generate
make manifests
```

Run after changing `api/provider/v1/provider.proto`:

```bash
make proto
```

Do not edit generated files manually.

## Tests

Unit tests:

```bash
go test ./...
```

Race-enabled suite:

```bash
make test
```

Kind smoke test:

```bash
make test-e2e
```

## CI Gates

GitHub Actions treats `Required CI Gate` as the branch-protection check. It
depends on:

- full Go tests plus deterministic core E2E and manifest tests
- binary builds
- generated-code freshness
- `go vet`
- `staticcheck`
- `gosec`
- `govulncheck`
- `go-licenses`
- gitleaks secret scanning
- Helm chart lint/render
- Trivy container image scan

Pull requests should not be merged unless `Required CI Gate` passes.

## Build Images

```bash
make docker-build
make docker-build-builder
make docker-build-uploader
```

## Style

- Use `fmt.Errorf("context: %w", err)` for errors.
- Pass `context.Context` into I/O functions.
- Keep interfaces small.
- Prefer table-driven tests.
- Use `log/slog` with structured fields.
- Keep generated credentials out of environment variables and logs.

## License Rules

- Do not link LGPL libraries into core binaries.
- Use LGPL components only through external processes or sockets.
- Keep statically linked dependencies Apache-2.0 or MIT compatible.
