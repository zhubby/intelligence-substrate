package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/trustedtls"
)

type Model struct {
	Service          *p.Service
	BaseURL          string
	Key              string
	Name             string
	External         bool
	InputUSD         float64
	OutputUSD        float64
	MinInterval      time.Duration
	DefaultMaxTokens int
	Options          p.M
	Client           *http.Client
}

func ModelFromEnv(s *p.Service) *Model {
	base := strings.TrimRight(os.Getenv("IS_MODEL_BASE_URL"), "/")
	name := os.Getenv("IS_MODEL_NAME")
	key, keyOK := modelAPIKeyFromEnv()
	if !keyOK {
		return nil
	}
	options := p.M{}
	if value := os.Getenv("IS_MODEL_OPTIONS_JSON"); value != "" {
		if len(value) > 2048 || json.Unmarshal([]byte(value), &options) != nil || options == nil {
			return nil
		}
	}
	if !validModelOptions(options) {
		return nil
	}
	ip, ie := strconv.ParseFloat(os.Getenv("IS_MODEL_INPUT_USD_PER_MILLION"), 64)
	op, oe := strconv.ParseFloat(os.Getenv("IS_MODEL_OUTPUT_USD_PER_MILLION"), 64)
	interval := 0.0
	defaultTokens := 4096
	var tokensErr error
	if value := os.Getenv("IS_MODEL_DEFAULT_MAX_TOKENS"); value != "" {
		defaultTokens, tokensErr = strconv.Atoi(value)
	}
	var intervalErr error
	if value := os.Getenv("IS_MODEL_MIN_INTERVAL_SECONDS"); value != "" {
		interval, intervalErr = strconv.ParseFloat(value, 64)
	}
	if base == "" || name == "" || ie != nil || oe != nil || !validModelPrices(ip, op) || tokensErr != nil || defaultTokens < 1 || defaultTokens > 8192 || intervalErr != nil || math.IsNaN(interval) || math.IsInf(interval, 0) || interval < 0 || interval > 300 {
		return nil
	}
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && os.Getenv("IS_MODEL_ALLOW_HTTP") == "true")) {
		return nil
	}
	return &Model{Service: s, BaseURL: base, Key: key, Name: name, External: os.Getenv("IS_MODEL_EXTERNAL") != "false", InputUSD: ip, OutputUSD: op, MinInterval: time.Duration(interval * float64(time.Second)), DefaultMaxTokens: defaultTokens, Options: options, Client: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// modelAPIKeyFromEnv keeps the secret out of ordinary environment dumps. The
// legacy inline variable remains supported for development compatibility, but
// an explicitly configured file is authoritative and must be a private,
// regular file. The file contents are trimmed once so a newline from a secret
// manager does not become part of the Authorization header.
func modelAPIKeyFromEnv() (string, bool) {
	path := strings.TrimSpace(os.Getenv("IS_MODEL_API_KEY_FILE"))
	if path != "" {
		body, err := trustedtls.ReadFile(path, true)
		if err != nil {
			return "", false
		}
		key := strings.TrimSpace(string(body))
		return key, key != ""
	}
	return os.Getenv("IS_MODEL_API_KEY"), true
}
func (m *Model) cost(input, output int) int64 {
	return int64(math.Ceil((float64(input)*m.InputUSD + float64(output)*m.OutputUSD) * 100 / 1000000))
}
func validModelPrices(input, output float64) bool {
	// The platform caps a task at 100,000,000 cents. Validate the largest
	// permitted call before any float-to-integer conversion or reservation.
	maximum := (130048*input + 8192*output) * 100 / 1000000
	return input >= 0 && output >= 0 && !math.IsNaN(maximum) && !math.IsInf(maximum, 0) && maximum <= 100000000
}
func (m *Model) Chat(ctx context.Context, a p.Actor, in p.M) (p.M, error) {
	return m.chat(ctx, a, in, "")
}

// Internal planning/draft reasoning requires a trusted generation intent.
// Public chat fields cannot select it or change its approval/role requirements.
func (m *Model) chat(ctx context.Context, a p.Actor, in p.M, generationRequestID string) (p.M, error) {
	if !validModelPrices(m.InputUSD, m.OutputUSD) || !validModelOptions(m.Options) {
		return nil, &p.Fault{Code: "model_configuration", Message: "模型价格或推理选项配置无效", Status: 503}
	}
	goalID := p.S(in, "goalId")
	g, e := m.Service.GetResource(ctx, a, goalID)
	if e != nil || g.Kind != "goal" {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &p.Fault{Code: "forbidden", Message: "目标不可访问", Status: 403}
	}
	if e = m.Service.CanAccess(ctx, a, g.ProjectID, "product", "architect", "developer", "analysis", "design", "security"); e != nil {
		return nil, e
	}
	project, e := m.Service.Store.Get(g.ProjectID)
	if e != nil {
		return nil, e
	}
	if m.External && !p.B(project.Data, "allowExternalModels") {
		return nil, &p.Fault{Code: "model_policy", Message: "此项目未授权外部模型访问", Status: 403}
	}
	messages := p.Items(in["messages"])
	if len(messages) == 0 {
		return nil, &p.Fault{Code: "invalid", Message: "模型消息不能为空", Status: 422}
	}
	contextPayload := p.M{"messages": messages}
	for key, value := range m.Options {
		contextPayload[key] = value
	}
	for _, key := range []string{"tools", "tool_choice", "response_format", "temperature"} {
		if v, ok := in[key]; ok {
			contextPayload[key] = v
		}
	}
	encoded, encodeErr := json.Marshal(contextPayload)
	if encodeErr != nil || len(encoded) > 128000 {
		return nil, &p.Fault{Code: "invalid", Message: "模型上下文超过 128 KB", Status: 422}
	}
	limit := float64(m.DefaultMaxTokens)
	if limit == 0 {
		limit = 4096
	}
	if value, exists := in["max_tokens"]; exists {
		switch value.(type) {
		case int, int64, float64, json.Number:
			limit = p.N(in, "max_tokens")
		default:
			limit = 0
		}
	}
	if math.IsNaN(limit) || math.IsInf(limit, 0) || limit != math.Trunc(limit) || limit < 1 || limit > 8192 {
		return nil, &p.Fault{Code: "invalid", Message: "模型输出预算无效", Status: 422}
	}
	maxTokens := int(limit)
	reserved := m.cost(len(encoded)+2048, maxTokens)
	charge := p.Resource{}
	runID := p.S(in, "runId")
	if a.Kind != "worker" && runID != "" {
		return nil, &p.Fault{Code: "forbidden", Message: "执行费用只能由对应租约的节点上报", Status: 403}
	}
	for {
		e = m.Service.Store.Update(ctx, func(t *p.Tx) error {
			if err := t.RequireProjectAccess(a, g.ProjectID, "product", "architect", "developer", "analysis", "design", "security"); err != nil {
				return err
			}
			current, err := t.Get(goalID)
			if err != nil {
				return err
			}
			generationChargeKey := ""
			if generationRequestID != "" {
				actor, err := t.CurrentActor(a)
				if err != nil {
					return err
				}
				request, err := t.Get(generationRequestID)
				if err != nil {
					return err
				}
				if request.Kind == "plan_generation" {
					if !p.Has([]string{"human", "agent"}, actor.Kind) || p.S(request.Data, "requestedBy") != actor.ID || p.S(request.Data, "requestedKind") != actor.Kind {
						return &p.Fault{Code: "forbidden", Message: "方案生成请求不属于当前身份", Status: 403}
					}
					if err = t.RequireProjectAccess(actor, g.ProjectID, "product", "architect", "developer"); err != nil {
						return err
					}
					if err = p.ValidatePlanGenerationContext(t, current, request); err != nil {
						return err
					}
					generationChargeKey = "planRequestId"
				} else {
					if actor.Kind != "human" {
						return &p.Fault{Code: "human_required", Message: "需求模型澄清需要人类产品负责人发起", Status: 403}
					}
					if err = t.RequireProjectAccess(actor, g.ProjectID, "product"); err != nil {
						return err
					}
					if !p.Has([]string{"clarification_generation", "requirement_generation"}, request.Kind) || request.GoalID != current.ID || request.ProjectID != current.ProjectID || request.Status != "running" || p.S(request.Data, "requestedBy") != actor.ID || p.N(request.Data, "requirementRevision") != float64(current.Revision) || current.Status != "awaiting_baseline" || p.N(current.Data, "approvedRevision") == float64(current.Revision) {
						return &p.Fault{Code: "approval_required", Message: "澄清生成不属于当前未批准需求版本", Status: 409}
					}
					generationChargeKey = "clarificationRequestId"
					if request.Kind == "requirement_generation" {
						if err = p.ValidateRequirementGenerationContext(t, current, request); err != nil {
							return err
						}
						generationChargeKey = "requirementRequestId"
					}
				}
			} else if current.Revision != g.Revision || int(p.N(current.Data, "approvedRevision")) != current.Revision {
				return &p.Fault{Code: "approval_required", Message: "模型执行前需要当前需求基线批准", Status: 409}
			}
			currentProject, err := t.Get(g.ProjectID)
			if err != nil {
				return err
			}
			if m.External && !p.B(currentProject.Data, "allowExternalModels") {
				return &p.Fault{Code: "model_policy", Message: "此项目未授权外部模型访问", Status: 403}
			}
			charges, err := t.List(g.ProjectID, "charge", g.ID)
			if err != nil {
				return err
			}
			held := 0.0
			calls := 0
			for _, c := range charges {
				if generationRequestID != "" && p.S(c.Data, generationChargeKey) == generationRequestID {
					return &p.Fault{Code: "generation_not_retried", Message: "生成请求已有模型费用记录，未重复调用", Status: 409}
				}
				calls++
				if c.Status == "reserved" {
					held += p.N(c.Data, "reservedCents")
				}
			}
			if calls >= 200 || p.N(current.Data, "spentCents")+held+float64(reserved) > p.N(current.Data, "budgetCents") {
				return &p.Fault{Code: "budget_exceeded", Message: "模型调用达到预算或次数限制", Status: 409}
			}
			if a.Kind == "worker" {
				if err := m.Service.ValidateAgentModelAdmission(t, a, in, float64(reserved)); err != nil {
					return err
				}
				run, err := t.Get(runID)
				if err != nil || run.Status != "running" || p.S(run.Data, "workerId") != a.WorkerID || run.GoalID != g.ID || p.S(in, "leaseToken") != p.S(run.Data, "leaseToken") || p.S(run.Data, "leaseUntil") < time.Now().UTC().Format(time.RFC3339Nano) {
					return &p.Fault{Code: "lease_expired", Message: "模型调用不属于当前有效执行租约", Status: 403}
				}
				runHeld := 0.0
				for _, c := range charges {
					if c.Status == "reserved" && p.S(c.Data, "runId") == runID {
						runHeld += p.N(c.Data, "reservedCents")
					}
				}
				if p.N(run.Data, "spentCents")+runHeld+float64(reserved) > p.N(run.Data, "reservedCents") {
					return &p.Fault{Code: "budget_exceeded", Message: "工作项模型预算不足", Status: 409}
				}
			}
			if err := m.admitModel(t); err != nil {
				return err
			}
			ts := time.Now().UTC().Format(time.RFC3339Nano)
			charge = p.Resource{ID: p.NewID("charge"), Kind: "charge", ProjectID: g.ProjectID, GoalID: g.ID, Title: m.Name, Status: "reserved", Revision: 1, CreatedAt: ts, UpdatedAt: ts, Data: p.M{"reservedCents": reserved, "runId": runID, "actorId": a.ID, "inputBound": len(encoded) + 2048, "outputBound": maxTokens, "providerOrigin": m.BaseURL, "protocol": "chat-completions", "inputUSDPerMillion": m.InputUSD, "outputUSDPerMillion": m.OutputUSD, "pricingBasis": "configured-estimate", "requestOptions": m.Options}}
			if generationRequestID != "" {
				charge.Data[generationChargeKey] = generationRequestID
				charge.Data["requirementRevision"] = current.Revision
			}
			return t.Put(charge)
		})
		var wait *modelAdmissionWait
		if !errors.As(e, &wait) {
			break
		}
		timer := time.NewTimer(time.Until(wait.until))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if e != nil {
		return nil, e
	}
	payload := p.M{"model": m.Name, "messages": messages, "max_tokens": maxTokens}
	for key, value := range m.Options {
		payload[key] = value
	}
	for _, k := range []string{"tools", "tool_choice", "response_format", "temperature"} {
		if v, ok := in[k]; ok {
			payload[k] = v
		}
	}
	body, _ := json.Marshal(payload)
	req, e := http.NewRequestWithContext(ctx, "POST", m.BaseURL+"/chat/completions", bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	if m.Key != "" {
		req.Header.Set("Authorization", "Bearer "+m.Key)
	}
	response, callErr := m.Client.Do(req)
	out := p.M{}
	httpStatus := 0
	if callErr == nil {
		defer response.Body.Close()
		httpStatus = response.StatusCode
		b, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil {
			callErr = err
		} else if len(b) > 2<<20 {
			callErr = fmt.Errorf("model response exceeds 2 MiB")
		} else if response.StatusCode < 200 || response.StatusCode >= 300 {
			callErr = fmt.Errorf("model HTTP %d", response.StatusCode)
		} else {
			callErr = json.Unmarshal(b, &out)
		}
	}
	charged := reserved
	uncertain := callErr != nil
	usage := p.Obj(out["usage"])
	outcome := "transport_error"
	if callErr == nil {
		input, inputOK := usage["prompt_tokens"].(float64)
		output, outputOK := usage["completion_tokens"].(float64)
		if inputOK && outputOK && input >= 0 && output >= 0 && input == math.Trunc(input) && output == math.Trunc(output) && input <= float64(len(encoded)+2048) && output <= float64(maxTokens) {
			charged = m.cost(int(input), int(output))
		} else {
			uncertain = true
		}
		outcome, callErr = modelOutputStatus(out, in)
		if uncertain {
			outcome = "usage_unverified"
			callErr = fmt.Errorf("model usage is missing, invalid or exceeds the reserved bounds")
		}
	}
	settleErr := m.Service.Store.Update(context.Background(), func(t *p.Tx) error {
		c, err := t.Get(charge.ID)
		if err != nil {
			return err
		}
		if c.Status != "reserved" {
			return nil
		}
		current, err := t.Get(g.ID)
		if err != nil {
			return err
		}
		c.Status = "settled"
		if uncertain {
			c.Status = "uncertain"
		}
		c.Data["chargedCents"] = charged
		c.Data["usage"] = usage
		c.Data["outcome"] = outcome
		c.Data["httpStatus"] = httpStatus
		for key, value := range map[string]string{"providerResponseId": p.S(out, "id"), "providerModel": p.S(out, "model")} {
			if len(value) <= 256 {
				c.Data[key] = value
			}
		}
		current.Data["spentCents"] = p.N(current.Data, "spentCents") + float64(charged)
		if runID != "" {
			run, err := t.Get(runID)
			if err != nil {
				return err
			}
			run.Data["spentCents"] = p.N(run.Data, "spentCents") + float64(charged)
			if err = t.Put(run); err != nil {
				return err
			}
		}
		if err = t.Put(current); err != nil {
			return err
		}
		if err = t.Put(c); err != nil {
			return err
		}
		return t.Audit(a, "model.charge", c, p.M{"status": c.Status, "chargedCents": charged})
	})
	if settleErr != nil {
		return nil, settleErr
	}
	if callErr != nil {
		return nil, &p.Fault{Code: "model_failed", Message: "模型未返回可用且费用已核实的完整结果，已保留费用与核对记录", Status: 502}
	}
	return out, nil
}
