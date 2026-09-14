output "resource_group_name" {
  description = "Name of the dedicated qualification resource group. Copy straight into qualify.yml's `azure_resource_group` workflow_dispatch input (and E2E_AZURE_RESOURCE_GROUP)."
  value       = azurerm_resource_group.qualify.name
}

output "subnet_id" {
  description = <<-EOT
    Full ARM resource ID of the qualification subnet
    (.../virtualNetworks/<vnet>/subnets/<subnet>). Copy straight into
    qualify.yml's `azure_subnet_id` workflow_dispatch input (and
    E2E_AZURE_SUBNET_ID) - this is the only network identifier qualify.yml
    needs beyond resource_group and nsg_id, since it derives the parent VNet
    resource ID from this subnet ID's own `/subnets/` path suffix.
  EOT
  value       = azurerm_subnet.qualify.id
}

output "nsg_id" {
  description = "Full ARM resource ID of the qualification network security group. Copy straight into qualify.yml's `azure_nsg_id` workflow_dispatch input (and E2E_AZURE_NSG_ID)."
  value       = azurerm_network_security_group.qualify.id
}

output "vnet_id" {
  description = "Full ARM resource ID of the qualification VNet. Not itself a qualify.yml input (it's derived there from subnet_id), but useful for E2E_AZURE_VNET_ID and for operator sanity-checking."
  value       = azurerm_virtual_network.qualify.id
}
