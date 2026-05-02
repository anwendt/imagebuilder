# AWS Remote Build Operations

AWS remote builds execute the build lifecycle inside AWS instead of running a
local QEMU build job. The provider starts a temporary EC2 instance from an
existing source AMI, executes supported provisioners through AWS Systems Manager
(SSM), stops the instance, creates a final AMI, and waits until the AMI becomes
available.

## Supported Source

Remote AWS builds currently require `spec.source.providerRef` to be an AMI ID:

```yaml
spec:
  build:
    mode: remote
  source:
    type: cloud-image
    providerRef: ami-0123456789abcdef0
  targets:
    - providerConfigRef:
        name: aws-eu-west-1
      format: ami
```

`cloud-image` and `marketplace` are accepted as source types when the source
provider reference is an AMI ID. `spec.source.url` is reserved for downloadable
HTTPS sources and is not used for provider-native identifiers.

## ProviderConfig

The AWS provider supports the normal AWS SDK credential chain. Prefer IRSA,
workload identity, or assumed roles over static access keys.

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
metadata:
  name: aws-eu-west-1
spec:
  provider: aws
  region: eu-west-1
  extra:
    roleArn: arn:aws:iam::123456789012:role/ImageBuilderProviderRole
    remote.instanceType: t3.small
    remote.subnetId: subnet-0123456789abcdef0
    remote.securityGroupIds: sg-0123456789abcdef0
    remote.iamInstanceProfile: ImageBuilderInstanceProfile
    remote.kmsKeyId: alias/imagebuilder
    remote.rootVolumeSizeGiB: "32"
```

Required fields:

| Field | Purpose |
|---|---|
| `remote.instanceType` | EC2 instance type for the temporary build instance. |
| `remote.subnetId` | Subnet for the temporary build instance. |
| `remote.securityGroupIds` | Comma-separated security group IDs. |
| `remote.kmsKeyId` | KMS key for encrypted root volumes/snapshots. |

Recommended fields:

| Field | Purpose |
|---|---|
| `roleArn` | Role assumed by the provider before calling AWS APIs. |
| `remote.iamInstanceProfile` | Instance profile used by the build instance for SSM. Required when provisioners are configured. |
| `remote.rootVolumeSizeGiB` | Root volume size override. |

Security defaults:

| Field | Default | Purpose |
|---|---:|---|
| `remote.allowSshKey` | `false` | Allows setting `remote.keyName`. Keep disabled for SSM-only builds. |
| `remote.allowPublicIngress` | `false` | Allows security groups exposing SSH/WinRM to `0.0.0.0/0` or `::/0`. Should remain disabled. |

The provider rejects `remote.keyName` unless `remote.allowSshKey=true`.
The provider also rejects security groups that expose ports `22`, `5985`, or
`5986` to the public internet unless `remote.allowPublicIngress=true`.

## Provisioners

Provisioning runs through SSM. This avoids opening SSH or WinRM to the build
instance.

Supported provisioners:

| Provisioner | OS | SSM document |
|---|---|---|
| `shell` | Linux | `AWS-RunShellScript` |
| `powershell` | Windows | `AWS-RunPowerShellScript` |
| `file` | Linux/Windows | Writes inline content through a generated SSM command. |

Unsupported provisioner types fail the remote build explicitly. SSM command
stdout/stderr are not copied into Kubernetes status to avoid leaking secrets.

## IAM

Provider role minimum permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:RunInstances",
        "ec2:DescribeInstances",
        "ec2:DescribeSecurityGroups",
        "ec2:StopInstances",
        "ec2:TerminateInstances",
        "ec2:CreateImage",
        "ec2:ImportSnapshot",
        "ec2:DescribeImportSnapshotTasks",
        "ec2:CancelImportTask",
        "ec2:RegisterImage",
        "ec2:DescribeImages",
        "ec2:DeregisterImage",
        "ec2:DeleteSnapshot",
        "ec2:CreateTags"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": "arn:aws:s3:::my-imagebuilder-artifacts/imagebuilder/*"
    },
    {
      "Effect": "Allow",
      "Action": "sts:GetCallerIdentity",
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ssm:DescribeInstanceInformation",
        "ssm:SendCommand",
        "ssm:GetCommandInvocation",
        "ssm:ListCommandInvocations"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": "arn:aws:iam::123456789012:role/ImageBuilderInstanceRole"
    },
    {
      "Effect": "Allow",
      "Action": [
        "kms:CreateGrant",
        "kms:DescribeKey",
        "kms:Encrypt",
        "kms:Decrypt",
        "kms:ReEncrypt*",
        "kms:GenerateDataKey*"
      ],
      "Resource": "arn:aws:kms:eu-west-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    }
  ]
}
```

Narrow the EC2 resources with tags and ARNs where your account policy allows it.
The provider tags temporary resources with:

- `imagebuilder.io/build-id`
- `imagebuilder.io/namespace`
- `imagebuilder.io/image-name`

`RunInstances` uses a deterministic AWS `ClientToken` derived from the build ID.
Before creating a final AMI, the provider searches for an existing AMI with the
same generated name and build tag, so retrying after a controller restart does
not create duplicate AMIs.

## Local Build Upload/Register

For local QEMU builds that target AWS, the upload job uses the same AWS provider
but imports the produced disk artifact:

1. upload artifact to `s3://<s3Bucket>/<s3Prefix>/<buildID>/disk.<format>`;
2. call `ec2:ImportSnapshot` for `vmdk`, `raw`, or `vhd`;
3. call `ec2:RegisterImage` from the imported snapshot;
4. poll until the AMI is `available`;
5. clean up S3 objects, partial snapshots, and AMIs on failure.

Required `ProviderConfig.spec.extra` fields for local upload/register:

| Field | Purpose |
|---|---|
| `s3Bucket` | Bucket used as the transient VM import staging area. |

Optional fields:

| Field | Default |
|---|---|
| `s3Prefix` | `imagebuilder` |
| `registerTimeout` | `2h` |
| `rootVolumeSizeGiB` | omitted; AWS uses snapshot size |

VMImage target tags are propagated to the registered AMI, except AWS-reserved
tag keys beginning with `aws:`.

Instance profile requirements:

- Attach `AmazonSSMManagedInstanceCore` or equivalent least-privilege SSM policy.
- The source AMI must include a working SSM Agent.
- The instance must reach SSM endpoints through internet egress or VPC endpoints.

Recommended VPC endpoints for private subnets:

- `com.amazonaws.<region>.ssm`
- `com.amazonaws.<region>.ssmmessages`
- `com.amazonaws.<region>.ec2messages`
- `com.amazonaws.<region>.kms` when using a customer-managed KMS key

## Cleanup

On terminal provider-side failures, the AWS provider performs best-effort
cleanup:

- terminates the temporary EC2 instance
- deregisters failed intermediate AMIs
- deletes snapshots attached to failed intermediate AMIs

Polling and transient AWS API errors do not trigger cleanup by themselves,
because they may be recoverable on the next reconcile.

## E2E Test

The real AWS E2E test is opt-in because it creates billable AWS resources.

```bash
export AWS_E2E=1
export AWS_E2E_REGION=eu-west-1
export AWS_E2E_SOURCE_AMI=ami-0123456789abcdef0
export AWS_E2E_SUBNET_ID=subnet-0123456789abcdef0
export AWS_E2E_SECURITY_GROUP_IDS=sg-0123456789abcdef0
export AWS_E2E_INSTANCE_PROFILE=ImageBuilderInstanceProfile
export AWS_E2E_KMS_KEY_ID=alias/imagebuilder

make test-e2e-aws
```

Optional settings:

| Variable | Default |
|---|---|
| `AWS_E2E_ROLE_ARN` | unset |
| `AWS_E2E_INSTANCE_TYPE` | `t3.micro` |
| `AWS_E2E_ROOT_VOLUME_SIZE_GIB` | `16` |
| `AWS_E2E_TIMEOUT` | `45m` |
| `AWS_E2E_BUILD_TIMEOUT` | `40m` |
| `AWS_E2E_POLL_INTERVAL` | `20s` |

The test cleans up the temporary instance and final AMI/snapshots when it exits.
