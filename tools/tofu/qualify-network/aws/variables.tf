variable "region" {
  description = "AWS region to create the qualification network in (e.g. \"us-east-1\"). Must be explicit - no default - since this network's region has to match the qualify.yml `aws_region` / tools/e2e/env.example's `E2E_AWS_REGION` value an operator later pins against it."
  type        = string

  validation {
    condition     = length(trimspace(var.region)) > 0
    error_message = "region must be a non-empty AWS region name."
  }
}

# No account_id variable: unlike a cross-account role ARN, nothing this
# module creates needs the AWS account id spelled out anywhere in HCL - the
# aws provider resolves the calling identity's account from whatever
# ambient credentials (OIDC role assumption in CI, or a local profile) it
# is run under, the same way the sibling Azure module's `subscription_id`
# is left to ambient resolution by default.

variable "name_prefix" {
  description = "Prefix used to name every resource this module creates (e.g. \"<name_prefix>-vpc\") and defaults to a name that self-identifies as qualification infrastructure - never point this at production naming."
  type        = string
  default     = "runnerscout-qualify"
}

variable "vpc_cidr" {
  description = "CIDR block for the qualification VPC. Small and isolated - this network exists only to launch qualification/E2E Spot instances, never to route to or peer with production networks."
  type        = string
  default     = "10.90.0.0/16"
}

variable "public_subnet_cidr" {
  description = "CIDR block for the public subnet that hosts only the NAT Gateway (see main.tf). No qualification instance is ever launched into this subnet."
  type        = string
  default     = "10.90.0.0/24"
}

variable "private_subnet_cidr" {
  description = "CIDR block for the private subnet qualify.yml / tools/e2e actually launch the qualification instance into (this module's `subnet_id` output)."
  type        = string
  default     = "10.90.1.0/24"
}

variable "tags" {
  description = "Extra tags merged onto every resource this module creates, in addition to the fixed `runnerscout-purpose = \"qualification-network\"` tag every resource always gets."
  type        = map(string)
  default     = {}
}
