output "resource_group_name" {
  description = "Name of the dedicated qualification resource group. Copy straight into tools/e2e/env.example's E2E_AZURE_RESOURCE_GROUP."
  value       = azurerm_resource_group.qualify.name
}

output "subnet_id" {
  description = <<-EOT
    Full ARM resource ID of the qualification subnet
    (.../virtualNetworks/<vnet>/subnets/<subnet>). Copy straight into
    tools/e2e/env.example's E2E_AZURE_SUBNET_ID. qualify.yml provisions and
    destroys its own network in-job and never takes this as an input.
  EOT
  value       = azurerm_subnet.qualify.id
}

output "nsg_id" {
  description = "Full ARM resource ID of the qualification network security group. Copy straight into tools/e2e/env.example's E2E_AZURE_NSG_ID."
  value       = azurerm_network_security_group.qualify.id
}

output "vnet_id" {
  description = "Full ARM resource ID of the qualification VNet. Useful for E2E_AZURE_VNET_ID and for operator sanity-checking."
  value       = azurerm_virtual_network.qualify.id
}
