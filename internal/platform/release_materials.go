package platform

import (
	"encoding/json"
	"os"
)

// Material reports are immutable artifact references, not task-selected file paths.
func (s *Service) releaseMaterials(t *Tx, g Resource, commit string, artifacts []any, raw any) ([]any, error) {
	if raw == nil {
		return nil, nil
	}
	switch raw.(type) {
	case []any, []string:
	default:
		return nil, invalid("商店资料必须为制品 ID 列表")
	}
	ids := Strings(raw)
	if len(ids) != len(Items(raw)) {
		return nil, invalid("商店资料 ID 必须为字符串")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) != 1 {
		return nil, invalid("当前支持一份 App Store 资料清单")
	}
	ar, err := t.Get(ids[0])
	if err != nil {
		return nil, err
	}
	var pkg Resource
	for _, entry := range artifacts {
		if S(Obj(entry), "target") == "ios-appstore" {
			pkg, err = t.Get(S(Obj(entry), "id"))
			if err != nil {
				return nil, err
			}
		}
	}
	if S(ar.Data, "purpose") == "release-feedback" || ar.Kind != "artifact" || ar.ProjectID != g.ProjectID || ar.GoalID != g.ID || revision(ar) != g.Revision || S(ar.Data, "commit") != commit || S(ar.Data, "target") != "ios-appstore" || S(ar.Data, "type") != "report" || pkg.ID == "" || S(ar.Data, "repositoryId") != S(pkg.Data, "repositoryId") {
		return nil, invalid("App Store 资料必须为同需求版本、仓库、提交及渠道的 JSON 报告")
	}
	if err = s.VerifyArtifact(ar); err != nil {
		return nil, err
	}
	if N(ar.Data, "size") > 65536 {
		return nil, invalid("商店资料 JSON 不能超过 64 KiB")
	}
	body, err := os.ReadFile(s.ArtifactPath(S(ar.Data, "digest")))
	if err != nil || !json.Valid(body) {
		return nil, invalid("商店资料必须是有效 JSON")
	}
	return []any{M{"id": ar.ID, "target": "ios-appstore", "digest": S(ar.Data, "digest")}}, nil
}

func (s *Service) releaseMaterialGate(t *Tx, r Resource, g Resource) error {
	materials := Items(r.Data["storeMetadata"])
	if len(materials) == 0 {
		return nil
	}
	ids := []any{}
	for _, entry := range materials {
		ids = append(ids, S(Obj(entry), "id"))
	}
	verified, err := s.releaseMaterials(t, g, S(r.Data, "commit"), Items(r.Data["artifacts"]), ids)
	if err != nil {
		return err
	}
	if Hash(verified) != Hash(materials) {
		return conflict("商店资料与发布候选绑定不一致")
	}
	return nil
}

func releaseMetadataDigest(r Resource, target string) string {
	if value := S(releaseTarget(r, target), "metadataDigest"); value != "" {
		return value
	}
	for _, entry := range Items(r.Data["storeMetadata"]) {
		if S(Obj(entry), "target") == target {
			return S(Obj(entry), "digest")
		}
	}
	return ""
}
