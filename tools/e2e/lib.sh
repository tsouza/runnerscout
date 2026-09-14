#!/usr/bin/env bash
# Shared helpers for tools/e2e/*.sh. Sourced, never executed directly - see
# docs/e2e-qualification.md for the full harness this supports and the exact
# environment variables each script reads.
#
# Every script that sources this file inherits the same two speed bumps
# qualify-aws.yml/qualify-azure.yml/qualify-gcp.yml enforce before they ever
# touch a real credential: e2e_require_confirmation (the exact-phrase spend
# confirmation) and e2e_require_ceiling (a numeric runtime bound no input can
# raise past a hard-coded constant). Both are checked again independently in
# every script that can reach a billable or GitHub-mutating call, exactly
# like aws_realcloud_test.go's/azure_realcloud_test.go's/gcp_realcloud_test.go's
# requireRealCloudConfirmation is independent of qualify-aws.yml's/
# qualify-azure.yml's/qualify-gcp.yml's own workflow-level check - so no
# single script in this directory is "the" safety gate that a shorter call
# path could bypass.
set -euo pipefail

# RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES mirrors
# RUNNERSCOUT_QUALIFY_MAX_RUNTIME_CEILING_MINUTES in qualify-aws.yml/
# qualify-azure.yml/qualify-gcp.yml: a hard-coded ceiling no
# E2E_MAX_RUNTIME_MINUTES value can exceed. This harness does strictly more
# than those workflows (cluster bring-up, real scale-set registration, a
# real dispatched job, real runner boot/registration/job pickup) so its
# ceiling is deliberately higher.
readonly RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES=60
readonly RUNNERSCOUT_E2E_CONFIRM_PHRASE='I-UNDERSTAND-THIS-COSTS-REAL-MONEY'

e2e_log() {
  echo "[$(date -u +%H:%M:%S)] $*" >&2
}

e2e_die() {
  echo "::error:: $*" >&2
  exit 1
}

e2e_require_var() {
  local name="$1"
  if [ -z "${!name:-}" ]; then
    e2e_die "required environment variable $name is not set (see docs/e2e-qualification.md)"
  fi
}

# e2e_require_confirmation refuses to continue unless E2E_CONFIRM_REAL_SPEND
# is exactly the same literal phrase qualify-aws.yml/qualify-azure.yml/
# qualify-gcp.yml use for confirm_real_spend - one phrase across every
# real-spend entry point in this repository, deliberately not
# scriptable-by-default (see docs/qualification-real-cloud.background.md's
# "Why a second confirmation phrase beyond workflow_dispatch").
e2e_require_confirmation() {
  if [ "${E2E_CONFIRM_REAL_SPEND:-}" != "$RUNNERSCOUT_E2E_CONFIRM_PHRASE" ]; then
    e2e_die "E2E_CONFIRM_REAL_SPEND must be exactly '$RUNNERSCOUT_E2E_CONFIRM_PHRASE' - refusing to provision a real scale set, real cloud VM or dispatch a real workflow without this exact literal confirmation"
  fi
}

# e2e_require_ceiling validates E2E_MAX_RUNTIME_MINUTES is a plain integer
# within (1, RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES]. Every script that
# enforces a wall-clock budget re-derives its own deadline from this value
# rather than trusting a prior script's already-validated copy, matching
# qualify-aws.yml's/qualify-azure.yml's/qualify-gcp.yml's own "recompute from
# inputs/env directly" discipline.
e2e_require_ceiling() {
  local max="${E2E_MAX_RUNTIME_MINUTES:-}"
  if ! [[ "$max" =~ ^[0-9]+$ ]] || [ "$max" -lt 1 ] || [ "$max" -gt "$RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES" ]; then
    e2e_die "E2E_MAX_RUNTIME_MINUTES must be a plain integer between 1 and $RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES (hard-coded harness ceiling); got '${max:-<unset>}'"
  fi
}

# e2e_deadline_epoch prints the unix timestamp E2E_MAX_RUNTIME_MINUTES from
# now, for scripts that need to poll against a hard wall-clock budget rather
# than a single blocking command's own timeout.
e2e_deadline_epoch() {
  echo $(( $(date +%s) + E2E_MAX_RUNTIME_MINUTES * 60 ))
}

# e2e_refresh_aws_catalog_inputs cross-validates the pinned AWS network
# identifiers (same read-only checks qualify-aws.yml's own pre-flight step
# performs) and exports the derived/observed values both bring-up.sh (to
# render the initial resource graph) and dispatch-and-wait.sh (to re-render
# a fresh, non-stale CapacityCatalog immediately before dispatch - see
# manifests/capacity-catalog.yaml.tmpl's own header comment for why) need:
# E2E_AWS_ZONE, E2E_AWS_SUBNET_CIDR, E2E_AWS_PRICE_MICROS,
# E2E_AWS_PRICE_OBSERVED_AT. Defined once here rather than duplicated in both
# scripts so the two can never silently drift on how a real price becomes a
# priceMicros integer.
e2e_refresh_aws_catalog_inputs() {
  local account_id subnet_vpc sg_vpc spot_price_usd
  account_id="$(aws sts get-caller-identity --query Account --output text)"
  [ "$account_id" = "$E2E_AWS_ACCOUNT_ID" ] || e2e_die "authenticated AWS account $account_id does not match E2E_AWS_ACCOUNT_ID=$E2E_AWS_ACCOUNT_ID"
  # shellcheck disable=SC2153  # E2E_AWS_SUBNET_ID is a real, required env var - not a typo of E2E_AWS_SUBNET_CIDR, which this function itself derives and exports below.
  subnet_vpc="$(aws ec2 describe-subnets --region "$E2E_AWS_REGION" --subnet-ids "$E2E_AWS_SUBNET_ID" --query 'Subnets[0].VpcId' --output text)"
  sg_vpc="$(aws ec2 describe-security-groups --region "$E2E_AWS_REGION" --group-ids "$E2E_AWS_SECURITY_GROUP_ID" --query 'SecurityGroups[0].VpcId' --output text)"
  [ "$subnet_vpc" = "$E2E_AWS_VPC_ID" ] && [ "$sg_vpc" = "$E2E_AWS_VPC_ID" ] || e2e_die "subnet ($subnet_vpc) and/or security group ($sg_vpc) do not both belong to E2E_AWS_VPC_ID=$E2E_AWS_VPC_ID"
  export E2E_AWS_ZONE
  E2E_AWS_ZONE="$(aws ec2 describe-subnets --region "$E2E_AWS_REGION" --subnet-ids "$E2E_AWS_SUBNET_ID" --query 'Subnets[0].AvailabilityZone' --output text)"
  export E2E_AWS_SUBNET_CIDR
  E2E_AWS_SUBNET_CIDR="$(aws ec2 describe-subnets --region "$E2E_AWS_REGION" --subnet-ids "$E2E_AWS_SUBNET_ID" --query 'Subnets[0].CidrBlock' --output text)"
  spot_price_usd="$(aws ec2 describe-spot-price-history --region "$E2E_AWS_REGION" --instance-types "$E2E_AWS_INSTANCE_TYPE" --availability-zone "$E2E_AWS_ZONE" --product-descriptions 'Linux/UNIX' --max-items 1 --query 'SpotPriceHistory[0].SpotPrice' --output text)"
  [ "$spot_price_usd" != "None" ] || e2e_die "no recent Spot price history for $E2E_AWS_INSTANCE_TYPE in $E2E_AWS_ZONE"
  # Same round(price * 1e6) rule internal/prices/aws.go's awsSpotPriceMicros
  # applies - this catalog price must mean the same thing the production
  # price observer would have computed, not an approximation of it.
  export E2E_AWS_PRICE_MICROS
  E2E_AWS_PRICE_MICROS="$(python3 -c "import sys; print(round(float(sys.argv[1]) * 1e6))" "$spot_price_usd")"
  export E2E_AWS_PRICE_OBSERVED_AT
  E2E_AWS_PRICE_OBSERVED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

# e2e_refresh_azure_catalog_inputs is the Azure sibling of
# e2e_refresh_aws_catalog_inputs, exporting the same kind of
# derived/observed values both bring-up-azure.sh and
# dispatch-and-wait-azure.sh need: E2E_AZURE_SUBNET_CIDR,
# E2E_AZURE_PRICE_MICROS, E2E_AZURE_PRICE_OBSERVED_AT. It deliberately
# mirrors qualify-azure.yml's own pre-flight step almost verbatim (structural
# ARM-ID nesting check, then read-only `az resource show`/`az account show`
# cross-validation) rather than reinventing Azure safety checks, per
# docs/qualification-real-cloud.md's/qualify-azure.yml's already-reviewed
# Azure pattern.
#
# Unlike AWS, the Availability Zone is a required operator input
# (E2E_AZURE_ZONE), never derived here - see
# docs/e2e-qualification.background.md's Azure section for why (Azure's
# managed-image API has no architecture concept and no per-resource zone
# derivation the way a subnet implies an AWS AZ).
#
# The Spot price observation itself needs no Azure credential at all -
# internal/prices/azure.go's AzureSpotClient talks to Azure's public,
# unauthenticated Retail Prices API - but this function still requires an
# authenticated `az` CLI session for the network/identity cross-validation
# half of its job, exactly like qualify-azure.yml's own pre-flight assumes
# `azure/login` already ran.
e2e_refresh_azure_catalog_inputs() {
  local account_id rg vnet_lc subnet_lc price_json match_count retail_price currency
  lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

  account_id="$(az account show --query id -o tsv)"
  [ "$account_id" = "$E2E_AZURE_SUBSCRIPTION_ID" ] || e2e_die "az login resolved subscription '$account_id', expected E2E_AZURE_SUBSCRIPTION_ID=$E2E_AZURE_SUBSCRIPTION_ID"

  vnet_lc="$(lc "$E2E_AZURE_VNET_ID")"
  # shellcheck disable=SC2153  # E2E_AZURE_SUBNET_ID is a real, required env var - not a typo of E2E_AZURE_SUBNET_CIDR, which this function itself derives and exports below (same pattern as e2e_refresh_aws_catalog_inputs's identical disable above).
  subnet_lc="$(lc "$E2E_AZURE_SUBNET_ID")"
  case "$subnet_lc" in
    "$vnet_lc/subnets/"*) ;;
    *) e2e_die "E2E_AZURE_SUBNET_ID ('$E2E_AZURE_SUBNET_ID') is not a /subnets/* child of E2E_AZURE_VNET_ID ('$E2E_AZURE_VNET_ID')" ;;
  esac

  for id in "$E2E_AZURE_VNET_ID" "$E2E_AZURE_SUBNET_ID" "$E2E_AZURE_NSG_ID" "$E2E_AZURE_IMAGE_ID"; do
    rg="$(az resource show --ids "$id" --query resourceGroup -o tsv)"
    if [ "$(lc "$rg")" != "$(lc "$E2E_AZURE_RESOURCE_GROUP")" ]; then
      e2e_die "resource '$id' belongs to resource group '$rg', not E2E_AZURE_RESOURCE_GROUP='$E2E_AZURE_RESOURCE_GROUP' - refusing to proceed"
    fi
  done

  export E2E_AZURE_SUBNET_CIDR
  E2E_AZURE_SUBNET_CIDR="$(az resource show --ids "$E2E_AZURE_SUBNET_ID" --query 'properties.addressPrefix' -o tsv)"
  [ -n "$E2E_AZURE_SUBNET_CIDR" ] && [ "$E2E_AZURE_SUBNET_CIDR" != "None" ] || e2e_die "could not resolve an addressPrefix for E2E_AZURE_SUBNET_ID=$E2E_AZURE_SUBNET_ID"

  # Same $filter this repository's own internal/prices/azure.go issues
  # (serviceName/priceType/armRegionName/armSkuName exact match, Spot meter
  # via contains(meterName, 'Spot')), and the same client-side "exactly one
  # non-Windows match" ambiguity rule azureUnambiguousSpotMatch enforces -
  # this catalog price must mean the same thing the production price
  # observer would have computed, not an approximation of it. No Azure
  # credential is used for this call: the Retail Prices API is public.
  price_json="$(curl -sS -f -G 'https://prices.azure.com/api/retail/prices' \
    --data-urlencode 'currencyCode=USD' \
    --data-urlencode "\$filter=serviceName eq 'Virtual Machines' and priceType eq 'Consumption' and armRegionName eq '$E2E_AZURE_REGION' and armSkuName eq '$E2E_AZURE_VM_SIZE' and contains(meterName, 'Spot')")" \
    || e2e_die "Azure Retail Prices API request failed for $E2E_AZURE_VM_SIZE in $E2E_AZURE_REGION"
  if [ -n "$(printf '%s' "$price_json" | jq -r '.NextPageLink // empty')" ]; then
    e2e_die "Azure Retail Prices API response paginated for $E2E_AZURE_VM_SIZE in $E2E_AZURE_REGION - ambiguous match"
  fi
  match_count="$(printf '%s' "$price_json" | jq '[.Items[] | select(.serviceName == "Virtual Machines" and .type == "Consumption" and .armRegionName == $r and .armSkuName == $s and (.meterName | contains("Spot")) and (.productName | contains("Windows") | not))] | length' --arg r "$E2E_AZURE_REGION" --arg s "$E2E_AZURE_VM_SIZE")"
  [ "$match_count" -eq 1 ] || e2e_die "Azure Retail Prices API returned $match_count matching Spot entries for $E2E_AZURE_VM_SIZE in $E2E_AZURE_REGION (expected exactly 1)"
  retail_price="$(printf '%s' "$price_json" | jq -r '[.Items[] | select(.serviceName == "Virtual Machines" and .type == "Consumption" and .armRegionName == $r and .armSkuName == $s and (.meterName | contains("Spot")) and (.productName | contains("Windows") | not))][0].retailPrice' --arg r "$E2E_AZURE_REGION" --arg s "$E2E_AZURE_VM_SIZE")"
  currency="$(printf '%s' "$price_json" | jq -r '[.Items[] | select(.serviceName == "Virtual Machines" and .type == "Consumption" and .armRegionName == $r and .armSkuName == $s and (.meterName | contains("Spot")) and (.productName | contains("Windows") | not))][0].currencyCode' --arg r "$E2E_AZURE_REGION" --arg s "$E2E_AZURE_VM_SIZE")"
  [ "$currency" = "USD" ] || e2e_die "Azure spot price currency '$currency' unexpected (wanted USD)"
  python3 -c "import sys; assert float(sys.argv[1]) > 0" "$retail_price" || e2e_die "Azure spot price value '$retail_price' invalid"

  # Same round(price * 1e6) rule internal/prices/azure.go's
  # azureRetailPriceMicros applies.
  export E2E_AZURE_PRICE_MICROS
  E2E_AZURE_PRICE_MICROS="$(python3 -c "import sys; print(round(float(sys.argv[1]) * 1e6))" "$retail_price")"
  export E2E_AZURE_PRICE_OBSERVED_AT
  E2E_AZURE_PRICE_OBSERVED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

# e2e_refresh_gcp_catalog_inputs cross-validates the pinned GCP network
# identifiers and boot image, mirroring qualify-gcp.yml's own pre-flight
# step almost verbatim (same three read-only `gcloud ... describe` checks:
# zone-belongs-to-region, subnetwork-belongs-to-network, image resolves).
# Unlike e2e_refresh_aws_catalog_inputs, this never observes a real price -
# GCP has no live spot-pricing client anywhere in this codebase, permanently,
# by design (docs/prices-gcp.md/.background.md, issue #2). It only refreshes
# E2E_GCP_PRICE_OBSERVED_AT to "now", because internal/placement.MaxPriceAge
# (5 minutes) rejects a stale offering regardless of provider - see
# manifests/capacity-catalog-gcp.yaml.tmpl's own header comment for why that
# still matters even though the price itself is static.
e2e_refresh_gcp_catalog_inputs() {
  local zone_region subnet_network
  zone_region="$(gcloud compute zones describe "$E2E_GCP_ZONE" --project "$E2E_GCP_PROJECT" --format='value(region.basename())')"
  [ "$zone_region" = "$E2E_GCP_REGION" ] || e2e_die "E2E_GCP_ZONE '$E2E_GCP_ZONE' belongs to region '$zone_region', not E2E_GCP_REGION='$E2E_GCP_REGION'"
  subnet_network="$(gcloud compute networks subnets describe "$E2E_GCP_SUBNETWORK" --project "$E2E_GCP_PROJECT" --region "$E2E_GCP_REGION" --format='value(network.basename())')"
  [ "$subnet_network" = "$E2E_GCP_NETWORK" ] || e2e_die "E2E_GCP_SUBNETWORK '$E2E_GCP_SUBNETWORK' (region $E2E_GCP_REGION) belongs to network '$subnet_network', not E2E_GCP_NETWORK='$E2E_GCP_NETWORK'"
  if ! gcloud compute images describe "$E2E_GCP_IMAGE" --project "$E2E_GCP_PROJECT" >/dev/null 2>&1; then
    e2e_die "E2E_GCP_IMAGE '$E2E_GCP_IMAGE' could not be resolved in project '$E2E_GCP_PROJECT'"
  fi
  export E2E_GCP_SUBNET_CIDR
  E2E_GCP_SUBNET_CIDR="$(gcloud compute networks subnets describe "$E2E_GCP_SUBNETWORK" --project "$E2E_GCP_PROJECT" --region "$E2E_GCP_REGION" --format='value(ipCidrRange)')"
  export E2E_GCP_PRICE_OBSERVED_AT
  E2E_GCP_PRICE_OBSERVED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

# e2e_default_catalog_vars exports the optional-with-a-default resource-graph
# inputs manifests/capacity-catalog*.yaml.tmpl (and, for cpu/memory,
# manifests/runner-class*.yaml.tmpl) read, so bring-up*.sh and
# dispatch-and-wait*.sh - two separate script invocations, each with its own
# process environment, since one script's own `export` does not reach a
# later, separately-invoked script - always agree on the same defaults
# rather than each hardcoding its own copy that could silently drift. Called
# by every AWS/Azure bring-up/dispatch-and-wait script before any e2e_render
# call (GCP has its own e2e_default_gcp_catalog_vars below, self-contained
# rather than sharing this one - see that function's own doc comment for
# why). Cloud-specific image/architecture/machine-type identifiers are
# deliberately NOT defaulted here: they have no safe default (an
# operator-chosen image/architecture pair must match exactly across
# bring-up and every later catalog refresh), so each cloud's own script
# requires them explicitly via e2e_require_var instead.
e2e_default_catalog_vars() {
  export E2E_AWS_OFFERING_CPU="${E2E_AWS_OFFERING_CPU:-2}"
  export E2E_AWS_OFFERING_MEMORY_MIB="${E2E_AWS_OFFERING_MEMORY_MIB:-4096}"
  export E2E_AWS_MAX_PRICE_MICROS="${E2E_AWS_MAX_PRICE_MICROS:-200000}"
  export E2E_AZURE_OFFERING_CPU="${E2E_AZURE_OFFERING_CPU:-2}"
  export E2E_AZURE_OFFERING_MEMORY_MIB="${E2E_AZURE_OFFERING_MEMORY_MIB:-4096}"
  export E2E_AZURE_MAX_PRICE_MICROS="${E2E_AZURE_MAX_PRICE_MICROS:-200000}"
  export E2E_PROVISIONING_SECONDS="${E2E_PROVISIONING_SECONDS:-300}"
  export E2E_MAX_LIFETIME_SECONDS="${E2E_MAX_LIFETIME_SECONDS:-1800}"
}

# e2e_default_gcp_catalog_vars is e2e_default_catalog_vars's GCP counterpart.
# It also (re-)exports E2E_PROVISIONING_SECONDS/E2E_MAX_LIFETIME_SECONDS with
# the same defaults as the AWS/Azure function, rather than sharing one
# function across every provider: bring-up-gcp.sh/dispatch-and-wait-gcp.sh
# never source anything AWS/Azure-specific, so they need their own
# self-contained default-setting call. E2E_GCP_IMAGE has no safe default,
# for the same reason E2E_AWS_AMI_ID does not - both scripts require it
# explicitly via e2e_require_var instead. E2E_GCP_ARCHITECTURE, unlike
# E2E_AWS_ARCHITECTURE, DOES get a default: internal/provider/gcp_sdk.go's
# createGCP never reads Offering.Architecture at all (see
# docs/qualification-real-cloud.md's GCP section - a named, real
# adapter-level gap, not something this harness can paper over with a
# stricter default), so there is no cross-check an operator-supplied value
# could possibly satisfy or violate here. E2E_GCP_PRICE_MICROS is a fixed,
# operator-overridable constant - never a real observation, see
# manifests/capacity-catalog-gcp.yaml.tmpl.
e2e_default_gcp_catalog_vars() {
  export E2E_GCP_OFFERING_CPU="${E2E_GCP_OFFERING_CPU:-2}"
  export E2E_GCP_OFFERING_MEMORY_MIB="${E2E_GCP_OFFERING_MEMORY_MIB:-4096}"
  export E2E_GCP_ARCHITECTURE="${E2E_GCP_ARCHITECTURE:-amd64}"
  export E2E_GCP_MAX_PRICE_MICROS="${E2E_GCP_MAX_PRICE_MICROS:-200000}"
  export E2E_GCP_PRICE_MICROS="${E2E_GCP_PRICE_MICROS:-10000}"
  export E2E_PROVISIONING_SECONDS="${E2E_PROVISIONING_SECONDS:-300}"
  export E2E_MAX_LIFETIME_SECONDS="${E2E_MAX_LIFETIME_SECONDS:-1800}"
}

# e2e_render renders tools/e2e/manifests/$1 via envsubst into $2, restricted
# to the explicit variable allowlist every manifest template draws from -
# never a bare `envsubst` with no argument, which would also rewrite any
# stray, unrelated "$"-shaped text a future manifest edit might introduce.
# The allowlist below is a superset covering every cloud's own manifests;
# envsubst only ever substitutes a name that is both listed here AND
# actually present in the template being rendered, so listing e.g. Azure or
# GCP names here is harmless for an AWS template and vice versa.
e2e_render() {
  # shellcheck disable=SC2016  # single-quoted deliberately: this is envsubst's own variable-name allowlist argument, never meant to be shell-expanded here - envsubst expands these names itself, against process environment, when it reads stdin below.
  envsubst '${E2E_NAMESPACE} ${E2E_AWS_ACCOUNT_ID} ${E2E_AWS_SUBNET_ID} ${E2E_AWS_SECURITY_GROUP_ID} ${E2E_AWS_REGION} ${E2E_AWS_VPC_ID} ${E2E_AWS_SUBNET_CIDR} ${E2E_AWS_ZONE} ${E2E_AWS_INSTANCE_TYPE} ${E2E_AWS_AMI_ID} ${E2E_AWS_OFFERING_CPU} ${E2E_AWS_OFFERING_MEMORY_MIB} ${E2E_AWS_ARCHITECTURE} ${E2E_AWS_PRICE_MICROS} ${E2E_AWS_PRICE_OBSERVED_AT} ${E2E_AWS_MAX_PRICE_MICROS} ${E2E_AZURE_SUBSCRIPTION_ID} ${E2E_AZURE_RESOURCE_GROUP} ${E2E_AZURE_VNET_ID} ${E2E_AZURE_SUBNET_ID} ${E2E_AZURE_NSG_ID} ${E2E_AZURE_IMAGE_ID} ${E2E_AZURE_VM_SIZE} ${E2E_AZURE_REGION} ${E2E_AZURE_ZONE} ${E2E_AZURE_ARCHITECTURE} ${E2E_AZURE_SUBNET_CIDR} ${E2E_AZURE_PRICE_MICROS} ${E2E_AZURE_PRICE_OBSERVED_AT} ${E2E_AZURE_OFFERING_CPU} ${E2E_AZURE_OFFERING_MEMORY_MIB} ${E2E_AZURE_MAX_PRICE_MICROS} ${E2E_AZURE_SSH_PUBLIC_KEY} ${E2E_GCP_PROJECT} ${E2E_GCP_REGION} ${E2E_GCP_ZONE} ${E2E_GCP_NETWORK} ${E2E_GCP_SUBNETWORK} ${E2E_GCP_SUBNET_CIDR} ${E2E_GCP_IMAGE} ${E2E_GCP_MACHINE_TYPE} ${E2E_GCP_OFFERING_CPU} ${E2E_GCP_OFFERING_MEMORY_MIB} ${E2E_GCP_ARCHITECTURE} ${E2E_GCP_PRICE_MICROS} ${E2E_GCP_PRICE_OBSERVED_AT} ${E2E_GCP_MAX_PRICE_MICROS} ${E2E_SCALE_SET_NAME} ${E2E_REGISTERED_SCALE_SET_ID} ${E2E_GITHUB_URL} ${E2E_GITHUB_APP_CLIENT_ID} ${E2E_GITHUB_APP_INSTALLATION_ID} ${E2E_PROVISIONING_SECONDS} ${E2E_MAX_LIFETIME_SECONDS}' \
    < "tools/e2e/manifests/$1" > "$2"
}
