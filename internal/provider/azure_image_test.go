package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func azureSupportedImage() map[string]any {
	return map[string]any{"id": azureFixtureImage, "name": "test", "type": "Microsoft.Compute/images", "location": allocation().Offering.Region,
		"properties": map[string]any{"provisioningState": "Succeeded", "storageProfile": map[string]any{"osDisk": map[string]any{"osType": "Linux", "osState": "Generalized"}}}}
}

const (
	azureFixtureGalleryDefinition = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/galleries/gal/images/imgdef"
	azureFixtureGalleryVersion    = azureFixtureGalleryDefinition + "/versions/1.0.0"
)

func azureSupportedGalleryVersion() map[string]any {
	return map[string]any{"id": azureFixtureGalleryVersion, "name": "1.0.0", "type": "Microsoft.Compute/galleries/images/versions", "location": allocation().Offering.Region,
		"properties": map[string]any{"provisioningState": "Succeeded", "storageProfile": map[string]any{}}}
}

func azureSupportedGalleryDefinition() map[string]any {
	return map[string]any{"id": azureFixtureGalleryDefinition, "name": "imgdef", "type": "Microsoft.Compute/galleries/images",
		"properties": map[string]any{"osType": "Linux", "osState": "Generalized"}}
}

// TestAzureGalleryImageVersionValidation confirms validateAzureImage
// correctly handles a Compute Gallery image version resource ID
// (Microsoft.Compute/galleries/<gallery>/images/<definition>/versions/<version>)
// - the second image resource shape it accepts, alongside a classic
// managed image - including that osType/osState are read from the PARENT
// image definition, never the version resource itself.
func TestAzureGalleryImageVersionValidation(t *testing.T) {
	for _, mode := range []string{"valid", "data-disks", "windows", "specialized", "wrong-region", "unavailable-version", "unavailable-definition", "wrong-parent-type"} {
		t.Run(mode, func(t *testing.T) {
			version := azureSupportedGalleryVersion()
			definition := azureSupportedGalleryDefinition()
			a := allocation()
			a.Offering.Image = azureFixtureGalleryVersion
			switch mode {
			case "data-disks":
				version["properties"].(map[string]any)["storageProfile"].(map[string]any)["dataDiskImages"] = []any{map[string]any{"lun": 0}}
			case "windows":
				definition["properties"].(map[string]any)["osType"] = "Windows"
			case "specialized":
				definition["properties"].(map[string]any)["osState"] = "Specialized"
			case "wrong-region":
				version["location"] = "other-region"
			case "wrong-parent-type":
				definition["type"] = "Microsoft.Compute/disks"
			}
			versionCalls, definitionCalls := 0, 0
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected non-GET request", r.Method, r.URL.Path)
				}
				if strings.EqualFold(r.URL.Path, azureFixtureGalleryVersion) {
					versionCalls++
					if mode == "unavailable-version" {
						w.WriteHeader(403)
						writeJSON(w, map[string]any{"error": map[string]string{"code": "AuthorizationFailed"}})
						return
					}
					writeJSON(w, version)
					return
				}
				if strings.EqualFold(r.URL.Path, azureFixtureGalleryDefinition) {
					definitionCalls++
					if mode == "unavailable-definition" {
						w.WriteHeader(403)
						writeJSON(w, map[string]any{"error": map[string]string{"code": "AuthorizationFailed"}})
						return
					}
					writeJSON(w, definition)
					return
				}
				t.Error("unexpected request path", r.URL.Path)
			})
			err := p.validateAzureImage(context.Background(), a)
			valid := mode == "valid"
			if (err == nil) != valid {
				t.Fatal("gallery image version verdict differs", mode, err)
			}
			if versionCalls != 1 {
				t.Fatal("expected exactly one version GET", versionCalls)
			}
			wantDefinitionCalls := 1
			if mode == "wrong-region" || mode == "unavailable-version" || mode == "data-disks" {
				wantDefinitionCalls = 0
			}
			if definitionCalls != wantDefinitionCalls {
				t.Fatal("unexpected definition GET count", definitionCalls, mode)
			}
		})
	}
}

func TestAzureManagedImageContractPrecedesDeployment(t *testing.T) {
	for _, mode := range []string{"data-disks", "windows", "specialized", "wrong-region", "missing-properties", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			a := allocation()
			a.Offering.Image = azureFixtureImage
			osDisk := map[string]any{"osType": "Linux", "osState": "Generalized"}
			storage := map[string]any{"osDisk": osDisk}
			properties := map[string]any{"provisioningState": "Succeeded", "storageProfile": storage}
			image := map[string]any{"id": azureFixtureImage, "name": "test", "type": "Microsoft.Compute/images", "location": a.Offering.Region, "properties": properties}
			switch mode {
			case "data-disks":
				storage["dataDisks"] = []any{map[string]any{"lun": 0, "diskSizeGB": 32}}
			case "windows":
				osDisk["osType"] = "Windows"
			case "specialized":
				osDisk["osState"] = "Specialized"
			case "wrong-region":
				image["location"] = "other-region"
			case "missing-properties":
				delete(image, "properties")
			}
			deployments := 0
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.EqualFold(r.URL.Path, azureFixtureImage) {
					if mode == "unavailable" {
						w.WriteHeader(403)
						writeJSON(w, map[string]any{"error": map[string]string{"code": "AuthorizationFailed"}})
					} else {
						writeJSON(w, image)
					}
					return
				}
				if deploymentPath(r) && r.Method == "PUT" {
					deployments++
					writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
					return
				}
				w.WriteHeader(404)
				writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
			})
			_, err := p.CreateWithResources(context.Background(), a)
			if err == nil || deployments != 0 {
				t.Fatalf("unsupported/unconfirmed image reached deployment: deployments=%d error=%v", deployments, err)
			}
		})
	}
}

func TestAzureImageMetadataRequiresMatchingIdentityAndCompleteShape(t *testing.T) {
	for _, mode := range []string{"valid", "case", "wrong-id", "wrong-type", "wrong-name", "missing-location", "active", "missing-os", "malformed-disks", "malformed-properties", "wrong-reference", "missing-region"} {
		t.Run(mode, func(t *testing.T) {
			image := azureSupportedImage()
			properties := image["properties"].(map[string]any)
			storage := properties["storageProfile"].(map[string]any)
			a := allocation()
			a.Offering.Image = azureFixtureImage
			switch mode {
			case "case":
				image["id"] = strings.ToUpper(azureFixtureImage)
				image["location"] = strings.ToUpper(a.Offering.Region)
			case "wrong-id":
				image["id"] = azureFixtureImage + "-other"
			case "wrong-type":
				image["type"] = "Microsoft.Compute/disks"
			case "wrong-name":
				image["name"] = "other"
			case "missing-location":
				delete(image, "location")
			case "active":
				properties["provisioningState"] = "Creating"
			case "missing-os":
				delete(storage, "osDisk")
			case "malformed-disks":
				storage["dataDisks"] = map[string]any{"lun": 0}
			case "malformed-properties":
				image["properties"] = []any{}
			case "wrong-reference":
				a.Offering.Image = strings.Replace(azureFixtureImage, "/images/", "/disks/", 1)
			case "missing-region":
				a.Offering.Region = ""
			}
			calls := 0
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || !strings.EqualFold(r.URL.Path, azureFixtureImage) || r.URL.Query().Get("api-version") != "2026-04-01" {
					t.Error("unexpected image metadata request", r.Method, r.URL.Path)
				}
				writeJSON(w, image)
			})
			err := p.validateAzureImage(context.Background(), a)
			valid := mode == "valid" || mode == "case"
			if (err == nil) != valid {
				t.Fatal("image metadata verdict differs", mode, err)
			}
			wantCalls := 1
			if mode == "wrong-reference" || mode == "missing-region" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatal("invalid reference was sent to ARM", calls)
			}
		})
	}
}
