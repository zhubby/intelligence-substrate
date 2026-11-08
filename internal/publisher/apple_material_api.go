package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

func appleStringState(resource appleResource) string {
	var state string
	_ = json.Unmarshal(resource.Attributes.State, &state)
	return state
}

func (e *Engine) appleMaterialCatalog(ctx context.Context, p Profile, snapshot *appleMaterialSnapshot) error {
	var response struct {
		Data []struct {
			Type       string            `json:"type"`
			ID         string            `json:"id"`
			Attributes appleAssetCatalog `json:"attributes"`
		} `json:"data"`
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	q := url.Values{"filter[features]": {"APP_STORE_VERSIONS"}}
	if err := e.appleCall(ctx, p, "GET", "/v1/appAssetLibraryRefData?"+q.Encode(), nil, &response); err != nil {
		return err
	}
	if len(response.Data) != 1 || response.Links.Next != "" || response.Data[0].Type != "appAssetLibraryRefData" || !appleResourceID.MatchString(response.Data[0].ID) {
		return fmt.Errorf("one complete App Store asset catalog required")
	}
	return validateAppleCatalog(response.Data[0].Attributes, snapshot)
}

func (e *Engine) appleMaterialParentLocales(ctx context.Context, p Profile, receipt *appleReceipt, discover bool, review ...bool) (map[string]appleResource, map[string]appleResource, error) {
	snapshot := receipt.Material
	var infos appleList
	if err := e.appleCall(ctx, p, "GET", "/v1/apps/"+p.AppID+"/appInfos?limit=200", nil, &infos); err != nil {
		return nil, nil, err
	}
	if infos.Links.Next != "" {
		return nil, nil, fmt.Errorf("complete App Store app-info collection required")
	}
	selected := ""
	seen := map[string]bool{}
	for _, info := range infos.Data {
		if !info.is("appInfos", "") || seen[info.ID] {
			return nil, nil, fmt.Errorf("invalid or duplicate App Store app info")
		}
		seen[info.ID] = true
		if discover {
			if appleStringState(info) == "PREPARE_FOR_SUBMISSION" {
				if selected != "" {
					return nil, nil, fmt.Errorf("ambiguous editable App Store app info")
				}
				selected = info.ID
			}
		} else if info.ID == snapshot.AppInfoID {
			states := []string{"PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW"}
			if len(review) > 0 && review[0] {
				states = append(states, "WAITING_FOR_REVIEW", "IN_REVIEW", "ACCEPTED", "PENDING_RELEASE", "READY_FOR_DISTRIBUTION", "REJECTED", "DEVELOPER_REJECTED")
			}
			if !containsApple(states, appleStringState(info)) {
				return nil, nil, fmt.Errorf("App Store app-info state is not recognized for this operation")
			}
			selected = info.ID
		}
	}
	if selected == "" {
		return nil, nil, fmt.Errorf("exactly one prepared App Store app-info resource required")
	}
	if discover {
		snapshot.AppInfoID = selected
	}
	read := func(path, kind string) (map[string]appleResource, error) {
		var response appleList
		if err := e.appleCall(ctx, p, "GET", path+"?limit=200", nil, &response); err != nil {
			return nil, err
		}
		if response.Links.Next != "" || len(response.Data) != len(snapshot.Locales) {
			return nil, fmt.Errorf("App Store complete locale set differs from approved material")
		}
		out := map[string]appleResource{}
		ids := map[string]bool{}
		for _, resource := range response.Data {
			if !resource.is(kind, "") || out[resource.Attributes.Locale].ID != "" || ids[resource.ID] {
				return nil, fmt.Errorf("invalid or duplicate App Store locale identity")
			}
			out[resource.Attributes.Locale] = resource
			ids[resource.ID] = true
		}
		return out, nil
	}
	versions, err := read("/v1/appStoreVersions/"+receipt.VersionID+"/appStoreVersionLocalizations", "appStoreVersionLocalizations")
	if err != nil {
		return nil, nil, err
	}
	localizations, err := read("/v1/appInfos/"+selected+"/appInfoLocalizations", "appInfoLocalizations")
	if err != nil {
		return nil, nil, err
	}
	for i := range snapshot.Locales {
		locale := &snapshot.Locales[i]
		v, a := versions[locale.Locale], localizations[locale.Locale]
		if v.ID == "" || a.ID == "" {
			return nil, nil, fmt.Errorf("App Store material locale was not prepared")
		}
		if discover {
			locale.VersionLocalizationID = v.ID
			locale.InfoLocalizationID = a.ID
		} else if locale.VersionLocalizationID != v.ID || locale.InfoLocalizationID != a.ID {
			return nil, nil, fmt.Errorf("App Store approved localization identity changed")
		}
	}
	return versions, localizations, nil
}

func (e *Engine) appleMaterialMedia(ctx context.Context, p Profile, locale *appleLocaleSnapshot, placed bool, review ...bool) error {
	for _, kind := range []string{"appScreenshotSets", "appPreviewSets"} {
		var legacy appleList
		if err := e.appleCall(ctx, p, "GET", "/v1/appStoreVersionLocalizations/"+locale.VersionLocalizationID+"/"+kind+"?limit=200", nil, &legacy); err != nil {
			return err
		}
		if legacy.Links.Next != "" || len(legacy.Data) != 0 {
			return fmt.Errorf("legacy App Store media requires explicit operator migration")
		}
	}
	var response appleList
	query := url.Values{"sort": {"placementGroupPosition"}, "include": {"image"}, "limit": {"200"}}
	if err := e.appleCall(ctx, p, "GET", "/v1/appStoreVersionLocalizations/"+locale.VersionLocalizationID+"/placements?"+query.Encode(), nil, &response); err != nil {
		return err
	}
	if response.Links.Next != "" {
		return fmt.Errorf("complete App Store placements required")
	}
	if !placed {
		if len(response.Data) != 0 {
			return fmt.Errorf("prepared App Store version already contains unapproved placements")
		}
		return nil
	}
	expected := map[string][]appleImageSnapshot{}
	total := 0
	for _, group := range locale.Screenshots {
		expected[group.PlacementGroup] = group.Images
		total += len(group.Images)
	}
	if len(response.Data) != total {
		return fmt.Errorf("App Store placement set differs from approved screenshots")
	}
	positions := map[string]int{}
	ids := map[string]bool{}
	for _, placement := range response.Data {
		a := placement.Attributes
		index := positions[a.PlacementGroup]
		images := expected[a.PlacementGroup]
		states := []string{"PARENT_PREPARE_FOR_SUBMISSION", "PARENT_READY_FOR_REVIEW"}
		if len(review) > 0 && review[0] {
			states = append(states, "PARENT_WAITING_FOR_REVIEW", "PARENT_IN_REVIEW", "PARENT_APPROVED")
		}
		if !placement.is("appAssetLibraryPlacements", "") || ids[placement.ID] || a.MediaType != "IMAGE" || a.PlacementType != "APP_SCREENSHOT" || index >= len(images) || !containsApple(states, appleStringState(placement)) {
			return fmt.Errorf("unexpected, unready or duplicate App Store placement")
		}
		if placement.ID != images[index].PlacementID || placement.related("image") != (appleRef{Type: "appAssetLibraryImages", ID: images[index].ImageID}) {
			return fmt.Errorf("App Store screenshot display order or image changed")
		}
		ids[placement.ID] = true
		positions[a.PlacementGroup]++
	}
	return nil
}

func (e *Engine) appleDiscoverMaterials(ctx context.Context, p Profile, receipt *appleReceipt) error {
	if _, _, err := e.appleMaterialParentLocales(ctx, p, receipt, true); err != nil {
		return err
	}
	var library appleResponse
	if err := e.appleCall(ctx, p, "GET", "/v1/apps/"+p.AppID+"/assetLibrary", nil, &library); err != nil {
		return err
	}
	if !library.Data.is("appAssetLibraries", "") {
		return fmt.Errorf("App Store app asset library identity missing")
	}
	receipt.Material.LibraryID = library.Data.ID
	if err := e.appleMaterialCatalog(ctx, p, receipt.Material); err != nil {
		return err
	}
	for i := range receipt.Material.Locales {
		if err := e.appleMaterialMedia(ctx, p, &receipt.Material.Locales[i], false); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) appleVerifyMaterials(ctx context.Context, p Profile, receipt *appleReceipt, placed bool, review ...bool) (bool, error) {
	snapshot := receipt.Material
	if snapshot == nil || snapshot.AppID != p.AppID || snapshot.Digest != p.MetadataDigest || !appleResourceID.MatchString(snapshot.LibraryID) {
		return false, fmt.Errorf("App Store material snapshot differs from administrator binding")
	}
	versions, infos, err := e.appleMaterialParentLocales(ctx, p, receipt, false, review...)
	if err != nil {
		return false, err
	}
	if err = e.appleMaterialCatalog(ctx, p, snapshot); err != nil {
		return false, err
	}
	var library appleResponse
	if err = e.appleCall(ctx, p, "GET", "/v1/apps/"+p.AppID+"/assetLibrary", nil, &library); err != nil {
		return false, err
	}
	if !library.Data.is("appAssetLibraries", snapshot.LibraryID) {
		return false, fmt.Errorf("App Store asset library changed")
	}
	ready := true
	for i := range snapshot.Locales {
		locale := &snapshot.Locales[i]
		if versions[locale.Locale].Attributes.appleVersionText != locale.VersionText || infos[locale.Locale].Attributes.appleInfoText != locale.AppInfo {
			return false, fmt.Errorf("App Store material text changed from the approved snapshot")
		}
		if err = e.appleMaterialMedia(ctx, p, locale, placed, review...); err != nil {
			return false, err
		}
		for _, group := range locale.Screenshots {
			for _, image := range group.Images {
				imageReady, err := e.appleVerifyMaterialImage(ctx, p, snapshot.LibraryID, image, review...)
				if err != nil {
					return false, err
				}
				ready = ready && imageReady
			}
		}
	}
	return ready, nil
}

func (e *Engine) appleVerifyMaterialImage(ctx context.Context, p Profile, libraryID string, image appleImageSnapshot, review ...bool) (bool, error) {
	var err error
	if !appleResourceID.MatchString(image.ImageID) {
		return false, fmt.Errorf("App Store image journal is incomplete")
	}
	var images appleList
	q := url.Values{"filter[id]": {image.ImageID}, "limit": {"2"}}
	if err = e.appleCall(ctx, p, "GET", "/v1/appAssetLibraries/"+libraryID+"/images?"+q.Encode(), nil, &images); err != nil {
		return false, err
	}
	if images.Links.Next != "" || len(images.Data) != 1 || !images.Data[0].is("appAssetLibraryImages", image.ImageID) {
		return false, fmt.Errorf("App Store image no longer belongs to the approved app library")
	}
	resource := images.Data[0]
	a := resource.Attributes
	if a.FileName != image.Filename || a.FileSize != image.Bytes || a.Category != "APP_SCREENSHOTS_AND_PREVIEWS" {
		return false, fmt.Errorf("App Store immutable image identity changed")
	}
	state := appleStringState(resource)
	states := []string{"PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW"}
	if len(review) > 0 && review[0] {
		states = append(states, "WAITING_FOR_REVIEW", "IN_REVIEW", "ACCEPTED", "APPROVED", "REJECTED")
	}
	if state == "UPLOAD_COMPLETE" {
		return false, nil
	} else if containsApple(states, state) {
		if a.SpecID != image.SpecID || a.ImageAsset.Width != image.Width || a.ImageAsset.Height != image.Height {
			return false, fmt.Errorf("App Store processed image specification differs from approved screenshot")
		}
	} else {
		return false, fmt.Errorf("App Store image is failed, archived or not in a recognized ready/processing state")
	}
	return true, nil
}
