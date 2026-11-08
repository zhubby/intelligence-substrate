package publisher

import (
	"context"
	"fmt"
	"path/filepath"
)

// Nil optional attributes clear old portal values; omission would retain them.
func appleTextPatch(value any) map[string]any {
	out := appleMaterialJSON(value)
	for key, v := range out {
		if v == "" {
			out[key] = nil
		}
	}
	return out
}

func (e *Engine) appleWriteMaterials(ctx context.Context, p Profile, r Request, receipt *appleReceipt, material *appleMaterial) error {
	for li := range receipt.Material.Locales {
		locale := &receipt.Material.Locales[li]
		for _, patch := range []struct {
			kind, id, stage string
			attrs           any
		}{
			{"appStoreVersionLocalizations", locale.VersionLocalizationID, "metadata_version_patch_intent", locale.VersionText},
			{"appInfoLocalizations", locale.InfoLocalizationID, "metadata_info_patch_intent", locale.AppInfo},
		} {
			if err := e.appleCheckpoint(ctx, r, receipt, patch.stage); err != nil {
				return err
			}
			var response appleResponse
			if err := e.appleCall(ctx, p, "PATCH", "/v1/"+patch.kind+"/"+patch.id, applePayload(patch.kind, patch.id, appleTextPatch(patch.attrs), nil), &response); err != nil {
				return err
			}
			if !response.Data.is(patch.kind, patch.id) {
				return fmt.Errorf("App Store text patch did not confirm the exact localization")
			}
		}
		for gi := range locale.Screenshots {
			for ii := range locale.Screenshots[gi].Images {
				image := &locale.Screenshots[gi].Images[ii]
				if err := e.appleUploadMaterialImage(ctx, p, r, receipt, material, image); err != nil {
					return err
				}
			}
		}
	}
	if _, err := e.appleVerifyMaterials(ctx, p, receipt, false); err != nil {
		return err
	}
	return e.appleCheckpoint(ctx, r, receipt, "materials_uploaded")
}

func (e *Engine) applePlaceMaterials(ctx context.Context, p Profile, r Request, receipt *appleReceipt, review ...bool) error {
	ready, err := e.appleVerifyMaterials(ctx, p, receipt, false, review...)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("App Store screenshots are still processing")
	}
	for li := range receipt.Material.Locales {
		locale := &receipt.Material.Locales[li]
		for gi := range locale.Screenshots {
			group := &locale.Screenshots[gi]
			ordered := []appleRef{}
			for ii := range group.Images {
				image := &group.Images[ii]
				if err = e.appleCheckpoint(ctx, r, receipt, "image_placement_intent"); err != nil {
					return err
				}
				var placement appleResponse
				attrs := map[string]string{"placementType": "APP_SCREENSHOT", "placementGroup": group.PlacementGroup}
				rel := map[string]any{"image": appleRel("appAssetLibraryImages", image.ImageID), "appStoreVersionLocalization": appleRel("appStoreVersionLocalizations", locale.VersionLocalizationID)}
				if err = e.appleCall(ctx, p, "POST", "/v1/appAssetLibraryPlacements", applePayload("appAssetLibraryPlacements", "", attrs, rel), &placement); err != nil {
					return err
				}
				a := placement.Data.Attributes
				if !placement.Data.is("appAssetLibraryPlacements", "") || a.MediaType != "IMAGE" || a.PlacementType != "APP_SCREENSHOT" || a.PlacementGroup != group.PlacementGroup || placement.Data.related("image") != (appleRef{Type: "appAssetLibraryImages", ID: image.ImageID}) {
					return fmt.Errorf("App Store placement receipt differs from approved screenshot")
				}
				image.PlacementID = placement.Data.ID
				ordered = append(ordered, appleRef{Type: "appAssetLibraryPlacements", ID: image.PlacementID})
			}
			if err = e.appleCheckpoint(ctx, r, receipt, "image_order_intent"); err != nil {
				return err
			}
			var order struct {
				Data appleRef `json:"data"`
			}
			rel := map[string]any{"orderedPlacements": map[string]any{"data": ordered}, "appStoreVersionLocalization": appleRel("appStoreVersionLocalizations", locale.VersionLocalizationID)}
			if err = e.appleCall(ctx, p, "POST", "/v1/appAssetLibraryPlacementOrderingRequests", applePayload("appAssetLibraryPlacementOrderingRequests", "", map[string]string{"placementGroup": group.PlacementGroup}, rel), &order); err != nil {
				return err
			}
			if order.Data.Type != "appAssetLibraryPlacementOrderingRequests" || !appleResourceID.MatchString(order.Data.ID) {
				return fmt.Errorf("App Store placement order was not confirmed")
			}
		}
	}
	ready, err = e.appleVerifyMaterials(ctx, p, receipt, true, review...)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("App Store material processing changed before build selection")
	}
	return e.appleCheckpoint(ctx, r, receipt, "materials_placed")
}

func (e *Engine) appleUploadMaterialImage(ctx context.Context, p Profile, r Request, receipt *appleReceipt, material *appleMaterial, image *appleImageSnapshot) error {
	if err := e.appleCheckpoint(ctx, r, receipt, "image_create_intent"); err != nil {
		return err
	}
	attrs := map[string]any{"fileName": image.Filename, "fileSize": image.Bytes, "category": "APP_SCREENSHOTS_AND_PREVIEWS", "referenceName": "sha256:" + image.SHA256}
	var reservation appleResponse
	if err := e.appleCall(ctx, p, "POST", "/v1/appAssetLibraryImages", applePayload("appAssetLibraryImages", "", attrs, map[string]any{"assetLibrary": appleRel("appAssetLibraries", receipt.Material.LibraryID)}), &reservation); err != nil {
		return err
	}
	if !reservation.Data.is("appAssetLibraryImages", "") || reservation.Data.Attributes.FileName != image.Filename || reservation.Data.Attributes.FileSize != image.Bytes || reservation.Data.Attributes.Category != "APP_SCREENSHOTS_AND_PREVIEWS" || appleStringState(reservation.Data) != "AWAITING_UPLOAD" {
		return fmt.Errorf("App Store image reservation differs from approved bytes")
	}
	image.ImageID = reservation.Data.ID
	guard := func() error { return e.appleCheckpoint(ctx, r, receipt, "image_bytes_upload_intent") }
	if err := guard(); err != nil {
		return err
	}
	if err := e.appleUploadBytes(ctx, p, filepath.Join(material.Dir, image.Filename), reservation.Data.Attributes.UploadOperations, image.Bytes, guard); err != nil {
		return err
	}
	if err := e.appleCheckpoint(ctx, r, receipt, "image_commit_intent"); err != nil {
		return err
	}
	var committed appleResponse
	// The current library protocol deliberately has no sourceFileChecksum.
	if err := e.appleCall(ctx, p, "PATCH", "/v1/appAssetLibraryImages/"+image.ImageID, applePayload("appAssetLibraryImages", image.ImageID, map[string]bool{"uploaded": true}, nil), &committed); err != nil {
		return err
	}
	if !committed.Data.is("appAssetLibraryImages", image.ImageID) || !containsApple([]string{"UPLOAD_COMPLETE", "PREPARE_FOR_SUBMISSION"}, appleStringState(committed.Data)) {
		return fmt.Errorf("App Store image commit was not positively accepted")
	}
	return nil
}
