variable "location" {
  description = "Azure region to create the qualification network in (e.g. \"eastus\"). Must be explicit - no default - since this network's region has to match the qualify.yml `azure_region` / E2E_AZURE_REGION value an operator later pins against it."
  type        = string

  validation {
    condition     = length(trimspace(var.location)) > 0
    error_message = "location must be a non-empty Azure region name."
  }
}

variable "subscription_id" {
  description = <<-EOT
    Azure subscription GUID to create resources in. Optional: the azurerm
    provider resolves a subscription from its own ambient auth context
    (ARM_SUBSCRIPTION_ID, an `az login` session, or - in CI - the Workload
    Identity Federation OIDC credential this module is meant to be run
    under) when this is left null. Set it explicitly only if that ambient
    context is ambiguous (e.g. the running identity has access to more than
    one subscription).
  EOT
  type        = string
  default     = null
}

variable "resource_group_name" {
  description = "Name of the dedicated resource group this module creates. Defaults to a name that self-identifies as qualification infrastructure - never point this at a production resource group name."
  type        = string
  default     = "runnerscout-qualify-network"
}

variable "vnet_address_space" {
  description = "Address space for the qualification VNet. Small and isolated - this network exists only to launch qualification/E2E Spot VMs, never to route to or peer with production networks."
  type        = list(string)
  default     = ["10.91.0.0/16"]
}

variable "subnet_address_prefixes" {
  description = "Address prefixes for the single subnet carved out of vnet_address_space."
  type        = list(string)
  default     = ["10.91.1.0/24"]
}

variable "tags" {
  description = "Extra tags merged onto every resource this module creates, in addition to the fixed `purpose = \"qualification-network\"` tag every resource always gets."
  type        = map(string)
  default     = {}
}
