package publisher

import "fmt"

// Field names follow current App Asset Library reference data, not the deprecated
// screenshot display-type enumeration. Unknown additions are ignored; every
// requested relationship must still be positively and unambiguously present.
type appleAssetCatalog struct {
	Features []struct {
		FeatureID         string `json:"featureId"`
		PlacementPolicies []struct {
			PlacementType string `json:"placementType"`
			GroupLimits   []struct {
				GroupIDs []string `json:"groupIds"`
				MaxCount int      `json:"maxCount"`
			} `json:"groupLimits"`
		} `json:"placementPolicies"`
	} `json:"features"`
	PlacementProfileGroups []struct {
		ID       string `json:"placementProfileGroupId"`
		Platform string `json:"platform"`
	} `json:"placementProfileGroups"`
	PlacementTypes []struct {
		ID           string   `json:"placementTypeId"`
		Categories   []string `json:"acceptsAssetCategories"`
		SpecMappings []struct {
			GroupID string   `json:"placementGroupId"`
			Specs   []string `json:"specs"`
		} `json:"specMappings"`
	} `json:"placementTypes"`
	ImageSpecs []struct {
		SpecID     string `json:"specId"`
		Dimensions struct {
			MinWidth  int `json:"minWidth"`
			MaxWidth  int `json:"maxWidth"`
			MinHeight int `json:"minHeight"`
			MaxHeight int `json:"maxHeight"`
		} `json:"dimensions"`
		CompatibleTypes []string `json:"compatiblePlacementTypes"`
		AlphaAllowed    bool     `json:"alphaAllowed"`
		Extensions      []string `json:"fileExtensions"`
		MIMEs           []string `json:"mimeTypes"`
		MaxFileSize     int64    `json:"maxFileSize"`
	} `json:"imageSpecs"`
}

func validateAppleCatalog(c appleAssetCatalog, s *appleMaterialSnapshot) error {
	// Count identities before filtering compatible entries. Otherwise a second
	// conflicting definition could disappear from the compatibility checks.
	features, profiles, kinds, specs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	unique := func(seen map[string]bool, id string) bool {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
		return true
	}
	for _, feature := range c.Features {
		if !unique(features, feature.FeatureID) {
			return fmt.Errorf("duplicate or missing App Store catalog feature identity")
		}
	}
	for _, profile := range c.PlacementProfileGroups {
		if !unique(profiles, profile.ID) {
			return fmt.Errorf("duplicate or missing App Store catalog profile identity")
		}
	}
	for _, kind := range c.PlacementTypes {
		if !unique(kinds, kind.ID) {
			return fmt.Errorf("duplicate or missing App Store catalog placement-type identity")
		}
	}
	for _, spec := range c.ImageSpecs {
		if !unique(specs, spec.SpecID) {
			return fmt.Errorf("duplicate or missing App Store catalog image-spec identity")
		}
	}
	for _, locale := range s.Locales {
		for _, group := range locale.Screenshots {
			platformCount, policyCount, mappingCount := 0, 0, 0
			var allowedSpecs []string
			for _, profile := range c.PlacementProfileGroups {
				if profile.ID == group.PlacementGroup && containsApple([]string{"IPHONE_APP_STORE", "IPAD_APP_STORE"}, profile.Platform) {
					platformCount++
				}
			}
			for _, feature := range c.Features {
				if feature.FeatureID != "APP_STORE_VERSIONS" {
					continue
				}
				for _, policy := range feature.PlacementPolicies {
					if policy.PlacementType != "APP_SCREENSHOT" {
						continue
					}
					for _, limit := range policy.GroupLimits {
						if containsApple(limit.GroupIDs, group.PlacementGroup) {
							policyCount++
							if limit.MaxCount < 1 || len(group.Images) > limit.MaxCount {
								return fmt.Errorf("App Store runtime screenshot count limit exceeded")
							}
						}
					}
				}
			}
			for _, kind := range c.PlacementTypes {
				if kind.ID != "APP_SCREENSHOT" || !containsApple(kind.Categories, "APP_SCREENSHOTS_AND_PREVIEWS") {
					continue
				}
				for _, mapping := range kind.SpecMappings {
					if mapping.GroupID == group.PlacementGroup {
						mappingCount++
						allowedSpecs = mapping.Specs
					}
				}
			}
			if platformCount != 1 || policyCount != 1 || mappingCount != 1 {
				return fmt.Errorf("App Store runtime catalog does not unambiguously allow this IOS screenshot group")
			}
			for _, asset := range group.Images {
				if !containsApple(allowedSpecs, asset.SpecID) {
					return fmt.Errorf("App Store screenshot specification is not allowed in the requested group")
				}
				found := 0
				for _, spec := range c.ImageSpecs {
					if spec.SpecID != asset.SpecID {
						continue
					}
					found++
					d := spec.Dimensions
					ext, mime := ".png", "image/png"
					if asset.Format == "jpeg" {
						ext, mime = ".jpg", "image/jpeg"
					}
					if d.MinWidth != asset.Width || d.MaxWidth != asset.Width || d.MinHeight != asset.Height || d.MaxHeight != asset.Height || !containsApple(spec.CompatibleTypes, "APP_SCREENSHOT") || !containsApple(spec.Extensions, ext) || !containsApple(spec.MIMEs, mime) || asset.Bytes > spec.MaxFileSize || spec.MaxFileSize < 1 || (asset.Alpha && !spec.AlphaAllowed) {
						return fmt.Errorf("App Store screenshot bytes, dimensions, alpha or format violate the live specification")
					}
				}
				if found != 1 {
					return fmt.Errorf("App Store screenshot specification is missing or ambiguous")
				}
			}
		}
	}
	return nil
}
