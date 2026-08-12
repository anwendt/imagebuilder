# Troubleshooting

## Build Stays Queued

Check status and events:

```bash
kubectl describe vmimage <name>
kubectl get leases -n imagebuilder-system
```

Common causes:

- `--max-concurrent-builds` reached.
- kube-scheduler cannot currently satisfy resources, node selectors, taints,
  PVC topology, or other scheduling constraints.
- stale Lease after an interrupted operator; expired Leases are reused.

## Build Job Fails

Inspect the Job and pod logs:

```bash
kubectl describe job <vmimage>-build
kubectl logs job/<vmimage>-build -c build
```

Failure reasons are classified in status:

- `SourceFetchFailed`
- `BootFailed`
- `GuestReadinessTimeout`
- `ProvisionerFailed`
- `FinalHygieneFailed`
- `ArtifactConvertFailed`

## ISO Boots But Provisioning Never Starts

Check:

- `spec.build.guestAccess` is present for provisioners that need guest access.
- Linux uses SSH and Windows uses WinRM.
- QEMU host forwarding uses loopback only.
- cloud-init/autounattend actually creates the expected temporary user.
- firewall rules inside the guest allow SSH or WinRM.

## FinalHygieneFailed

The builder checks for bootstrap leftovers before shutdown/convert.

Linux checks include:

- cloud-init NoCloud seed residue under `/var/lib/cloud/seed`
- generated temporary user still present

Windows checks include:

- `Autounattend.xml` leftovers
- autologon registry values
- WinRM Basic auth still enabled
- generated user still enabled

For Cloudbase-Init based Windows images, also verify that the MSI path in
`spec.source.installer.windows.cloudbaseInitMsi` is reachable from Windows
Setup and that the generated `cloudbase-init.conf` points at the intended
metadata service.

Fix the provisioning or sanitizer step that leaves the residue.

## Upload Job Fails

Inspect:

```bash
kubectl describe job <vmimage>-upload
kubectl logs job/<vmimage>-upload -c upload
```

Common causes:

- missing or invalid `ProviderConfig`
- provider plugin not registered or unhealthy
- artifact PVC not available to the upload job
- target provider credentials invalid

## Webhook Fails Closed

Check:

```bash
kubectl get certificate -n imagebuilder-system
kubectl get secret imagebuilder-webhook-server-cert -n imagebuilder-system
kubectl describe validatingwebhookconfiguration imagebuilder-validating-webhook-configuration
```

Common causes:

- cert-manager is not installed.
- CA injection annotation was not processed.
- operator pod cannot mount `imagebuilder-webhook-server-cert`.

## Provider Does Not Become Healthy

Check:

```bash
kubectl describe platformprovider <name>
kubectl logs deploy/provider-<name>
```

Common causes:

- provider image rejected by supply-chain policy
- provider Deployment not ready
- gRPC service does not implement `GetCapabilities`
- `protocol_version` is not `v1`
- provider name does not match the expected provider identity

## Metrics Missing

Check:

```bash
kubectl get svc imagebuilder-operator-metrics -n imagebuilder-system
kubectl port-forward -n imagebuilder-system svc/imagebuilder-operator-metrics 8080:8080
curl localhost:8080/metrics
```

If using Prometheus Operator, ensure `ServiceMonitor` is installed and selected
by your Prometheus instance.
