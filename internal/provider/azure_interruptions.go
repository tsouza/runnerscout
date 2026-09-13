package provider

import (
	"github.com/tsouza/runnerscout/internal/azurequeue"
)

// InterruptionQueue returns an internal/azurequeue.Client authenticated with
// this AzureSDK's own already-resolved credential chain (see credentials()),
// reused rather than a separately-scoped credential.
//
// This is a deliberate choice, not an oversight: Azure RBAC role
// assignments on the target Storage Queue resource - not which SDK object
// or azidentity chain requested the token - are what actually restrict a
// caller to "read/process/delete messages on this one queue" (see
// docs/azure-interruption-delivery.md's provisioning section, "read/
// process/delete rights on that queue for the controller's identity"). The
// managed identity or workload identity credentials() already resolves for
// ARM VM provisioning is perfectly capable of requesting a Storage Queue
// data-plane token instead (azqueue's own client requests the
// storage.azure.com audience itself; azcore.TokenCredential is
// audience-agnostic), and every azidentity credential type this codebase
// selects (see azureCredential in credentials.go) is designed to be shared
// across differently-scoped ARM/data-plane clients this way. Resolving a
// second, distinct credential here would only duplicate credentials()'s own
// environment-probing and failure-mode handling for no isolation benefit -
// exactly the kind of speculative machinery this codebase's CQ-08 aversion
// (see docs/azure-interruption-delivery.md's "Rejected alternatives")
// already argues against elsewhere in this same delivery path.
//
// queueURL is the full queue endpoint, e.g.
// "https://<account>.queue.core.windows.net/<queue>" - see
// internal/azurequeue.New's own doc comment.
func (a *AzureSDK) InterruptionQueue(queueURL string) (*azurequeue.Client, error) {
	credential, err := a.credentials()
	if err != nil {
		return nil, err
	}
	return azurequeue.New(queueURL, credential, nil)
}
