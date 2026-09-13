package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAzureCreationRefusesOccupiedOrUnknownResourceNames(t *testing.T) {
	for _, mode := range []string{"vacant", "foreign-vm", "foreign-nic", "foreign-disk", "owned-vm", "untagged-disk", "unavailable", "invalid-404", "empty-404", "existing-deployment"} {
		t.Run(mode, func(t *testing.T) {
			vm, disk := azureCreationVM(), azureCreationDisk()
			nic := azureOwnedNIC()
			target := "virtualmachines/rs-test"
			switch mode {
			case "foreign-vm":
				vm["tags"] = map[string]string{"runnerscout-owner": "foreign"}
			case "foreign-nic":
				target = "networkinterfaces/rs-test-nic"
				nic["tags"] = map[string]string{"runnerscout-owner": "foreign"}
			case "foreign-disk":
				target = "disks/rs-test-os"
				disk["tags"] = map[string]string{"runnerscout-owner": "foreign"}
			case "untagged-disk":
				target = "disks/rs-test-os"
			}
			mutations := 0
			deployed := false
			vacancies := map[string]bool{}
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				path := strings.ToLower(r.URL.Path)
				if strings.EqualFold(r.URL.Path, azureFixtureImage) && r.Method == "GET" {
					writeJSON(w, azureSupportedImage())
					return
				}
				if deployed && r.Method == "GET" && strings.HasSuffix(path, "/resources") {
					writeJSON(w, map[string]any{"value": []any{vm, disk, nic}})
					return
				}
				if r.Method != "GET" {
					mutations++
				}
				if deploymentPath(r) && r.Method == "PUT" {
					if mode == "vacant" && len(vacancies) != 4 {
						t.Error("deployment submitted before every name was observed absent", vacancies)
					}
					// ARM create-or-update applies template tags to existing VM/NIC
					// names. A later ownership read cannot undo that adoption.
					deployed = true
					vm["tags"] = map[string]string{"runnerscout-owner": "test", "runnerscout-operation": "rs-test"}
					nic["tags"] = vm["tags"]
					writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
					return
				}
				if !deployed && mode == "existing-deployment" && deploymentPath(r) {
					writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Running"}})
					return
				}
				if !deployed && (mode == "invalid-404" || mode == "empty-404") {
					w.WriteHeader(404)
					if mode == "invalid-404" {
						writeJSON(w, map[string]any{"error": map[string]string{"code": "AuthorizationFailed"}})
					}
					return
				}
				if !deployed && mode == "unavailable" {
					w.WriteHeader(503)
					writeJSON(w, map[string]any{"error": map[string]string{"code": "ServiceUnavailable"}})
					return
				}
				resources := map[string]any{"virtualmachines/rs-test": vm, "networkinterfaces/rs-test-nic": nic, "disks/rs-test-os": disk}
				for suffix, resource := range resources {
					if strings.HasSuffix(path, "/"+suffix) && (deployed || mode != "vacant" && suffix == target) {
						if r.Method == "PATCH" && suffix == "disks/rs-test-os" {
							var body struct {
								Tags map[string]string `json:"tags"`
							}
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							disk["tags"] = body.Tags
						}
						writeJSON(w, resource)
						return
					}
				}
				vacancies[path] = true
				w.WriteHeader(404)
				writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
			})
			a := allocation()
			a.Offering.Image = azureFixtureImage
			_, err := p.CreateWithResources(context.Background(), a)
			if mode == "vacant" {
				if err != nil || mutations != 2 {
					t.Fatalf("vacant names did not create and tag once: writes=%d error=%v", mutations, err)
				}
				return
			}
			if err == nil || mutations != 0 {
				t.Fatalf("occupied/unknown creation reached ARM mutation: writes=%d error=%v", mutations, err)
			}
		})
	}
}
