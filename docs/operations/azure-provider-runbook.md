# Azure Provider Runbook

## Rollout

1. Publish the Azure provider image with `Azure Provider Image Release`.
2. Verify the workflow summary contains a digest and cosign signature.
3. Update samples with `AZURE_PROVIDER_DIGEST=sha256:<digest> make update-azure-provider-samples`.
4. Apply a digest-pinned `PlatformProvider` with signature verification and mTLS in production.
5. Run one non-production VMImage through `make test-e2e-azure` or an equivalent cluster job.

## Rollback

1. Keep the previous provider image digest in release notes.
2. Reapply the previous digest-pinned `PlatformProvider`.
3. Confirm provider health and `imagebuilder_provider_healthy{provider="azure"}`.
4. Delete failed staging blobs and partial managed images through normal VMImage cleanup before retrying.

## Quota And Throttling

- Check regional Compute Gallery replica quotas before enabling multi-region publishing.
- Keep `pageUploadConcurrency` conservative for shared storage accounts. Start with `4`; increase only after watching throttling and upload latency.
- Increase `pageUploadChunkMiB` only in multiples that remain 512-byte aligned. The default is `4`.
- Watch storage account ingress limits when several VMImages upload large VHDs concurrently.

## Common Failures

| Symptom | Likely Cause | Action |
| --- | --- | --- |
| `artifact is not an Azure-compatible fixed VHD` | Dynamic VHD/VHDX/QCOW2/raw artifact | Convert to fixed VHD before upload. |
| `AuthorizationFailed` | Missing RBAC action or DataAction | Compare assignment with `azure-provider-role.json` and target scopes. |
| Gallery version delete fails | Gallery replication still settling | Retry cleanup after replication operation completes or delete version manually. |
| Workload Identity token unavailable | ServiceAccount/federated credential mismatch | Check `azure.workload.identity/*` annotations, issuer, subject, and token projection. |
| Storage throttling | Too many concurrent page uploads | Lower `pageUploadConcurrency` or distribute builds across storage accounts. |

## Metrics

The standalone Azure provider exposes Prometheus metrics on `:8080/metrics` by
default. Set `--metrics-listen=""` to disable it.

Key provider metrics:

- `imagebuilder_azure_operation_duration_seconds`
- `imagebuilder_azure_page_upload_bytes_total`
- `imagebuilder_azure_page_upload_ranges_total`

Correlate these with the core operator metrics for build, upload, register, and
cleanup duration.
