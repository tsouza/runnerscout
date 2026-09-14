# No defaults on either variable, deliberately: this module creates
# long-lived infrastructure from a one-time, by-hand `tofu apply` (see
# README.md), so an operator must always type the exact project and region
# they intend to provision into rather than silently inheriting a default
# that could point at the wrong project.

variable "project_id" {
  description = "GCP project to create the qualification network in. Must be the same project already provisioned as this repository's GCP_QUALIFICATION_PROJECT_ID (see docs/qualification-real-cloud.md's GCP section) - this module does not create or validate that project."
  type        = string
}

variable "region" {
  description = "Region the qualification subnetwork is created in (e.g. us-central1). Must match the gcp_region this repository's qualify.yml/E2E_GCP_REGION will be pinned to when using the network/subnetwork this module outputs."
  type        = string
}
