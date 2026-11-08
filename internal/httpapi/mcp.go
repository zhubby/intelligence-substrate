package httpapi

import (
	"encoding/json"
	"math"
	"net/http"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

var agentCommands = []string{"goals.create", "clarifications.propose", "requirements.propose", "blueprints.propose", "designs.propose", "plans.propose", "plans.reorder", "tasks.split", "tasks.wait", "messages.send", "messages.claim", "messages.ack", "messages.receive", "repositories.create", "repositories.commit", "previews.create", "previews.stop", "reviews.create", "knowledge.create", "knowledge.revise", "releases.create", "store_revisions.propose", "runtime.rollback.propose"}

func (h *Handler) mcp(w http.ResponseWriter, r *http.Request, a p.Actor) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  p.M             `json:"params"`
	}
	if e := json.NewDecoder(r.Body).Decode(&request); e != nil || request.JSONRPC != "2.0" {
		writeJSON(w, 200, p.M{"jsonrpc": "2.0", "id": nil, "error": p.M{"code": -32600, "message": "Invalid JSON-RPC request"}})
		return
	}
	if request.ID == nil && request.Method == "notifications/initialized" {
		w.WriteHeader(202)
		return
	}
	response := p.M{"jsonrpc": "2.0", "id": request.ID}
	result := p.M{}
	switch request.Method {
	case "initialize":
		version := p.S(request.Params, "protocolVersion")
		if version != "2024-11-05" && version != "2025-03-26" && version != "2025-06-18" {
			version = "2025-06-18"
		}
		result = p.M{"protocolVersion": version, "capabilities": p.M{"tools": p.M{}}, "serverInfo": p.M{"name": "intelligence-substrate", "version": "0.1.0"}, "instructions": "Use project-scoped credentials. Human approvals, protected quality recipes and production operations are enforced by the control plane. Worker mutations require runId and leaseToken."}
	case "ping":
	case "tools/list":
		result["tools"] = []any{
			p.M{"name": "blueprint_catalog", "description": "Read the administrator-installed application blueprint versions and exact asset digests. Selecting a blueprint is a proposal; human plan approval and independent validation remain required.", "inputSchema": p.M{"type": "object", "properties": p.M{"projectId": p.M{"type": "string"}}, "required": []string{"projectId"}, "additionalProperties": false}},
			p.M{"name": "delivery_metrics", "description": "Read version-scoped observed delivery metrics, sample counts and missing data. Human business judgments cannot be written by Agents.", "inputSchema": p.M{"type": "object", "properties": p.M{"projectId": p.M{"type": "string"}, "goalId": p.M{"type": "string"}, "revision": p.M{"type": "integer", "minimum": 1}}, "required": []string{"projectId"}, "additionalProperties": false}},
			p.M{"name": "acceptance_trace", "description": "Read exact requirement-to-artifact, independent evidence and release traceability. Historical records never count toward current acceptance.", "inputSchema": p.M{"type": "object", "properties": p.M{"projectId": p.M{"type": "string"}, "goalId": p.M{"type": "string"}, "revision": p.M{"type": "integer", "minimum": 1}}, "required": []string{"projectId", "goalId"}, "additionalProperties": false}},
			p.M{"name": "inspect_source_operation", "description": "Inspect an authorized interrupted source candidate and actual target branch. Returns no lease token. Human owners make recovery decisions separately.", "inputSchema": p.M{"type": "object", "properties": p.M{"id": p.M{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}},
			p.M{"name": "get_resource", "description": "Read one authorized goal, task, plan, artifact or release.", "inputSchema": p.M{"type": "object", "properties": p.M{"id": p.M{"type": "string"}}, "required": []string{"id"}, "additionalProperties": false}},
			p.M{"name": "project_workspace", "description": "Read authorized project context and current work state.", "inputSchema": p.M{"type": "object", "properties": p.M{"projectId": p.M{"type": "string"}}, "required": []string{"projectId"}, "additionalProperties": false}},
			p.M{"name": "platform_command", "description": "Submit a scoped change through the governed control plane. Returns authoritative state or a rejection.", "inputSchema": p.M{"type": "object", "properties": p.M{"command": p.M{"type": "string", "enum": agentCommands}, "input": p.M{"type": "object"}}, "required": []string{"command", "input"}, "additionalProperties": false}},
		}
	case "tools/call":
		args := p.Obj(request.Params["arguments"])
		var value any
		var e error
		switch p.S(request.Params, "name") {
		case "blueprint_catalog":
			value, e = h.Service.BlueprintCatalog(r.Context(), a, p.S(args, "projectId"))
		case "delivery_metrics":
			project, goal, rev, err := metricArguments(args)
			if err != nil {
				e = err
			} else {
				value, e = h.Service.DeliveryMetrics(r.Context(), a, project, goal, rev)
			}
		case "acceptance_trace":
			revision := 0
			if raw, present := args["revision"]; present {
				number, ok := raw.(float64)
				if !ok || number < 1 || number > 1e9 || math.Trunc(number) != number {
					e = &p.Fault{Code: "invalid", Message: "需求版本必须为正整数", Status: 400}
					break
				}
				revision = int(number)
			}
			value, e = h.Service.AcceptanceTrace(r.Context(), a, p.S(args, "projectId"), p.S(args, "goalId"), revision)
		case "inspect_source_operation":
			value, e = h.Service.InspectSourceOperation(r.Context(), a, p.S(args, "id"))
		case "get_resource":
			v, err := h.Service.GetResource(r.Context(), a, p.S(args, "id"))
			value = sanitize(v)
			e = err
		case "project_workspace":
			values, err := h.Service.Workspace(r.Context(), a, p.S(args, "projectId"))
			safe := []p.Resource{}
			for _, v := range values {
				safe = append(safe, sanitize(v))
			}
			value = safe
			e = err
		case "platform_command":
			name := p.S(args, "command")
			if !p.Has(agentCommands, name) {
				e = &p.Fault{Code: "tool_forbidden", Message: "此命令不向 Agent 工具开放", Status: 403}
			} else {
				v, err := h.Service.Command(r.Context(), a, name, p.Obj(args["input"]))
				value = sanitize(v)
				if name == "messages.claim" {
					value = v
				}
				e = err
			}
		default:
			response["error"] = p.M{"code": -32602, "message": "Unknown tool"}
			writeJSON(w, 200, response)
			return
		}
		if e != nil {
			result = p.M{"isError": true, "content": []any{p.M{"type": "text", "text": e.Error()}}}
		} else {
			body, _ := json.Marshal(value)
			result = p.M{"content": []any{p.M{"type": "text", "text": string(body)}}, "structuredContent": p.M{"resource": value}}
		}
	default:
		response["error"] = p.M{"code": -32601, "message": "Method not found"}
		writeJSON(w, 200, response)
		return
	}
	response["result"] = result
	writeJSON(w, 200, response)
}
