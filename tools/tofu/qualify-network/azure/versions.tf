# Pinned so `tofu init`/`terraform init` resolve a predictable provider and
# core version for this rarely-run, hand-invoked bootstrap module. OpenTofu
# and Terraform both understand this exact syntax (OpenTofu is a drop-in
# fork), so this file works unmodified under either binary.
terraform {
  required_version = ">= 1.6.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
  }
}
