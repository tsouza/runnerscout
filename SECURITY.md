# Security

Do not run production workloads on this experimental project. Report vulnerabilities
through GitHub private vulnerability reporting for this repository, when available.
Never put credentials or exploitable private details in a public issue.

Controller credentials stay in Secrets/workload identity; VM jobs receive only their
ephemeral runner configuration. Images must be pinned, maintained and contain a
non-root runner account. Jobs have no controller service account, cloud provisioning
role or shared disk. Treat all workflow code as hostile to its disposable VM.
Provisioning networks deny inbound traffic, block controller/private management
networks and cloud credential metadata, and allow the documented GitHub outbound
endpoints. Operators own cloud network policy; Kubernetes NetworkPolicy does not
isolate standalone VMs. Privileged job capabilities require explicit image support.

Do not expose JIT configuration, API tokens or cloud responses in logs/evidence.
The editable repository and CI are not a separate protected evaluator authority.
