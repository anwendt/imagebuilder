import argparse
import os
import re
from pathlib import Path


DIGEST_ENV_BY_REPOSITORY = {
    "ghcr.io/anwendt/imagebuilder-operator": "OPERATOR_DIGEST",
    "ghcr.io/anwendt/imagebuilder-builder": "BUILDER_DIGEST",
    "ghcr.io/anwendt/imagebuilder-uploader": "UPLOADER_DIGEST",
    "ghcr.io/anwendt/imagebuilder-provisioner-ansible": "PROVISIONER_ANSIBLE_DIGEST",
    "ghcr.io/anwendt/imagebuilder-provisioner-chef": "PROVISIONER_CHEF_DIGEST",
    "ghcr.io/anwendt/imagebuilder-provisioner-custom": "PROVISIONER_CUSTOM_DIGEST",
    "ghcr.io/anwendt/imagebuilder-provisioner-puppet": "PROVISIONER_PUPPET_DIGEST",
    "ghcr.io/anwendt/imagebuilder-provisioner-saltstack": "PROVISIONER_SALTSTACK_DIGEST",
}


def update_digests(values_path: Path) -> None:
    lines = values_path.read_text().splitlines()
    for repository, environment_name in DIGEST_ENV_BY_REPOSITORY.items():
        digest = os.environ.get(environment_name, "")
        if re.fullmatch(r"sha256:[0-9a-f]{64}", digest) is None:
            raise SystemExit(f"invalid release digest for {repository}: {digest!r}")

        repository_matches = [
            index
            for index, line in enumerate(lines)
            if line.strip() == f"repository: {repository}"
        ]
        if len(repository_matches) != 1:
            raise SystemExit(
                f"expected exactly one values entry for {repository}, "
                f"found {len(repository_matches)}"
            )

        repository_index = repository_matches[0]
        digest_matches = [
            index
            for index in range(repository_index + 1, min(repository_index + 4, len(lines)))
            if lines[index].strip().startswith("digest:")
        ]
        if len(digest_matches) != 1:
            raise SystemExit(f"expected one digest field after {repository}")

        digest_index = digest_matches[0]
        indentation = lines[digest_index][:-len(lines[digest_index].lstrip())]
        lines[digest_index] = f'{indentation}digest: "{digest}"'

    values_path.write_text("\n".join(lines) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Pin a release chart to the image digests built by GitHub Actions."
    )
    parser.add_argument("--values", required=True, type=Path)
    args = parser.parse_args()
    update_digests(args.values)


if __name__ == "__main__":
    main()
