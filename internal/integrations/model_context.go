package integrations

import (
	"fmt"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Operator-controlled compatibility options cannot replace messages, model,
// streaming, credentials or any budget/authority field.
func validModelOptions(options p.M) bool {
	for key, value := range options {
		switch key {
		case "thinking":
			thinking := p.Obj(value)
			if len(thinking) != 1 || !p.Has([]string{"enabled", "disabled"}, p.S(thinking, "type")) {
				return false
			}
		case "reasoning_effort":
			effort, ok := value.(string)
			if !ok || !p.Has([]string{"low", "medium", "high", "max"}, effort) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// modelOutputStatus validates completion separately from usage. A provider can
// report exact billable tokens even when its answer was refused or truncated.
func modelOutputStatus(out, input p.M) (string, error) {
	choices := p.Items(out["choices"])
	if len(choices) != 1 {
		return "invalid_response", fmt.Errorf("model must return one completion")
	}
	choice := p.Obj(choices[0])
	message := p.Obj(choice["message"])
	if refusal, exists := message["refusal"]; exists && refusal != nil && refusal != "" {
		return "refused", fmt.Errorf("model refused the request")
	}
	switch p.S(choice, "finish_reason") {
	case "stop":
		content, ok := message["content"].(string)
		if ok && strings.TrimSpace(content) != "" {
			return "completed", nil
		}
	case "tool_calls":
		if len(p.Items(input["tools"])) > 0 && len(p.Items(message["tool_calls"])) > 0 {
			return "completed", nil
		}
	case "length":
		return "incomplete", fmt.Errorf("model exhausted its output limit")
	case "content_filter":
		return "refused", fmt.Errorf("model output was filtered")
	}
	return "invalid_response", fmt.Errorf("model did not report a usable complete answer")
}
