#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  test/e2e/azure-tomcat-e2e.sh [prep|run|run-clean|cleanup|cleanup-rg|check|print-env]

Environment:
  The script loads this file when it exists:
    ~/.config/imagebuilder/azure-tomcat-e2e.env

  Override paths with:
    IMAGEBUILDER_REPO=/path/to/imagebuilder
    IMAGEBUILDER_AZURE_ENV=/path/to/envfile

Required for prep:
  AZURE_E2E_SUBSCRIPTION_ID
  AZURE_E2E_TENANT_ID
  AZURE_E2E_CLIENT_ID
  AZURE_E2E_CLIENT_SECRET
  AZURE_E2E_LOCATION
  AZURE_E2E_RESOURCE_GROUP

Prep creates or updates:
  AZURE_E2E_STORAGE_ACCOUNT
  AZURE_E2E_STORAGE_ACCOUNT_KEY
  AZURE_E2E_STORAGE_CONTAINER
  AZURE_E2E_SOURCE_TYPE=snapshot
  AZURE_E2E_SOURCE_ID
  AZURE_E2E_NETWORK_INTERFACE_ID

Optional prep settings:
  AZURE_E2E_MARKETPLACE_IMAGE=Canonical:ubuntu-24_04-lts:server:latest
  AZURE_E2E_PREP_PREFIX=imagebuilder-e2e
  AZURE_E2E_PREP_ADMIN_USER=azureuser
  AZURE_E2E_VM_SIZE=Standard_B2s

Required for run/check:
  AZURE_E2E_SUBSCRIPTION_ID
  AZURE_E2E_TENANT_ID
  AZURE_E2E_CLIENT_ID
  AZURE_E2E_CLIENT_SECRET
  AZURE_E2E_LOCATION
  AZURE_E2E_RESOURCE_GROUP
  AZURE_E2E_STORAGE_ACCOUNT
  AZURE_E2E_STORAGE_ACCOUNT_KEY
  AZURE_E2E_SOURCE_ID
  AZURE_E2E_NETWORK_INTERFACE_ID

Cleanup:
  run-clean runs the Azure Tomcat E2E test and cleans up test resources
  inside the configured resource group afterwards, even when the test fails.

  cleanup only deletes known test resources inside the configured resource
  group. It never deletes the resource group itself.

  cleanup-rg is disabled because the service principal is scoped to the
  resource group and the resource group must remain in place.
USAGE
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Required command not found: $1" >&2
    exit 1
  }
}

require_vars() {
  local missing=()
  local name
  for name in "$@"; do
    if [[ -z "${!name:-}" ]]; then
      missing+=("${name}")
    fi
  done
  if ((${#missing[@]} > 0)); then
    echo "Missing required Azure E2E variables:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    echo >&2
    echo "Create ${env_file} or export the variables in your shell." >&2
    exit 1
  fi
}

write_export() {
  local name="$1"
  local value="$2"
  local escaped
  escaped="$(printf '%q' "${value}")"
  mkdir -p "$(dirname "${env_file}")"
  touch "${env_file}"
  chmod 600 "${env_file}"
  if grep -q "^export ${name}=" "${env_file}"; then
    perl -0pi -e "s|^export ${name}=.*$|export ${name}=${escaped}|m" "${env_file}"
  else
    printf 'export %s=%s\n' "${name}" "${escaped}" >>"${env_file}"
  fi
  export "${name}=${value}"
}

azure_login() {
  require_command az
  az login \
    --service-principal \
    --username "${AZURE_E2E_CLIENT_ID}" \
    --password "${AZURE_E2E_CLIENT_SECRET}" \
    --tenant "${AZURE_E2E_TENANT_ID}" \
    --output none
  az account set --subscription "${AZURE_E2E_SUBSCRIPTION_ID}"
}

random_suffix() {
  printf '%s%s' "$(date +%s)" "${RANDOM}"
}

prep() {
  require_vars \
    AZURE_E2E_SUBSCRIPTION_ID \
    AZURE_E2E_TENANT_ID \
    AZURE_E2E_CLIENT_ID \
    AZURE_E2E_CLIENT_SECRET \
    AZURE_E2E_LOCATION \
    AZURE_E2E_RESOURCE_GROUP

  azure_login

  local rg="${AZURE_E2E_RESOURCE_GROUP}"
  local location="${AZURE_E2E_LOCATION}"
  local prefix="${AZURE_E2E_PREP_PREFIX:-imagebuilder-e2e}"
  local safe_prefix
  safe_prefix="$(printf '%s' "${prefix}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-' | cut -c1-32)"
  local suffix
  suffix="$(random_suffix)"
  local vm_name="${AZURE_E2E_PREP_VM_NAME:-${safe_prefix}-source-${suffix}}"
  local snapshot_name="${AZURE_E2E_PREP_SNAPSHOT_NAME:-${safe_prefix}-ubuntu-snapshot-${suffix}}"
  local nic_name="${AZURE_E2E_PREP_NIC_NAME:-${safe_prefix}-build-nic-${suffix}}"
  local admin_user="${AZURE_E2E_PREP_ADMIN_USER:-azureuser}"
  local image="${AZURE_E2E_MARKETPLACE_IMAGE:-Canonical:ubuntu-24_04-lts:server:latest}"
  local vm_size="${AZURE_E2E_VM_SIZE:-Standard_B2s}"
  local container="${AZURE_E2E_STORAGE_CONTAINER:-imagebuilder-e2e}"

  local existing_rg_location
  existing_rg_location="$(az group show --name "${rg}" --query location --output tsv 2>/dev/null || true)"
  if [[ -n "${existing_rg_location}" ]]; then
    if [[ "${existing_rg_location}" != "${location}" ]]; then
      echo "Resource group ${rg} already exists in ${existing_rg_location}; using that location instead of ${location}."
      location="${existing_rg_location}"
      write_export AZURE_E2E_LOCATION "${location}"
    else
      echo "Using existing resource group ${rg} in ${location}."
    fi
  else
    echo "Creating resource group ${rg} in ${location}..."
    az group create --name "${rg}" --location "${location}" --output none
  fi

  local storage="${AZURE_E2E_STORAGE_ACCOUNT:-}"
  if [[ -z "${storage}" ]]; then
    local compact_prefix
    compact_prefix="$(printf '%s' "${safe_prefix}" | tr -cd 'a-z0-9' | cut -c1-8)"
    storage="${compact_prefix}$(printf '%s' "${suffix}" | tr -cd '0-9' | cut -c1-12)"
    storage="$(printf '%s' "${storage}" | cut -c1-24)"
    write_export AZURE_E2E_STORAGE_ACCOUNT "${storage}"
  fi

  if ! az storage account show --resource-group "${rg}" --name "${storage}" >/dev/null 2>&1; then
    echo "Creating storage account ${storage}..."
    az storage account create \
      --resource-group "${rg}" \
      --name "${storage}" \
      --location "${location}" \
      --sku Standard_LRS \
      --kind StorageV2 \
      --https-only true \
      --min-tls-version TLS1_2 \
      --output none
  else
    echo "Using existing storage account ${storage}."
  fi

  local storage_key
  storage_key="$(az storage account keys list \
    --resource-group "${rg}" \
    --account-name "${storage}" \
    --query '[0].value' \
    --output tsv)"
  write_export AZURE_E2E_STORAGE_ACCOUNT_KEY "${storage_key}"
  write_export AZURE_E2E_STORAGE_CONTAINER "${container}"

  az storage container create \
    --name "${container}" \
    --account-name "${storage}" \
    --account-key "${storage_key}" \
    --output none

  echo "Creating Ubuntu source VM ${vm_name} from ${image}..."
  az vm create \
    --resource-group "${rg}" \
    --name "${vm_name}" \
    --location "${location}" \
    --image "${image}" \
    --size "${vm_size}" \
    --admin-username "${admin_user}" \
    --generate-ssh-keys \
    --public-ip-address "" \
    --output none

  local source_disk_id
  source_disk_id="$(az vm show \
    --resource-group "${rg}" \
    --name "${vm_name}" \
    --query 'storageProfile.osDisk.managedDisk.id' \
    --output tsv)"

  local source_nic_id
  source_nic_id="$(az vm nic list \
    --resource-group "${rg}" \
    --vm-name "${vm_name}" \
    --query '[0].id' \
    --output tsv)"

  local source_nsg_id
  source_nsg_id="$(az network nic show \
    --ids "${source_nic_id}" \
    --query 'networkSecurityGroup.id' \
    --output tsv 2>/dev/null || true)"

  local subnet_id
  subnet_id="$(az network nic show \
    --ids "${source_nic_id}" \
    --query 'ipConfigurations[0].subnet.id' \
    --output tsv)"
  local source_vnet_id="${subnet_id%/subnets/*}"

  echo "Creating source snapshot ${snapshot_name}..."
  az snapshot create \
    --resource-group "${rg}" \
    --name "${snapshot_name}" \
    --location "${location}" \
    --source "${source_disk_id}" \
    --sku Standard_LRS \
    --output none

  local snapshot_id
  snapshot_id="$(az snapshot show \
    --resource-group "${rg}" \
    --name "${snapshot_name}" \
    --query id \
    --output tsv)"

  echo "Creating unattached build NIC ${nic_name}..."
  az network nic create \
    --resource-group "${rg}" \
    --name "${nic_name}" \
    --location "${location}" \
    --subnet "${subnet_id}" \
    --output none

  local build_nic_id
  build_nic_id="$(az network nic show \
    --resource-group "${rg}" \
    --name "${nic_name}" \
    --query id \
    --output tsv)"

  echo "Deallocating source VM ${vm_name}..."
  az vm deallocate --resource-group "${rg}" --name "${vm_name}" --output none

  write_export AZURE_E2E_SOURCE_TYPE snapshot
  write_export AZURE_E2E_SOURCE_ID "${snapshot_id}"
  write_export AZURE_E2E_NETWORK_INTERFACE_ID "${build_nic_id}"
  write_export AZURE_E2E_VM_SIZE "${vm_size}"
  write_export AZURE_E2E_PREP_VM_NAME "${vm_name}"
  write_export AZURE_E2E_PREP_SOURCE_DISK_ID "${source_disk_id}"
  write_export AZURE_E2E_PREP_SOURCE_NIC_ID "${source_nic_id}"
  write_export AZURE_E2E_PREP_SOURCE_NSG_ID "${source_nsg_id}"
  write_export AZURE_E2E_PREP_SOURCE_VNET_ID "${source_vnet_id}"

  echo
  echo "Prep completed."
  echo "Env file: ${env_file}"
  echo "Source snapshot: ${snapshot_id}"
  echo "Build NIC: ${build_nic_id}"
  echo
  echo "Next:"
  echo "  imagebuilder-azure-tomcat-e2e check"
  echo "  imagebuilder-azure-tomcat-e2e run"
  echo "  imagebuilder-azure-tomcat-e2e run-clean"
}

cleanup_resource_group() {
  echo "Refusing to delete Azure resource group ${AZURE_E2E_RESOURCE_GROUP:-<unset>}." >&2
  echo "This script keeps the resource group because the service principal is scoped to it." >&2
  echo "Use 'imagebuilder-azure-tomcat-e2e cleanup' to delete only test resources inside the group." >&2
  exit 2
}

delete_resource_id() {
  local label="$1"
  local id="${2:-}"

  if [[ -z "${id}" ]]; then
    return 0
  fi

  echo "Deleting ${label}..."
  if ! az resource delete --ids "${id}" --only-show-errors >/dev/null 2>&1; then
    echo "Warning: could not delete ${label}; it may already be gone or still be in use." >&2
  fi
}

cleanup_resources() {
  require_vars \
    AZURE_E2E_SUBSCRIPTION_ID \
    AZURE_E2E_TENANT_ID \
    AZURE_E2E_CLIENT_ID \
    AZURE_E2E_CLIENT_SECRET \
    AZURE_E2E_RESOURCE_GROUP

  azure_login

  echo "Cleaning Azure E2E resources inside resource group ${AZURE_E2E_RESOURCE_GROUP}; the resource group itself will not be deleted."

  if [[ -n "${AZURE_E2E_PREP_VM_NAME:-}" ]]; then
    echo "Deleting prep source VM ${AZURE_E2E_PREP_VM_NAME}..."
    if ! az vm delete \
      --resource-group "${AZURE_E2E_RESOURCE_GROUP}" \
      --name "${AZURE_E2E_PREP_VM_NAME}" \
      --yes \
      --force-deletion true \
      --only-show-errors >/dev/null 2>&1; then
      echo "Warning: could not delete prep source VM; it may already be gone." >&2
    fi
  fi

  delete_resource_id "prep source disk" "${AZURE_E2E_PREP_SOURCE_DISK_ID:-}"
  delete_resource_id "prep source NIC" "${AZURE_E2E_PREP_SOURCE_NIC_ID:-}"
  delete_resource_id "build NIC" "${AZURE_E2E_NETWORK_INTERFACE_ID:-}"
  delete_resource_id "source snapshot" "${AZURE_E2E_SOURCE_ID:-}"
  delete_resource_id "prep source NSG" "${AZURE_E2E_PREP_SOURCE_NSG_ID:-}"
  delete_resource_id "prep source VNet" "${AZURE_E2E_PREP_SOURCE_VNET_ID:-}"

  if [[ -n "${AZURE_E2E_STORAGE_ACCOUNT:-}" ]]; then
    echo "Deleting storage account ${AZURE_E2E_STORAGE_ACCOUNT}..."
    if ! az storage account delete \
      --resource-group "${AZURE_E2E_RESOURCE_GROUP}" \
      --name "${AZURE_E2E_STORAGE_ACCOUNT}" \
      --yes \
      --only-show-errors >/dev/null 2>&1; then
      echo "Warning: could not delete storage account; it may already be gone." >&2
    fi
  fi

  echo "Azure E2E resource cleanup completed."
}

run_test() {
  if [[ ! -d "${repo}" ]]; then
    echo "Imagebuilder repo not found: ${repo}" >&2
    echo "Set IMAGEBUILDER_REPO=/path/to/imagebuilder if needed." >&2
    exit 1
  fi

  cd "${repo}"
  AZURE_E2E=1 AZURE_E2E_WORKLOAD=tomcat go test ./plugins/azure -run TestAzureRemoteBuildTomcat_E2E -count=1 -v -timeout=90m
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_repo="$(cd "${script_dir}/../.." && pwd)"
repo="${IMAGEBUILDER_REPO:-${default_repo}}"
env_file="${IMAGEBUILDER_AZURE_ENV:-${HOME}/.config/imagebuilder/azure-tomcat-e2e.env}"
action="${1:-run}"

case "${action}" in
  prep|run|run-clean|cleanup|cleanup-rg|check|print-env|-h|--help)
    ;;
  *)
    echo "Unsupported action: ${action}" >&2
    usage >&2
    exit 2
    ;;
esac

if [[ "${action}" == "-h" || "${action}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -f "${env_file}" ]]; then
  # shellcheck source=/dev/null
  source "${env_file}"
fi

export AZURE_E2E="${AZURE_E2E:-1}"
export AZURE_E2E_WORKLOAD="${AZURE_E2E_WORKLOAD:-tomcat}"
export AZURE_E2E_SOURCE_TYPE="${AZURE_E2E_SOURCE_TYPE:-managed-disk}"
export AZURE_E2E_STORAGE_CONTAINER="${AZURE_E2E_STORAGE_CONTAINER:-imagebuilder-e2e}"

if [[ "${action}" == "prep" ]]; then
  prep
  exit 0
fi

if [[ "${action}" == "cleanup-rg" ]]; then
  cleanup_resource_group
  exit 0
fi

if [[ "${action}" == "cleanup" ]]; then
  cleanup_resources
  exit 0
fi

require_vars \
  AZURE_E2E_SUBSCRIPTION_ID \
  AZURE_E2E_TENANT_ID \
  AZURE_E2E_CLIENT_ID \
  AZURE_E2E_CLIENT_SECRET \
  AZURE_E2E_LOCATION \
  AZURE_E2E_RESOURCE_GROUP \
  AZURE_E2E_STORAGE_ACCOUNT \
  AZURE_E2E_STORAGE_ACCOUNT_KEY \
  AZURE_E2E_SOURCE_ID \
  AZURE_E2E_NETWORK_INTERFACE_ID

case "${action}" in
  check)
    echo "Azure Tomcat E2E environment is complete."
    echo "Repo: ${repo}"
    echo "Env file: ${env_file}"
    exit 0
    ;;
  print-env)
    printf 'export AZURE_E2E=%q\n' "${AZURE_E2E}"
    printf 'export AZURE_E2E_WORKLOAD=%q\n' "${AZURE_E2E_WORKLOAD}"
    printf 'export AZURE_E2E_LOCATION=%q\n' "${AZURE_E2E_LOCATION}"
    printf 'export AZURE_E2E_RESOURCE_GROUP=%q\n' "${AZURE_E2E_RESOURCE_GROUP}"
    printf 'export AZURE_E2E_STORAGE_ACCOUNT=%q\n' "${AZURE_E2E_STORAGE_ACCOUNT}"
    printf 'export AZURE_E2E_STORAGE_CONTAINER=%q\n' "${AZURE_E2E_STORAGE_CONTAINER}"
    printf 'export AZURE_E2E_SOURCE_TYPE=%q\n' "${AZURE_E2E_SOURCE_TYPE}"
    printf 'export AZURE_E2E_SOURCE_ID=%q\n' "${AZURE_E2E_SOURCE_ID}"
    printf 'export AZURE_E2E_NETWORK_INTERFACE_ID=%q\n' "${AZURE_E2E_NETWORK_INTERFACE_ID}"
    exit 0
    ;;
esac

if [[ "${action}" == "run-clean" ]]; then
  status=0
  run_test || status=$?
  cleanup_resources
  exit "${status}"
fi

run_test
