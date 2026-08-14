# Namespace-scoped operator mode

Namespace-scoped mode limits the controller-runtime cache and namespaced RBAC to one workload namespace. It reduces the impact of an operator compromise from every namespace to the selected namespace, especially for ProviderConfig credential Secrets.

Enable it with Helm:

```yaml
operator:
  namespaceScopedMode: true
  watchNamespace: imagebuilder-system
schedulerNamespace: imagebuilder-system
providerNamespace: imagebuilder-system
```

The watch, scheduler, and provider namespaces must be identical. The chart renders a Role and RoleBinding for VMImages, ProviderConfigs, Jobs, Pods, PVCs, Secrets, Events, Leases, provider Deployments, and Services. A small read/write ClusterRole remains for the cluster-scoped PlatformProvider CRD and read-only signature-policy/webhook discovery.

The default remains cluster-scoped for backward compatibility. To serve multiple tenant namespaces with reduced Secret scope, install one namespace-scoped release per tenant namespace with a distinct release name and leader-election identity boundary.

Namespace-scoped mode does not make a namespace safe from users who can create arbitrary Pods in that same namespace. Apply normal tenant RBAC, Pod Security admission, ResourceQuota, NetworkPolicy, and Secret access controls as well.
