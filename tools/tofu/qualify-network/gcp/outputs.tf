# Named to drop straight into .github/workflows/qualify.yml's
# workflow_dispatch inputs (gcp_network/gcp_subnetwork) or
# tools/e2e/env-gcp.example (E2E_GCP_NETWORK/E2E_GCP_SUBNETWORK) - copy the
# value, not the output name, into whichever of those an operator is filling
# in.

output "network_name" {
  description = "Copy into qualify.yml's gcp_network workflow_dispatch input, or E2E_GCP_NETWORK."
  value       = google_compute_network.qualify.name
}

output "subnetwork_name" {
  description = "Copy into qualify.yml's gcp_subnetwork workflow_dispatch input, or E2E_GCP_SUBNETWORK."
  value       = google_compute_subnetwork.qualify.name
}
