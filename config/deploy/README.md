# Deployment Manifests

`operator.yaml` is a development manifest. It intentionally uses `:dev` images
and disables production provider policies so local kind and smoke-test workflows
can run without signed, digest-pinned provider images.

Use the Helm chart for production installs. The chart renders fail-closed
webhooks, NetworkPolicies, provider mTLS/digest/signature policy flags, and the
Kyverno image signature policy by default.
