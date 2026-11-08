package publisher

import (
	"encoding/json"
	"os"
	"testing"
)

func appleCatalogFixtureJSON() map[string]any {
	return map[string]any{
		"features":               []any{map[string]any{"featureId": "APP_STORE_VERSIONS", "placementPolicies": []any{map[string]any{"placementType": "APP_SCREENSHOT", "groupLimits": []any{map[string]any{"groupIds": []string{"IPHONE_TEST_PROFILE"}, "maxCount": 10}}}}}},
		"placementProfileGroups": []any{map[string]any{"placementProfileGroupId": "IPHONE_TEST_PROFILE", "platform": "IPHONE_APP_STORE"}},
		"placementTypes":         []any{map[string]any{"placementTypeId": "APP_SCREENSHOT", "acceptsAssetCategories": []string{"APP_SCREENSHOTS_AND_PREVIEWS"}, "specMappings": []any{map[string]any{"placementGroupId": "IPHONE_TEST_PROFILE", "specs": []string{"test-spec"}}}}},
		"imageSpecs":             []any{map[string]any{"specId": "test-spec", "dimensions": map[string]any{"minWidth": 100, "maxWidth": 100, "minHeight": 200, "maxHeight": 200}, "compatiblePlacementTypes": []string{"APP_SCREENSHOT"}, "alphaAllowed": false, "fileExtensions": []string{".png"}, "mimeTypes": []string{"image/png"}, "maxFileSize": 1048576}},
	}
}

func TestAppleCatalogUsesRuntimePolicyAndImageSpecifications(t *testing.T) {
	for _, scenario := range []string{"valid", "unavailable-feature", "wrong-platform", "wrong-category", "wrong-group", "wrong-spec", "dimensions", "mime", "extension", "max-bytes", "alpha", "count", "ambiguous-spec", "ambiguous-policy", "conflicting-feature", "conflicting-profile", "conflicting-type"} {
		t.Run(scenario, func(t *testing.T) {
			manifest, p := appleMaterialJSONFixture(t)
			writeAppleMaterialFixture(t, &p, manifest)
			material, err := stageAppleMaterial(p, Request{Version: "1.0.0", MetadataDigest: p.MetadataDigest})
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(material.Dir)
			catalog := appleCatalogFixtureJSON()
			spec := catalog["imageSpecs"].([]any)[0].(map[string]any)
			feature := catalog["features"].([]any)[0].(map[string]any)
			policy := feature["placementPolicies"].([]any)[0].(map[string]any)
			kind := catalog["placementTypes"].([]any)[0].(map[string]any)
			switch scenario {
			case "unavailable-feature":
				feature["featureId"] = "CUSTOM_PRODUCT_PAGES"
			case "wrong-platform":
				catalog["placementProfileGroups"].([]any)[0].(map[string]any)["platform"] = "MAC_APP_STORE"
			case "wrong-category":
				kind["acceptsAssetCategories"] = []string{"CREATIVE_ASSETS"}
			case "wrong-group":
				kind["specMappings"].([]any)[0].(map[string]any)["placementGroupId"] = "IPAD_OTHER"
			case "wrong-spec":
				kind["specMappings"].([]any)[0].(map[string]any)["specs"] = []string{"other-spec"}
			case "dimensions":
				spec["dimensions"].(map[string]any)["minWidth"] = 101
			case "mime":
				spec["mimeTypes"] = []string{"image/jpeg"}
			case "extension":
				spec["fileExtensions"] = []string{".jpg"}
			case "max-bytes":
				spec["maxFileSize"] = 1
			case "alpha":
				material.Snapshot.Locales[0].Screenshots[0].Images[0].Alpha = true
			case "count":
				policy["groupLimits"].([]any)[0].(map[string]any)["maxCount"] = 0
			case "ambiguous-spec":
				catalog["imageSpecs"] = []any{spec, spec}
			case "ambiguous-policy":
				feature["placementPolicies"] = []any{policy, policy}
			case "conflicting-feature":
				catalog["features"] = append(catalog["features"].([]any), map[string]any{"featureId": "APP_STORE_VERSIONS", "placementPolicies": []any{}})
			case "conflicting-profile":
				catalog["placementProfileGroups"] = append(catalog["placementProfileGroups"].([]any), map[string]any{"placementProfileGroupId": "IPHONE_TEST_PROFILE", "platform": "MAC_APP_STORE"})
			case "conflicting-type":
				catalog["placementTypes"] = append(catalog["placementTypes"].([]any), map[string]any{"placementTypeId": "APP_SCREENSHOT", "acceptsAssetCategories": []string{"CREATIVE_ASSETS"}, "specMappings": []any{}})
			}
			body, _ := json.Marshal(catalog)
			var data appleAssetCatalog
			if err = json.Unmarshal(body, &data); err != nil {
				t.Fatal(err)
			}
			err = validateAppleCatalog(data, material.Snapshot)
			if scenario == "valid" && err != nil {
				t.Fatal(err)
			}
			if scenario != "valid" && err == nil {
				t.Fatal("runtime catalog incompatibility accepted")
			}
		})
	}
}
