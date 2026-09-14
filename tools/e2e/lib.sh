#!/usr/bin/env bash
# Shared helpers for tools/e2e/*.sh. Sourced, never executed directly - see
# docs/e2e-qualification.md for the full harness this supports and the exact
# environment variables each script reads.
#
# Every script that sources this file inherits the same two speed bumps
# qualify-aws.yml enforces before it ever touches a real credential:
# e2e_require_confirmation (the exact-phrase spend confirmation) and
# e2e_require_ceiling (a numeric runtime bound no input can raise past a
# hard-coded constant). Both are checked again independently in every script
# that can reach a billable or GitHub-mutating call, exactly like
# aws_realcloud_test.go's requireRealCloudConfirmation is independent of
# qualify-aws.yml's own workflow-level check - so no single script in this
# directory is "the" safety gate that a shorter call path could bypass.
set -euo pipefail

# RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES mirrors
# RUNNERSCOUT_QUALIFY_MAX_RUNTIME_CEILING_MINUTES in qualify-aws.yml: a
# hard-coded ceiling no E2E_MAX_RUNTIME_MINUTES value can exceed. This harness
# does strictly more than qualify-aws.yml (cluster bring-up, real scale-set
# registration, a real dispatched job, real runner boot/registration/job
# pickup) so its ceiling is deliberately higher.
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
# is exactly the same literal phrase qualify-aws.yml/qualify-gcp.yml use for
# confirm_real_spend - one phrase across every real-spend entry point in this
# repository, deliberately not scriptable-by-default (see
# docs/qualification-real-cloud.background.md's "Why a second confirmation
# phrase beyond workflow_dispatch").
e2e_require_confirmation() {
  if [ "${E2E_CONFIRM_REAL_SPEND:-}" != "$RUNNERSCOUT_E2E_CONFIRM_PHRASE" ]; then
    e2e_die "E2E_CONFIRM_REAL_SPEND must be exactly '$RUNNERSCOUT_E2E_CONFIRM_PHRASE' - refusing to provision a real scale set, real cloud VM or dispatch a real workflow without this exact literal confirmation"
  fi
}

# e2e_require_ceiling validates E2E_MAX_RUNTIME_MINUTES is a plain integer
# within (1, RUNNERSCOUT_E2E_MAX_RUNTIME_CEILING_MINUTES]. Every script that
# enforces a wall-clock budget re-derives its own deadline from this value
# rather than trusting a prior script's already-validated copy, matching
# qualify-aws.yml's own "recompute from inputs/env directly" discipline.
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

# e2e_default_catalog_vars exports the optional-with-a-default resource-graph
# inputs manifests/capacity-catalog.yaml.tmpl (and, for cpu/memory,
# manifests/runner-class.yaml.tmpl) read, so bring-up.sh and
# dispatch-and-wait.sh - two separate script invocations, each with its own
# process environment, since bring-up.sh's own `export` does not reach a
# later, separately-invoked script - always agree on the same defaults
# rather than each hardcoding its own copy that could silently drift. Called
# by both scripts before any e2e_render call. E2E_AWS_AMI_ID and
# E2E_AWS_ARCHITECTURE are deliberately NOT defaulted here: they have no
# safe default (an operator-chosen AMI/architecture pair must match exactly
# across bring-up and every later catalog refresh), so both scripts require
# them explicitly via e2e_require_var instead.
e2e_default_catalog_vars() {
  export E2E_AWS_OFFERING_CPU="${E2E_AWS_OFFERING_CPU:-2}"
  export E2E_AWS_OFFERING_MEMORY_MIB="${E2E_AWS_OFFERING_MEMORY_MIB:-4096}"
  export E2E_AWS_MAX_PRICE_MICROS="${E2E_AWS_MAX_PRICE_MICROS:-200000}"
  export E2E_PROVISIONING_SECONDS="${E2E_PROVISIONING_SECONDS:-300}"
  export E2E_MAX_LIFETIME_SECONDS="${E2E_MAX_LIFETIME_SECONDS:-1800}"
}

# e2e_render renders tools/e2e/manifests/$1 via envsubst into $2, restricted
# to the explicit variable allowlist every manifest template draws from -
# never a bare `envsubst` with no argument, which would also rewrite any
# stray, unrelated "$"-shaped text a future manifest edit might introduce.
e2e_render() {
  # shellcheck disable=SC2016  # single-quoted deliberately: this is envsubst's own variable-name allowlist argument, never meant to be shell-expanded here - envsubst expands these names itself, against process environment, when it reads stdin below.
  envsubst '${E2E_NAMESPACE} ${E2E_AWS_ACCOUNT_ID} ${E2E_AWS_SUBNET_ID} ${E2E_AWS_SECURITY_GROUP_ID} ${E2E_AWS_REGION} ${E2E_AWS_VPC_ID} ${E2E_AWS_SUBNET_CIDR} ${E2E_AWS_ZONE} ${E2E_AWS_INSTANCE_TYPE} ${E2E_AWS_AMI_ID} ${E2E_AWS_OFFERING_CPU} ${E2E_AWS_OFFERING_MEMORY_MIB} ${E2E_AWS_ARCHITECTURE} ${E2E_AWS_PRICE_MICROS} ${E2E_AWS_PRICE_OBSERVED_AT} ${E2E_AWS_MAX_PRICE_MICROS} ${E2E_SCALE_SET_NAME} ${E2E_REGISTERED_SCALE_SET_ID} ${E2E_GITHUB_URL} ${E2E_GITHUB_APP_CLIENT_ID} ${E2E_GITHUB_APP_INSTALLATION_ID} ${E2E_PROVISIONING_SECONDS} ${E2E_MAX_LIFETIME_SECONDS}' \
    < "tools/e2e/manifests/$1" > "$2"
}
