package platform

import "context"

type CostProblem struct {
	ResourceID string `json:"resourceId"`
	Category   string `json:"category"`
	Reason     string `json:"reason"`
}

// CostReport reports the recorded scope. Missing invoices, allocations and
// labor time cannot establish a complete actual delivery cost.
type CostReport struct {
	ProjectID             string           `json:"projectId"`
	GoalID                string           `json:"goalId,omitempty"`
	Currency              string           `json:"currency"`
	GeneratedAt           string           `json:"generatedAt"`
	Complete              bool             `json:"complete"`
	ModelBudgetCents      int64            `json:"modelBudgetCents"`
	ModelEstimatedCents   int64            `json:"modelEstimatedCents"`
	ModelReconciledCents  int64            `json:"modelReconciledCents"`
	ModelPendingCents     int64            `json:"modelPendingCents"`
	ExternalRecordedCents int64            `json:"externalRecordedCents"`
	RecordedTotalCents    int64            `json:"recordedTotalCents"`
	ExposureCents         int64            `json:"exposureCents"`
	ExternalByCategory    map[string]int64 `json:"externalByCategory"`
	UnreportedCategories  []string         `json:"unreportedCategories"`
	Unmeasurable          []CostProblem    `json:"unmeasurable"`
	Charges               []Resource       `json:"charges"`
	Expenses              []Resource       `json:"expenses"`
}

func (s *Service) DeliveryCosts(ctx context.Context, actor Actor, projectID, goalID string) (CostReport, error) {
	report := CostReport{ProjectID: projectID, GoalID: goalID, Currency: "USD", ExternalByCategory: map[string]int64{}, UnreportedCategories: []string{}, Unmeasurable: []CostProblem{}, Charges: []Resource{}, Expenses: []Resource{}}
	err := s.Store.Update(ctx, func(tx *Tx) error {
		current, err := tx.CurrentActor(actor)
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
		if goalID != "" {
			goal, err := tx.Get(goalID)
			if err != nil {
				return err
			}
			if goal.Kind != "goal" || goal.ProjectID != projectID {
				return denied()
			}
		}
		items, err := tx.List(projectID, "", goalID)
		if err != nil {
			return err
		}
		problem := func(r Resource, category, reason string) {
			report.Unmeasurable = append(report.Unmeasurable, CostProblem{r.ID, category, reason})
		}
		for _, r := range items {
			switch r.Kind {
			case "goal":
				amount, err := costCents(r.Data["budgetCents"])
				if err != nil {
					problem(r, "model_budget", "模型预算金额缺失或无效")
					continue
				}
				report.ModelBudgetCents += amount
			case "charge":
				report.Charges = append(report.Charges, r)
				var value any
				switch r.Status {
				case "reserved":
					value = r.Data["reservedCents"]
				case "uncertain":
					value = r.Data["chargedCents"]
					if value == nil {
						value = r.Data["reservedCents"]
					}
				case "settled":
					value = r.Data["chargedCents"]
				default:
					problem(r, "model", "模型费用状态未知")
					continue
				}
				amount, err := costCents(value)
				if err != nil {
					problem(r, "model", "模型费用金额缺失或不是有效整数美分")
					continue
				}
				if r.Status != "settled" {
					report.ModelPendingCents += amount
				} else if S(r.Data, "reconciledBy") != "" {
					report.ModelReconciledCents += amount
				} else {
					report.ModelEstimatedCents += amount
				}
			case "expense":
				report.Expenses = append(report.Expenses, r)
				if r.Status == "voided" {
					continue
				}
				if r.Status != "recorded" || S(r.Data, "currency") != "USD" || !Has(expenseCategories, S(r.Data, "category")) {
					problem(r, "external", "外部费用状态、币种或类别无效")
					continue
				}
				amount, err := costCents(r.Data["amountCents"])
				if err != nil {
					problem(r, S(r.Data, "category"), "外部费用金额缺失或不是有效整数美分")
					continue
				}
				report.ExternalRecordedCents += amount
				report.ExternalByCategory[S(r.Data, "category")] += amount
			}
		}
		for _, category := range []string{"build", "device", "storage", "channel", "labor"} {
			if _, exists := report.ExternalByCategory[category]; !exists {
				report.UnreportedCategories = append(report.UnreportedCategories, category)
			}
		}
		report.RecordedTotalCents = report.ModelEstimatedCents + report.ModelReconciledCents + report.ExternalRecordedCents
		report.ExposureCents = report.RecordedTotalCents + report.ModelPendingCents
		report.GeneratedAt = now()
		return nil
	})
	return report, err
}
