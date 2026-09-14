# Named to drop straight into tools/e2e/env-gcp.example
# (E2E_GCP_NETWORK/E2E_GCP_SUBNETWORK) - copy the value, not the output
# name. qualify.yml provisions and destroys its own network in-job and
# never takes either of these as a workflow_dispatch input.

output "network_name" {
  description = "Copy into tools/e2e/env-gcp.example's E2E_GCP_NETWORK."
  value       = google_compute_network.qualify.name
}

output "subnetwork_name" {
  description = "Copy into tools/e2e/env-gcp.example's E2E_GCP_SUBNETWORK."
  value       = google_compute_subnetwork.qualify.name
}
