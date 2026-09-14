# Minimal, isolated Azure network for this repo's real-cloud Azure
# qualification/E2E lanes (.github/workflows/qualify.yml's `provider: azure`
# matrix branch, and tools/e2e/bring-up-azure.sh). Both already treat the
# resource group / subnet / NSG as pre-existing, pinned, operator-supplied
# input - this module is what actually provisions that input, once.
#
# What runs here, and why it needs no inbound access:
#
#   - qualify.yml's Azure branch drives internal/provider/azure_realcloud_test.go,
#     which creates one real Spot VM through the production adapter, confirms
#     it reaches PowerState/running, observes its Spot price, and deletes it.
#     Its own header comment is explicit that it feeds a synthetic,
#     non-functional placeholder in place of a real GitHub JIT token, so the
#     cloud-init runner-registration step is *expected* to fail harmlessly
#     inside the guest - this lane never depends on real inbound OR outbound
#     connectivity succeeding; it only qualifies the ARM control-plane
#     lifecycle (create/observe/price/delete).
#   - tools/e2e/bring-up-azure.sh's harness goes further and expects a real
#     self-hosted GitHub Actions runner to register and pick up a job. A
#     self-hosted runner is pull-only by design: it makes outbound HTTPS
#     (443) connections to GitHub to register itself and long-poll for work,
#     and never accepts any inbound connection from GitHub or anywhere else.
#     Nothing in internal/provider/azure.go's createAzure (NIC/VM template)
#     or azure_inventory.go opens, expects, or checks any inbound port
#     either - the NIC gets only a dynamic private IP, no public IP is ever
#     allocated by this codebase's Azure adapter.
#
# So this network needs zero inbound access and only outbound connectivity
# (443 to GitHub, plus whatever the platform itself needs - DNS, NTP, IMDS -
# which Azure's own default outbound rule already carries). See the NSG
# resource below for exactly what is (and is not) enforced.

provider "azurerm" {
  subscription_id = var.subscription_id

  features {}
}

resource "azurerm_resource_group" "qualify" {
  name     = var.resource_group_name
  location = var.location

  tags = merge(var.tags, {
    purpose = "qualification-network"
  })
}

resource "azurerm_virtual_network" "qualify" {
  name                = "runnerscout-qualify-vnet"
  resource_group_name = azurerm_resource_group.qualify.name
  location            = azurerm_resource_group.qualify.location
  address_space       = var.vnet_address_space

  tags = merge(var.tags, {
    purpose = "qualification-network"
  })
}

resource "azurerm_subnet" "qualify" {
  name                 = "runnerscout-qualify-subnet"
  resource_group_name  = azurerm_resource_group.qualify.name
  virtual_network_name = azurerm_virtual_network.qualify.name
  address_prefixes     = var.subnet_address_prefixes
}

# Azure NSGs already default-deny all inbound traffic that isn't from within
# the VNet or an Azure load balancer (the built-in, unlisted
# AllowVnetInBound/AllowAzureLoadBalancerInBound/DenyAllInBound rules every
# NSG carries). Nothing this module's subnet ever hosts talks to another VM
# in this VNet or sits behind a load balancer, so that built-in default
# already amounts to "deny everything" here in practice. This module adds an
# explicit, visible DenyAllInbound rule anyway (lowest possible priority, so
# it never masks a future legitimate rule added above it) rather than relying
# solely on the implicit platform default - an operator reading this NSG's
# rule list should see the "no inbound, ever" intent stated outright instead
# of having to already know Azure's own default-rule behavior.
#
# No custom outbound rule is added: the default AllowInternetOutBound rule
# (also built-in) is left as-is. Restricting it to 443-only was considered
# and rejected here - the qualification/E2E VM also needs outbound access for
# things this module has no authoritative list of today (OS package
# repositories, time sync, DNS), and narrowing it wrongly would turn a
# network-provisioning bug into a mysterious runner-registration failure.
# Egress is bounded by this being an isolated, single-purpose VNet with no
# peering and no route to any other network, not by port-level filtering.
resource "azurerm_network_security_group" "qualify" {
  name                = "runnerscout-qualify-nsg"
  resource_group_name = azurerm_resource_group.qualify.name
  location            = azurerm_resource_group.qualify.location

  security_rule {
    name                       = "DenyAllInbound"
    priority                   = 4096
    direction                  = "Inbound"
    access                     = "Deny"
    protocol                   = "*"
    source_port_range          = "*"
    destination_port_range     = "*"
    source_address_prefix      = "*"
    destination_address_prefix = "*"
  }

  tags = merge(var.tags, {
    purpose = "qualification-network"
  })
}

resource "azurerm_subnet_network_security_group_association" "qualify" {
  subnet_id                 = azurerm_subnet.qualify.id
  network_security_group_id = azurerm_network_security_group.qualify.id
}
