package platform

import (
	"context"
	"sort"
	"time"
)

type DeliveryMetricReport struct {
	ProjectID                           string            `json:"projectId"`
	GoalID                              string            `json:"goalId,omitempty"`
	GeneratedAt                         string            `json:"generatedAt"`
	Requirements                        int               `json:"requirements"`
	PreviewSamples                      int               `json:"previewSamples"`
	MeanPreviewSeconds                  *float64          `json:"meanPreviewSeconds"`
	AcceptanceSamples                   int               `json:"acceptanceSamples"`
	FirstPassCount                      int               `json:"firstPassCount"`
	FirstPassRate                       *float64          `json:"firstPassRate"`
	EffortSamples                       int               `json:"effortSamples"`
	HumanSeconds                        *float64          `json:"humanSeconds"`
	ReportedProductionDefects           int               `json:"reportedProductionDefects"`
	PublishExecutions                   int               `json:"publishExecutions"`
	FailedPublishExecutions             int               `json:"failedPublishExecutions"`
	PublishExecutionFailureRate         *float64          `json:"publishExecutionFailureRate"`
	ObservedChannelWaitSeconds          float64           `json:"observedChannelWaitSeconds"`
	ChannelHistoryComplete              bool              `json:"channelHistoryComplete"`
	TotalCostPerSuccessfulDeliveryCents *float64          `json:"totalCostPerSuccessfulDeliveryCents"`
	ManualBaselineAvailable             bool              `json:"manualBaselineAvailable"`
	Versions                            []MetricVersion   `json:"versions"`
	Observations                        []MeasurementView `json:"observations"`
	Gaps                                []string          `json:"gaps"`
}

type MetricVersion struct {
	GoalID                string   `json:"goalId"`
	Title                 string   `json:"title"`
	RequirementRevision   int      `json:"requirementRevision"`
	Current               bool     `json:"current"`
	StartedAt             string   `json:"startedAt"`
	FirstPreviewID        string   `json:"firstPreviewId,omitempty"`
	PreviewSeconds        *float64 `json:"previewSeconds"`
	FirstAcceptanceID     string   `json:"firstAcceptanceId,omitempty"`
	FirstAcceptanceResult string   `json:"firstAcceptanceResult,omitempty"`
}

// Bounded read model: no arbitrary task/run/provider payloads.
type MeasurementView struct {
	ID                      string   `json:"id"`
	GoalID                  string   `json:"goalId"`
	RequirementRevision     int      `json:"requirementRevision"`
	Type                    string   `json:"type"`
	Status                  string   `json:"status"`
	Reference               string   `json:"reference"`
	Note                    string   `json:"note"`
	RecordedBy              string   `json:"recordedBy"`
	RecordedAt              string   `json:"recordedAt"`
	Result                  string   `json:"result,omitempty"`
	ArtifactID              string   `json:"artifactId,omitempty"`
	EvidenceID              string   `json:"evidenceId,omitempty"`
	ReleaseID               string   `json:"releaseId,omitempty"`
	ProofArtifactID         string   `json:"proofArtifactId,omitempty"`
	ProofDigest             string   `json:"proofDigest,omitempty"`
	Commit                  string   `json:"commit,omitempty"`
	VerificationEnvironment string   `json:"verificationEnvironment,omitempty"`
	StartedAt               string   `json:"startedAt,omitempty"`
	EndedAt                 string   `json:"endedAt,omitempty"`
	DurationSeconds         float64  `json:"durationSeconds,omitempty"`
	ReplacesMeasurementID   string   `json:"replacesMeasurementId,omitempty"`
	VoidReason              string   `json:"voidReason,omitempty"`
	VoidedBy                string   `json:"voidedBy,omitempty"`
	VoidedAt                string   `json:"voidedAt,omitempty"`
	Usable                  bool     `json:"usable"`
	Issues                  []string `json:"issues"`
}

func (s *Service) DeliveryMetrics(ctx context.Context, a Actor, projectID, goalID string, rev int) (DeliveryMetricReport, error) {
	report := DeliveryMetricReport{ProjectID: projectID, GoalID: goalID, Versions: []MetricVersion{}, Observations: []MeasurementView{}, Gaps: []string{
		"仅统计已登记且依据可核验的观测；未采集数据不代表零耗时、未通过或无缺陷。",
		"完整成功交付成本尚不可测，已登记费用请查看交付成本账本。",
		"人工研发对照基线尚未采集，不能计算效率提升比例。",
		"业务验收为人工登记判断，技术测试通过不计为业务验收。",
		"上线缺陷仅为登记数，尚无完整生产观察窗口。",
		"发布执行失败率只统计已结束的提交/继续提审操作，不能证明版本最终失败率。",
		"渠道等待为各渠道观测区间之和，单位渠道秒；并行区间不能视为交付日历时长。",
	}}
	err := s.Store.Update(ctx, func(tx *Tx) error {
		current, err := tx.CurrentActor(a)
		if err != nil {
			return err
		}
		if err = s.allowed(tx, current, projectID); err != nil {
			return err
		}
		project, err := tx.Get(projectID)
		if err != nil {
			return err
		}
		if project.Kind != "project" {
			return missing()
		}
		if rev < 0 || (rev > 0 && goalID == "") {
			return invalid("历史需求版本需要同时指定交付目标")
		}
		if goalID != "" {
			g, err := tx.Get(goalID)
			if err != nil {
				return err
			}
			if g.Kind != "goal" || g.ProjectID != projectID {
				return denied()
			}
		}
		items, err := tx.List(projectID, "", "")
		if err != nil {
			return err
		}
		byID := map[string]Resource{}
		for _, r := range items {
			byID[r.ID] = r
		}
		for _, g := range items {
			if g.Kind != "goal" || (goalID != "" && g.ID != goalID) {
				continue
			}
			selected := rev
			if selected == 0 {
				selected = g.Revision
			}
			baseline, err := measurementBaseline(tx, g, selected)
			if err != nil {
				return err
			}
			report.Versions = append(report.Versions, MetricVersion{GoalID: g.ID, Title: g.Title, RequirementRevision: selected, Current: selected == g.Revision, StartedAt: baseline.CreatedAt})
		}
		sort.Slice(report.Versions, func(i, j int) bool { return report.Versions[i].GoalID < report.Versions[j].GoalID })
		sort.Slice(items, func(i, j int) bool {
			left, _ := time.Parse(time.RFC3339Nano, items[i].CreatedAt)
			right, _ := time.Parse(time.RFC3339Nano, items[j].CreatedAt)
			if left.Equal(right) {
				return items[i].ID < items[j].ID
			}
			return left.Before(right)
		})
		report.Requirements = len(report.Versions)
		var previewSum, effortSum float64
		channels, fullChannels := 0, 0
		at := time.Now().UTC()
		for i := range report.Versions {
			v := &report.Versions[i]
			origin, err := time.Parse(time.RFC3339Nano, v.StartedAt)
			if err != nil {
				return conflict("需求版本起始时间无法核验")
			}
			until := at
			for _, r := range items {
				if r.Kind == "requirement" && r.GoalID == v.GoalID && r.Revision == v.RequirementRevision+1 {
					cutoff, e := time.Parse(time.RFC3339Nano, r.CreatedAt)
					if e != nil {
						return conflict("后续需求版本时间无法核验")
					}
					if cutoff.Before(until) {
						until = cutoff
					}
				}
			}
			for _, r := range items {
				if r.GoalID != v.GoalID || revision(r) != v.RequirementRevision {
					continue
				}
				switch r.Kind {
				case "measurement":
					m := s.measurementView(tx, r)
					report.Observations = append(report.Observations, m)
					if !m.Usable {
						continue
					}
					switch m.Type {
					case "preview":
						when, err := time.Parse(time.RFC3339Nano, m.RecordedAt)
						if err == nil && !when.Before(origin) && v.FirstPreviewID == "" {
							seconds := when.Sub(origin).Seconds()
							v.FirstPreviewID, v.PreviewSeconds = m.ID, &seconds
							previewSum += seconds
							report.PreviewSamples++
						}
					case "business_acceptance":
						if v.FirstAcceptanceID == "" {
							v.FirstAcceptanceID, v.FirstAcceptanceResult = m.ID, m.Result
							report.AcceptanceSamples++
							if m.Result == "passed" {
								report.FirstPassCount++
							}
						}
					case "human_effort":
						report.EffortSamples++
						effortSum += m.DurationSeconds
					case "production_defect":
						report.ReportedProductionDefects++
					}
				case "run":
					task := byID[S(r.Data, "taskId")]
					if task.Kind == "task" && task.GoalID == r.GoalID && revision(task) == revision(r) && mutatingPublicationTask(task) && Has([]string{"succeeded", "failed"}, r.Status) {
						report.PublishExecutions++
						if r.Status == "failed" {
							report.FailedPublishExecutions++
						}
					}
				case "release":
					for _, x := range Items(r.Data["targets"]) {
						seconds, complete := observedChannelWait(Obj(x), until)
						report.ObservedChannelWaitSeconds += seconds
						channels++
						if complete {
							fullChannels++
						}
					}
				}
			}
		}
		if report.PreviewSamples > 0 {
			value := previewSum / float64(report.PreviewSamples)
			report.MeanPreviewSeconds = &value
		}
		if report.AcceptanceSamples > 0 {
			value := float64(report.FirstPassCount) / float64(report.AcceptanceSamples)
			report.FirstPassRate = &value
		}
		if report.EffortSamples > 0 {
			report.HumanSeconds = &effortSum
		}
		if report.PublishExecutions > 0 {
			value := float64(report.FailedPublishExecutions) / float64(report.PublishExecutions)
			report.PublishExecutionFailureRate = &value
		}
		report.ChannelHistoryComplete = channels > 0 && channels == fullChannels
		if !report.ChannelHistoryComplete {
			report.Gaps = append(report.Gaps, "渠道历史未完整采集，旧记录未观测到的等待时间无法补算。")
		}
		report.GeneratedAt = at.Format(time.RFC3339Nano)
		return nil
	})
	return report, err
}

func observedChannelWait(target M, until time.Time) (float64, bool) {
	history := Items(target["observedHistory"])
	if len(history) == 0 {
		return 0, false
	}
	complete := S(Obj(history[0]), "from") == "" && S(Obj(history[0]), "to") == "ready"
	var seconds float64
	for i, x := range history {
		event := Obj(x)
		start, err := time.Parse(time.RFC3339Nano, S(event, "at"))
		if err != nil {
			return 0, false
		}
		end := until
		if i+1 < len(history) {
			next := Obj(history[i+1])
			nextAt, err := time.Parse(time.RFC3339Nano, S(next, "at"))
			if err != nil || nextAt.Before(start) || S(next, "from") != S(event, "to") {
				return 0, false
			}
			if nextAt.Before(end) {
				end = nextAt
			}
		}
		if Has([]string{"processing", "awaiting_review", "awaiting_release"}, S(event, "to")) && end.After(start) {
			seconds += end.Sub(start).Seconds()
		}
	}
	return seconds, complete
}
