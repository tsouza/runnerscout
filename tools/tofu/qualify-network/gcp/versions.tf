# Pinned per this repo's own philosophy (see main.tf's header comment):
# nothing about this module's provider or engine version should silently
# drift between runs of a module that is applied rarely and by hand.
terraform {
  required_version = ">= 1.7.0" # OpenTofu (this module's intended runner) or Terraform, either works - no engine-specific syntax is used anywhere in this module.

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }
}
