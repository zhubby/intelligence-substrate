//go:build ignore

// Explicitly invokes a paid provider using synthetic data and an isolated
// container Worker. Local acceptance approvals do not represent enterprise signoff.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/worker"
)

const buildRecipe = "node --check app.js && test -f index.html && tar -cf package.tar app.js index.html"

var browserSource = flag.String("browser", "", "operator-owned real-browser Python source; follow up a successful container acceptance")
var browserDockerHost = flag.String("browser-docker-host", "", "explicit local Docker unix socket for the protected browser pipeline")
var browserExecutable = flag.String("browser-executable", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "explicit installed browser executable")
var repairBrowser = flag.Bool("repair-browser", false, "allow one platform-created model repair within the original goal budget")
var retryBrowserRepair = flag.String("retry-browser-repair", "", "explicitly retry this existing browser defect's blocked repair within its original limit")
var ownerBrowserReplan = flag.String("owner-browser-replan", "", "local acceptance owner explicitly approves a replacement UI/build plan after this defect exhausted its repair limit")
var ownerQualityReplan = flag.String("owner-quality-replan", "", "local acceptance owner explicitly replans a feedback backend/build after an independently detected state-path failure")
var ownerCommandReplan = flag.String("owner-command-replan", "", "local acceptance owner takes over a budget-blocked feedback model repair with an explicitly approved fixed command; not model-generated repair")

func main() {
	state := flag.String("state", "", "new private evidence directory")
	configFile := flag.String("config", "", "operator-controlled container Worker config")
	mode := flag.String("mode", "", "feedback or booking")
	qaFile := flag.String("qa", "scripts/live-model-qa.py", "protected independent quality source")
	resume := flag.Bool("resume", false, "retry the original blocked code task in this owned acceptance database")
	replan := flag.Bool("replan", false, "propose and review backend/UI/build decomposition on the original goal and budget")
	flag.Parse()
	if *state == "" || *configFile == "" || (*mode != "feedback" && *mode != "booking") || (*resume && *replan) || (*browserSource != "" && (*resume || *replan)) || (*repairBrowser && *browserSource == "") || (*retryBrowserRepair != "" && !*repairBrowser) {
		fmt.Fprintln(os.Stderr, "-state, -config and -mode=feedback|booking are required")
		os.Exit(2)
	}
	if *ownerBrowserReplan != "" && (*browserSource == "" || *repairBrowser || *retryBrowserRepair != "" || *mode != "booking") {
		fmt.Fprintln(os.Stderr, "owner browser replan requires booking browser follow-up without automatic retries")
		os.Exit(2)
	}
	if *ownerQualityReplan != "" && (*browserSource == "" || *repairBrowser || *retryBrowserRepair != "" || *ownerBrowserReplan != "" || *mode != "feedback") {
		fmt.Fprintln(os.Stderr, "owner quality replan requires feedback browser follow-up without automatic retries")
		os.Exit(2)
	}
	if *ownerCommandReplan != "" && (*browserSource == "" || *repairBrowser || *retryBrowserRepair != "" || *ownerBrowserReplan != "" || *ownerQualityReplan != "" || *mode != "feedback") {
		fmt.Fprintln(os.Stderr, "owner command takeover requires feedback browser follow-up without model repair or automatic retries")
		os.Exit(2)
	}
	followup := *browserSource != ""
	if err := os.Mkdir(*state, 0700); err != nil && !((*resume || *replan || followup) && os.IsExist(err)) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report := p.M{"kind": "actual-model-container-delivery-acceptance", "mode": *mode, "startedAt": time.Now().UTC(), "approvalIdentity": "synthetic local acceptance owner", "productPlanComplete": false, "published": false, "nativeDeviceTested": false}
	if *resume || *replan || followup {
		previous, err := os.ReadFile(filepath.Join(*state, "result.json"))
		var before p.M
		if err != nil || json.Unmarshal(previous, &before) != nil || p.S(before, "kind") != "actual-model-container-delivery-acceptance" || p.S(before, "mode") != *mode || (p.B(before, "passed") != followup && !((*ownerQualityReplan != "" || *ownerCommandReplan != "") && followup && before["artifact"] != nil)) {
			fmt.Fprintln(os.Stderr, "resume requires this tool's failed acceptance for the same mode")
			os.Exit(1)
		}
		archive := "previous-attempt-" + time.Now().UTC().Format("20060102T150405.000000000") + ".json"
		if err := os.WriteFile(filepath.Join(*state, archive), previous, 0600); err != nil {
			panic(err)
		}
		for _, key := range []string{"projectId", "goalId", "repositoryId", "proposedPlan", "image"} {
			report[key] = before[key]
		}
		if followup {
			report["kind"] = "actual-model-browser-repair-acceptance"
			report["originalArtifact"] = before["artifact"]
			report["releaseApproved"] = false
		}
		report["resumedFrom"] = archive
		report["previousAttemptError"] = before["error"]
	}
	err := verify(*state, *configFile, *mode, *qaFile, *resume, *replan, report)
	report["passed"], report["finishedAt"] = err == nil, time.Now().UTC()
	if err != nil {
		report["error"] = err.Error()
	}
	body, _ := json.MarshalIndent(report, "", "  ")
	resultName := "result.json"
	if followup {
		resultName = "browser-result-" + time.Now().UTC().Format("20060102T150405.000000000") + ".json"
	}
	if writeErr := os.WriteFile(filepath.Join(*state, resultName), append(body, '\n'), 0600); writeErr != nil {
		panic(writeErr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if followup {
		fmt.Println("Actual generated artifact browser follow-up passed:", filepath.Join(*state, resultName))
	} else {
		fmt.Println("Actual model planning, container development, pinned build and independent business QA passed:", *state)
	}
}

func verify(state, configFile, mode, qaFile string, resume, replan bool, report p.M) error {
	configBody, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	var config worker.Config
	if err = json.Unmarshal(configBody, &config); err != nil {
		return err
	}
	if config.Driver != "container" || config.Image == "" || config.AllowNative {
		return fmt.Errorf("an explicit isolated container configuration is required")
	}
	qa, err := os.ReadFile(qaFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	store, err := p.Open(filepath.Join(state, "platform.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	svc := p.NewService(store)
	svc.Root = state
	h := httpapi.New(svc, false)
	pass := p.Secret()
	email := "model-proof@example.test"
	if resume || replan || *browserSource != "" {
		email = "model-retry-" + p.NewID("") + "@example.test"
		if _, err = h.Auth.CreateIdentity(email, "Local acceptance retry owner", pass, true); err != nil {
			return err
		}
	} else {
		if err = h.Auth.Bootstrap(email, pass); err != nil {
			return err
		}
	}
	sessionToken, session, err := h.Auth.Login(email, pass)
	if err != nil {
		return err
	}
	model := integrations.ModelFromEnv(svc)
	if model == nil {
		return fmt.Errorf("explicit model endpoint, name, prices and policy configuration required")
	}
	diagnostics := &providerDiagnostics{base: http.DefaultTransport}
	model.Client.Transport = diagnostics
	defer func() {
		diagnostics.mu.Lock()
		defer diagnostics.mu.Unlock()
		report["providerFailures"] = diagnostics.failures
	}()
	report["modelName"], report["providerBaseURL"], report["minIntervalSeconds"], report["defaultMaxTokens"] = model.Name, model.BaseURL, model.MinInterval.Seconds(), model.DefaultMaxTokens
	report["providerOptions"] = model.Options
	h.ModelCall, h.GeneratePlan = model.Chat, model.GeneratePlan
	server := httptest.NewServer(h)
	defer server.Close()
	command := func(name string, input p.M) (p.Resource, error) {
		body, _ := json.Marshal(input)
		req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/commands/"+name, bytes.NewReader(body))
		if err != nil {
			return p.Resource{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-CSRF-Token", session.CSRF)
		req.AddCookie(&http.Cookie{Name: "is_session", Value: sessionToken})
		res, err := server.Client().Do(req)
		if err != nil {
			return p.Resource{}, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			var failure p.M
			_ = json.NewDecoder(res.Body).Decode(&failure)
			return p.Resource{}, fmt.Errorf("%s HTTP %d: %v", name, res.StatusCode, failure)
		}
		var out p.Resource
		err = json.NewDecoder(res.Body).Decode(&out)
		return out, err
	}
	spec, criteria := businessSpec(mode)
	var project, goal, repository p.Resource
	if resume || replan || *browserSource != "" {
		if p.S(report, "image") != config.Image {
			return fmt.Errorf("resume cannot change the reviewed tool image")
		}
		project, err = store.Get(p.S(report, "projectId"))
		if err != nil {
			return err
		}
		goal, err = store.Get(p.S(report, "goalId"))
		if err != nil {
			return err
		}
		repository, err = store.Get(p.S(report, "repositoryId"))
		if err != nil {
			return err
		}
		if goal.ProjectID != project.ID || repository.ProjectID != project.ID {
			return fmt.Errorf("resume resource binding mismatch")
		}
	} else {
		project, err = command("projects.create", p.M{"title": "Real model " + mode, "allowExternalModels": true})
		if err != nil {
			return err
		}
		goal, err = command("goals.create", p.M{"projectId": project.ID, "title": "Generated " + mode + " Web/API", "outcome": spec, "criteria": criteria, "targets": []any{"web"}, "budget": 5})
		if err != nil {
			return err
		}
		report["projectId"], report["goalId"], report["image"] = project.ID, goal.ID, config.Image
		if _, err = command("goals.approve", p.M{"id": goal.ID}); err != nil {
			return err
		}
		repository, err = command("repositories.create", p.M{"projectId": project.ID, "title": mode + "-source"})
		if err != nil {
			return err
		}
		report["repositoryId"] = repository.ID
	}
	previousRuns, err := store.List(project.ID, "run", goal.ID)
	if err != nil {
		return err
	}
	oldRuns := map[string]bool{}
	for _, run := range previousRuns {
		if run.Status == "running" {
			return fmt.Errorf("existing execution remains live; do not retry")
		}
		oldRuns[run.ID] = true
	}
	previousCharges, err := store.List(project.ID, "charge", goal.ID)
	if err != nil {
		return err
	}
	oldCharges := map[string]bool{}
	for _, charge := range previousCharges {
		oldCharges[charge.ID] = true
	}
	runner := func(role string, caps []any, quality bool) (*worker.Runner, error) {
		registered, err := command("workers.register", p.M{"projectId": project.ID, "title": role + " isolated Node/Python", "runtime": "linux", "roles": []any{role}, "capabilities": caps, "trustedQuality": quality})
		if err != nil {
			return nil, err
		}
		token, _, err := h.Auth.IssueToken(ctx, session.Actor, p.M{"projectId": project.ID, "workerId": registered.ID})
		if err != nil {
			return nil, err
		}
		client, err := worker.NewClient(server.URL, token, true)
		if err != nil {
			return nil, err
		}
		cfg := config
		cfg.StateDir = filepath.Join(state, role)
		return &worker.Runner{Client: client, Config: cfg}, nil
	}
	dev, err := runner("developer", []any{"code", "build"}, false)
	if err != nil {
		return err
	}
	quality, err := runner("quality", []any{"test"}, true)
	if err != nil {
		return err
	}
	if *browserSource != "" {
		return verifyBrowserFollowup(ctx, state, mode, qa, config, store, svc, h, session.Actor, server.URL, command, dev, quality, report)
	}
	if resume {
		tasks, err := store.List(project.ID, "task", goal.ID)
		if err != nil {
			return err
		}
		active := []p.Resource{}
		for _, task := range tasks {
			if p.S(task.Data, "planId") == p.S(goal.Data, "activePlanId") {
				active = append(active, task)
			}
		}
		if len(active) < 2 || len(active) > 3 {
			return fmt.Errorf("resume requires the reviewed active code/build plan")
		}
		retried := false
		for _, task := range active {
			if p.S(task.Data, "kind") == "code" && task.Status == "blocked" {
				if _, err = command("tasks.retry", p.M{"id": task.ID, "reason": "explicit local owner retry after provider diagnostics and bounded configuration review"}); err != nil {
					return err
				}
				report["retriedTaskId"], retried = task.ID, true
			} else if (p.S(task.Data, "kind") != "build" && p.S(task.Data, "kind") != "code") || task.Status != "pending" {
				return fmt.Errorf("resume found a changed task state")
			}
		}
		if !retried {
			return fmt.Errorf("no original blocked code task to retry")
		}
	} else {
		recipe := "For this acceptance create exactly two dependent Linux tasks, one kind=code with no command (the real Agent must write the application), then kind=build."
		if replan {
			recipe = "Earlier planning or development failed with recorded incomplete output or provider errors. Replan this same approved goal without changing its scope, budget or quality standard. Create exactly THREE dependent Linux tasks: kind=code id=backend implements ONLY app.js and never writes index.html; kind=code id=ui dependsOn=[backend] implements ONLY index.html using that exact backend API; kind=build dependsOn=[ui] packages both files. Code tasks have NO command and no sourceMode: the real Agent must write source in separate bounded steps. Do not start a combined whole-application code task. Keep source compact and dependency-free. Each code task must write its one complete file, run at most one short syntax check, and then finish. Do NOT start a server, spawn background jobs, write test files or run business/restart/concurrency tests during code tasks; the protected independent QA performs those actual checks after build. Include these phase constraints explicitly in each code task instructions. This is a linear source chain: OMIT sourceFrom entirely on every task; NEVER emit empty sourceFrom arrays."
			report["replannedOriginalGoal"], report["priorTaskRetryLimitPreserved"] = true, true
		}
		if replan && p.S(report, "previousAttemptError") != "" {
			recipe += " The previous proposal/execution was rejected with this diagnostic; correct the proposal while preserving the baseline: " + p.S(report, "previousAttemptError")
		}
		if replan {
			current, err := store.Get(goal.ID)
			if err != nil {
				return err
			}
			remaining := int64(p.N(current.Data, "budgetCents") - p.N(current.Data, "spentCents"))
			if remaining < 100 {
				return fmt.Errorf("remaining original budget is insufficient for another bounded full-app plan")
			}
			recipe += fmt.Sprintf(" The original goal still has its fixed total budget of %d cents, with %d cents already spent/held and %d cents remaining. Each code execution must explicitly have budgetCents=95, and build budgetCents=20; these are execution ceilings for sequential work, never an increase to the total goal budget. Keep code tasks focused and short so actual model costs fit the remaining goal budget.", int64(p.N(current.Data, "budgetCents")), int64(p.N(current.Data, "spentCents")), remaining)
		}
		knowledgeCommand := "knowledge.create"
		knowledgeInput := p.M{"projectId": project.ID, "title": "Verified Node/Python Web recipe", "source": "operator:live-model-acceptance-v1", "content": recipe + " All tasks use repositoryId=" + repository.ID + ". Together the code tasks implement the approved goal using only Node standard libraries in app.js plus a standalone index.html with inline CSS/JavaScript, no dependencies, external resources or npm. Use a non-main branch. The build command must be exactly [\"/bin/sh\",\"-c\",\"" + buildRecipe + "\"], outputPath=package.tar, target=web. Node and Python are available in the isolated container; Go/Flutter are not. Independent protected business QA is scheduled by the owner after the artifact exists, not by fabricated test tasks. Do not add manual or publication tasks to this bounded Web acceptance; other platform delivery remains outside this acceptance. Include detailed code instructions and preserve the full business baseline."}
		if replan {
			known, listErr := store.List(project.ID, "knowledge", "")
			if listErr != nil {
				return listErr
			}
			for _, item := range known {
				if item.Status != "superseded" && p.S(item.Data, "source") == "operator:live-model-acceptance-v1" {
					knowledgeCommand = "knowledge.revise"
					knowledgeInput["id"], knowledgeInput["reason"] = item.ID, "Explicit backend/UI decomposition after recorded provider failures"
					break
				}
			}
		}
		_, err = command(knowledgeCommand, knowledgeInput)
		if err != nil {
			return err
		}
		fmt.Println("Generating an actual model plan for", mode)
		plan, err := model.GeneratePlan(ctx, session.Actor, p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "key": p.NewID("plan_request")})
		if err != nil {
			return err
		}
		report["proposedPlan"] = plan
		tasks := p.Items(plan.Data["tasks"])
		expectedCount := 2
		if replan {
			expectedCount = 3
		}
		if len(tasks) != expectedCount {
			return fmt.Errorf("model proposal needs review: expected %d bounded tasks", expectedCount)
		}
		var producer, build p.M
		codeTasks := map[string]p.M{}
		for _, value := range tasks {
			task := p.Obj(value)
			if p.S(task, "runtime") != "linux" || p.S(task, "repositoryId") != repository.ID || p.N(task, "timeoutSeconds") > 900 || p.N(task, "budgetCents") > 500 {
				return fmt.Errorf("model plan exceeds declared environment or budget")
			}
			switch p.S(task, "kind") {
			case "code":
				producer = task
				codeTasks[p.S(task, "id")] = task
				if p.S(task, "instructions") == "" || len(p.Items(task["command"])) != 0 || p.S(task, "sourceMode") != "" {
					return fmt.Errorf("code task bypasses bounded model execution")
				}
				if replan && p.N(task, "budgetCents") != 95 {
					return fmt.Errorf("replanned code task does not preserve the reviewed 95-cent execution ceiling")
				}
			case "build":
				build = task
				if replan && p.N(task, "budgetCents") != 20 {
					return fmt.Errorf("replanned build does not preserve the reviewed 20-cent execution ceiling")
				}
			default:
				return fmt.Errorf("model proposed an unreviewed task kind")
			}
		}
		if replan {
			backend, ui := codeTasks["backend"], codeTasks["ui"]
			if backend == nil || ui == nil || len(p.Items(backend["dependsOn"])) != 0 || !reflect.DeepEqual(p.Strings(ui["dependsOn"]), []string{"backend"}) || !reflect.DeepEqual(p.Strings(build["dependsOn"]), []string{"ui"}) {
				return fmt.Errorf("replanned model proposal does not preserve backend -> UI -> build")
			}
			producer = ui
		}
		if producer == nil || build == nil || p.S(producer, "instructions") == "" || len(p.Items(producer["command"])) != 0 || !reflect.DeepEqual(p.Strings(build["command"]), []string{"/bin/sh", "-c", buildRecipe}) || p.S(build, "outputPath") != "package.tar" || p.S(build, "target") != "web" || !p.Has(p.Strings(build["dependsOn"]), p.S(producer, "id")) {
			return fmt.Errorf("model plan does not match the explicitly reviewed recipe")
		}
		if _, err = command("plans.approve", p.M{"id": plan.ID}); err != nil {
			return err
		}
		report["planApprovedByLocalAcceptanceOwner"] = true
	}
	cycles := 2
	if replan {
		cycles = 3
	}
	if resume {
		active, err := store.List(project.ID, "task", goal.ID)
		if err != nil {
			return err
		}
		cycles = 0
		for _, task := range active {
			if p.S(task.Data, "planId") == p.S(goal.Data, "activePlanId") && task.Status == "pending" {
				cycles++
			}
		}
	}
	for cycle := 0; cycle < cycles; cycle++ {
		fmt.Println("Running actual model/container developer cycle", cycle+1, "for", mode)
		if worked, err := dev.RunOnce(ctx); err != nil || !worked {
			return fmt.Errorf("developer cycle %d worked=%v: %v", cycle+1, worked, err)
		}
		runs, err := store.List(project.ID, "run", goal.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if !oldRuns[run.ID] && run.Status != "succeeded" {
				report["failedRuns"] = runs
				return fmt.Errorf("actual %s execution failed; inspect persisted run and charge evidence", p.S(p.Obj(run.Data["task"]), "kind"))
			}
		}
	}
	artifacts, err := store.List(project.ID, "artifact", goal.ID)
	if err != nil || len(artifacts) != 1 {
		return fmt.Errorf("no unique actual package: %v", err)
	}
	artifact := artifacts[0]
	if err = svc.VerifyArtifact(artifact); err != nil {
		return err
	}
	report["artifact"] = artifact
	pipeline, err := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent " + mode + " business contract", "runtime": "linux", "method": "automated", "verificationEnvironment": "ci", "command": []any{"python3", "-c", string(qa), mode}})
	if err != nil {
		return err
	}
	ids := []any{}
	for _, criterion := range criteria {
		ids = append(ids, p.S(p.Obj(criterion), "id"))
	}
	if _, err = command("pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifact.ID, "criteria": ids}); err != nil {
		return err
	}
	fmt.Println("Running independent container business checks for", mode)
	if worked, err := quality.RunOnce(ctx); err != nil || !worked {
		return fmt.Errorf("quality worked=%v: %v", worked, err)
	}
	evidence, err := store.List(project.ID, "evidence", goal.ID)
	if err != nil || len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "commit") != p.S(artifact.Data, "commit") {
		report["qualityEvidence"] = evidence
		return fmt.Errorf("independent business quality did not pass on the actual artifact")
	}
	report["qualityEvidence"] = evidence
	_, releaseErr := command("releases.create", p.M{"goalId": goal.ID, "version": "0.1.0", "commit": p.S(artifact.Data, "commit"), "artifactIds": []any{artifact.ID}})
	if releaseErr == nil || !strings.Contains(releaseErr.Error(), "目标环境") {
		return fmt.Errorf("automated-only evidence bypassed the browser gate: %v", releaseErr)
	}
	report["browserGateRejectedRelease"] = true
	charges, err := store.List(project.ID, "charge", goal.ID)
	if err != nil || len(charges) < 3 {
		return fmt.Errorf("planning and development lack real provider usage")
	}
	uncertainCharges := []any{}
	for _, charge := range charges {
		if !oldCharges[charge.ID] && (charge.Status != "settled" || p.S(charge.Data, "providerResponseId") == "" || p.S(charge.Data, "outcome") != "completed") {
			return fmt.Errorf("model charge requires reconciliation")
		}
		if charge.Status != "settled" {
			uncertainCharges = append(uncertainCharges, charge.ID)
		}
	}
	report["historicalChargesAwaitingReconciliation"], report["financialReconciliationComplete"] = uncertainCharges, len(uncertainCharges) == 0
	report["charges"] = charges
	runs, err := store.List(project.ID, "run", goal.ID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		delete(run.Data, "leaseToken")
	}
	report["runs"] = runs
	report["modelName"], report["providerBaseURL"] = model.Name, model.BaseURL
	report["priceSource"] = "explicit configured acceptance estimate; not a provider invoice"
	return nil
}

func verifyBrowserFollowup(ctx context.Context, state, mode string, qa []byte, config worker.Config, store *p.Store, svc *p.Service, h *httpapi.Handler, actor p.Actor, baseURL string, command func(string, p.M) (p.Resource, error), dev, quality *worker.Runner, report p.M) error {
	if !strings.HasPrefix(*browserDockerHost, "unix:///") || !filepath.IsAbs(*browserExecutable) {
		return fmt.Errorf("browser follow-up requires an explicit local Docker socket and browser executable")
	}
	code, err := os.ReadFile(*browserSource)
	if err != nil {
		return err
	}
	projectID, goalID := p.S(report, "projectId"), p.S(report, "goalId")
	artifact, err := store.Get(p.S(p.Obj(report["originalArtifact"]), "id"))
	if err != nil || artifact.ProjectID != projectID || artifact.GoalID != goalID {
		return fmt.Errorf("browser source artifact binding invalid: %v", err)
	}
	if err = svc.VerifyArtifact(artifact); err != nil {
		return err
	}
	defer func() {
		for _, kind := range []string{"run", "charge", "defect", "evidence"} {
			items, listErr := store.List(projectID, kind, goalID)
			if listErr != nil {
				continue
			}
			for _, item := range items {
				delete(item.Data, "leaseToken")
			}
			report[kind+"s"] = items
		}
		if goal, getErr := store.Get(goalID); getErr == nil {
			report["goalSpentCents"], report["goalBudgetCents"] = goal.Data["spentCents"], goal.Data["budgetCents"]
		}
		report["financialReconciliationComplete"] = false
		report["priceSource"] = "explicit configured acceptance estimate; not a provider invoice"
	}()
	registered, err := command("workers.register", p.M{"projectId": projectID, "title": "Independent actual browser host", "runtime": runtime.GOOS, "roles": []any{"quality"}, "capabilities": []any{"test"}, "trustedQuality": true})
	if err != nil {
		return err
	}
	token, _, err := h.Auth.IssueToken(ctx, actor, p.M{"projectId": projectID, "workerId": registered.ID})
	if err != nil {
		return err
	}
	client, err := worker.NewClient(baseURL, token, true)
	if err != nil {
		return err
	}
	browser := &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(state, "browser-worker")}}
	criteria := []any{"F1", "F3"}
	if mode == "booking" {
		criteria = []any{"B1", "B4", "B5"}
	}
	expectedCommand := []any{"python3", "-c", string(code), "--mode", mode, "--image", config.Image, "--docker-host", *browserDockerHost, "--executable", *browserExecutable, "--evidence-base", filepath.Join(state, "browser-evidence")}
	runTask := func(controller *worker.Runner, taskID string) (p.Resource, error) {
		before, listErr := store.List(projectID, "run", goalID)
		if listErr != nil {
			return p.Resource{}, listErr
		}
		seen := map[string]bool{}
		for _, run := range before {
			seen[run.ID] = true
		}
		if worked, runErr := controller.RunOnce(ctx); runErr != nil || !worked {
			return p.Resource{}, fmt.Errorf("protected cycle worked=%v: %v", worked, runErr)
		}
		runs, listErr := store.List(projectID, "run", goalID)
		if listErr != nil {
			return p.Resource{}, listErr
		}
		for _, run := range runs {
			if !seen[run.ID] && p.S(run.Data, "taskId") == taskID {
				return run, nil
			}
		}
		return p.Resource{}, fmt.Errorf("no actual execution for %s", taskID)
	}
	if *ownerBrowserReplan != "" || *ownerQualityReplan != "" {
		defectID := *ownerBrowserReplan
		if *ownerQualityReplan != "" {
			defectID = *ownerQualityReplan
		}
		defect, err := store.Get(defectID)
		if err != nil || defect.Kind != "defect" || defect.ProjectID != projectID || defect.GoalID != goalID || !p.Has([]string{"blocked", "repairing"}, defect.Status) || p.S(defect.Data, "artifactId") != artifact.ID {
			return fmt.Errorf("owner replan does not match the blocked original browser defect")
		}
		oldRepair, err := store.Get(p.S(defect.Data, "repairTaskId"))
		if err != nil {
			return err
		}
		if *ownerQualityReplan != "" && oldRepair.Status == "pending" {
			stopped, err := runTask(dev, oldRepair.ID)
			if err != nil || stopped.Status != "blocked" || !strings.Contains(p.S(p.Obj(stopped.Data["result"]), "reason"), "预算") {
				return fmt.Errorf("quality owner replan requires the actual original budget stop: %v", err)
			}
			report["originalAutomaticRepairBudgetStop"] = stopped.ID
			oldRepair, err = store.Get(oldRepair.ID)
			if err != nil {
				return err
			}
		}
		if oldRepair.Status != "blocked" || (*ownerBrowserReplan != "" && p.N(oldRepair.Data, "attempts") < p.N(oldRepair.Data, "maxAttempts")) {
			return fmt.Errorf("owner replan requires the preserved, exhausted automatic repair")
		}
		goal, err := store.Get(goalID)
		if err != nil {
			return err
		}
		remaining := int64(p.N(goal.Data, "budgetCents") - p.N(goal.Data, "spentCents"))
		if remaining < 80 {
			return fmt.Errorf("insufficient remaining original budget for the reviewed UI replacement")
		}
		instructions := "Repair ONLY index.html. The protected real browser failed because reserve(1) / reserve(2) sends numeric IDs while the exact backend slots are slot-1 / slot-2. Use these string slot IDs. Remove readonly from booking-token so users can paste saved credentials after reload. Preserve all approved Chinese UI, pending duplicate prevention, plain text rendering, network errors and refresh behavior. Read only index.html; app.js passed independent API QA and must remain unchanged. Make the complete focused UI repair, then finish promptly. Do not start a server, write test files, or run a self-test loop. The unchanged protected browser and API pipelines validate the new artifact. Do not change any requirement, gate, total goal budget or old failure/attempt record."
		codeBudget := remaining * 3 / 4
		buildBudget := remaining - codeBudget
		if *ownerQualityReplan != "" {
			instructions = "Repair ONLY app.js. The independent protected API test failed on the valid feedback POST: the server closed the connection while saving. Source review identified STATE_PATH = path.join(process.cwd(), STATE_FILE); when STATE_FILE is absolute, path.join incorrectly nests it under cwd instead of using the configured writable path. Honor both absolute and relative STATE_FILE (for example path.resolve(STATE_FILE)), retaining atomic write/rename and restart persistence. Inspect only app.js, preserve index.html exactly and preserve all other approved validation and API behavior. Make the focused fix then finish promptly; no server, background jobs, generated test files or self-test loop. The unchanged protected API and real-browser pipelines validate the rebuilt artifact. Never alter the baseline, quality gate, total goal budget, failed defect or attempt history."
			codeBudget, buildBudget = remaining-20, 20
		}
		plan, err := command("plans.propose", p.M{"goalId": goalID, "expectedRequirementRevision": goal.Revision, "summary": "Local acceptance owner: focused " + mode + " source repair and rebuild after the recorded automatic repair stop", "risk": "Explicit owner replan, not autonomous recovery; retain the blocked original defect and all costs", "tasks": []any{
			p.M{"id": "source_repair", "title": "Owner-approved focused " + mode + " repair", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": p.S(artifact.Data, "repositoryId"), "commit": p.S(artifact.Data, "commit"), "branch": "repair-browser/" + goalID + "/owner-1", "instructions": instructions, "budgetCents": codeBudget, "timeoutSeconds": 900},
			p.M{"id": "repackage", "title": "Build the reviewed " + mode + " repair", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": p.S(artifact.Data, "repositoryId"), "dependsOn": []any{"source_repair"}, "command": []any{"/bin/sh", "-c", buildRecipe}, "outputPath": "package.tar", "target": "web", "budgetCents": buildBudget, "timeoutSeconds": 900},
		}})
		if err != nil {
			return err
		}
		if _, err = command("plans.approve", p.M{"id": plan.ID}); err != nil {
			return err
		}
		report["ownerApprovedReplacementPlan"], report["blockedOriginalDefectPreserved"], report["automaticRepairCompleted"] = plan, defect.ID, false
		pending, err := store.List(projectID, "task", goalID)
		if err != nil {
			return err
		}
		ids := map[string]string{}
		for _, task := range pending {
			if p.S(task.Data, "planId") == plan.ID {
				ids[p.S(task.Data, "logicalId")] = task.ID
			}
		}
		fmt.Println("Running explicitly owner-replanned source repair within the original goal budget for", mode)
		for _, logical := range []string{"source_repair", "repackage"} {
			run, err := runTask(dev, ids[logical])
			if err != nil || run.Status != "succeeded" {
				return fmt.Errorf("owner-replanned %s did not succeed: %v", logical, err)
			}
			if logical == "repackage" {
				artifact, err = store.Get(p.S(p.Obj(run.Data["result"]), "artifactId"))
				if err != nil {
					return err
				}
			}
		}
	}
	if *ownerCommandReplan != "" {
		defect, err := store.Get(*ownerCommandReplan)
		if err != nil || defect.Kind != "defect" || defect.ProjectID != projectID || defect.GoalID != goalID || defect.Status != "blocked" || p.S(defect.Data, "artifactId") != artifact.ID {
			return fmt.Errorf("command takeover requires the preserved original feedback defect")
		}
		goal, err := store.Get(goalID)
		if err != nil {
			return err
		}
		runs, err := store.List(projectID, "run", goalID)
		if err != nil {
			return err
		}
		var stopped p.Resource
		for _, run := range runs {
			task := p.Obj(run.Data["task"])
			if run.Status == "blocked" && p.S(run.Data, "planId") == p.S(goal.Data, "activePlanId") && p.S(task, "logicalId") == "source_repair" && p.S(task, "commit") == p.S(artifact.Data, "commit") && strings.Contains(p.S(p.Obj(run.Data["result"]), "reason"), "预算") {
				stopped = run
			}
		}
		if stopped.ID == "" {
			return fmt.Errorf("command takeover requires the actual current model repair budget stop")
		}
		remaining := int64(p.N(goal.Data, "budgetCents") - p.N(goal.Data, "spentCents"))
		if remaining < 40 {
			return fmt.Errorf("insufficient original budget for fixed-command takeover and rebuild")
		}
		// This is an operator-authored, approved source change, not a model
		// response or an edit to the artifact/independent quality policy.
		fix := "from pathlib import Path\np=Path('app.js')\nb=p.read_bytes()\nold=b'const STATE_PATH = path.join(process.cwd(), STATE_FILE);'\nnew=b'const STATE_PATH = path.resolve(STATE_FILE);'\nassert b.count(old)==1, 'reviewed state-path expression changed'\np.write_bytes(b.replace(old,new))\n"
		plan, err := command("plans.propose", p.M{"goalId": goalID, "expectedRequirementRevision": goal.Revision, "summary": "Local acceptance owner: fixed-command feedback path repair and rebuild after the actual model budget stop", "risk": "Manual engineering takeover, not autonomous or model-generated repair; preserve all failed attempts, costs and the original defect", "tasks": []any{
			p.M{"id": "owner_fixed_repair", "title": "Owner-authored single-expression feedback path repair", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": p.S(artifact.Data, "repositoryId"), "commit": p.S(artifact.Data, "commit"), "branch": "repair-quality/" + goalID + "/owner-command-1", "instructions": "Human takeover: execute only the reviewed fixed command; preserve index.html and all other source bytes. Independent QA and browser gates remain required.", "command": []any{"python3", "-c", fix}, "budgetCents": remaining - 20, "timeoutSeconds": 900},
			p.M{"id": "owner_repackage", "title": "Build the manually repaired feedback source", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": p.S(artifact.Data, "repositoryId"), "dependsOn": []any{"owner_fixed_repair"}, "command": []any{"/bin/sh", "-c", buildRecipe}, "outputPath": "package.tar", "target": "web", "budgetCents": 20, "timeoutSeconds": 900},
		}})
		if err != nil {
			return err
		}
		if _, err = command("plans.approve", p.M{"id": plan.ID}); err != nil {
			return err
		}
		report["ownerApprovedReplacementPlan"], report["blockedOriginalDefectPreserved"], report["automaticRepairCompleted"] = plan, defect.ID, false
		report["manualSourceTakeover"], report["finalRepairUsedModel"], report["priorModelRepairBudgetStop"] = true, false, stopped.ID
		pending, err := store.List(projectID, "task", goalID)
		if err != nil {
			return err
		}
		ids := map[string]string{}
		for _, task := range pending {
			if p.S(task.Data, "planId") == plan.ID {
				ids[p.S(task.Data, "logicalId")] = task.ID
			}
		}
		chargesBefore, err := store.List(projectID, "charge", goalID)
		if err != nil {
			return err
		}
		fmt.Println("Running explicitly owner-authored fixed-command takeover; no model repair call for", mode)
		for _, logical := range []string{"owner_fixed_repair", "owner_repackage"} {
			run, err := runTask(dev, ids[logical])
			if err != nil || run.Status != "succeeded" {
				return fmt.Errorf("fixed-command %s did not succeed: %v", logical, err)
			}
			if logical == "owner_repackage" {
				artifact, err = store.Get(p.S(p.Obj(run.Data["result"]), "artifactId"))
				if err != nil {
					return err
				}
			}
		}
		chargesAfter, err := store.List(projectID, "charge", goalID)
		if err != nil {
			return err
		}
		if len(chargesAfter) != len(chargesBefore) {
			return fmt.Errorf("fixed-command takeover unexpectedly called the model")
		}
		report["takeoverModelCalls"] = 0
	}
	apiVerifiedBeforeBrowser := ""
	if *ownerQualityReplan != "" || *ownerCommandReplan != "" {
		apiPipeline, err := command("pipelines.create", p.M{"projectId": projectID, "title": "Unchanged independent feedback contract after owner repair", "runtime": "linux", "method": "automated", "verificationEnvironment": "ci", "command": []any{"python3", "-c", string(qa), mode}})
		if err != nil {
			return err
		}
		apiTask, err := command("pipelines.run", p.M{"pipelineId": apiPipeline.ID, "artifactId": artifact.ID, "criteria": []any{"F1", "F2", "F3"}})
		if err != nil {
			return err
		}
		checked, err := runTask(quality, apiTask.ID)
		if err != nil || checked.Status != "succeeded" {
			return fmt.Errorf("independent API contract failed after owner source repair: %v", err)
		}
		_, releaseErr := command("releases.create", p.M{"goalId": goalID, "version": "0.1.0", "commit": p.S(artifact.Data, "commit"), "artifactIds": []any{artifact.ID}})
		if releaseErr == nil || !strings.Contains(releaseErr.Error(), "目标环境") {
			return fmt.Errorf("API-only owner repair bypassed the browser gate: %v", releaseErr)
		}
		report["browserGateRejectedRelease"] = true
		apiVerifiedBeforeBrowser = artifact.ID
	}
	var checked p.Resource
	if *retryBrowserRepair != "" {
		defect, err := store.Get(*retryBrowserRepair)
		if err != nil || defect.Kind != "defect" || defect.ProjectID != projectID || defect.GoalID != goalID || defect.Status != "blocked" || p.S(defect.Data, "artifactId") != artifact.ID {
			return fmt.Errorf("explicit browser retry does not match the blocked original defect")
		}
		failedTask, err := store.Get(p.S(defect.Data, "testTaskId"))
		if err != nil {
			return err
		}
		pipeline, err := store.Get(p.S(failedTask.Data, "pipelineId"))
		if err != nil || p.S(pipeline.Data, "verificationEnvironment") != "browser" || !reflect.DeepEqual(p.Strings(pipeline.Data["command"]), p.Strings(expectedCommand)) {
			return fmt.Errorf("explicit browser retry requires the unchanged protected browser pipeline")
		}
		checked, err = store.Get(p.S(defect.Data, "failedRunId"))
		if err != nil || checked.Status != "failed" {
			return fmt.Errorf("original independent browser failure is missing")
		}
		if _, err = command("tasks.retry", p.M{"id": p.S(defect.Data, "repairTaskId"), "reason": "Explicit local acceptance owner retry after reducing repeated coordination context; original scope, task budget and retry ceiling preserved"}); err != nil {
			return err
		}
		report["retriedOriginalBrowserDefect"] = defect.ID
	} else {
		pipeline, err := command("pipelines.create", p.M{"projectId": projectID, "title": "Actual " + mode + " browser user journeys", "runtime": runtime.GOOS, "method": "automated", "verificationEnvironment": "browser", "command": expectedCommand})
		if err != nil {
			return err
		}
		first, err := command("pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifact.ID, "criteria": criteria})
		if err != nil {
			return err
		}
		fmt.Println("Running protected native browser pipeline on the actual generated artifact for", mode)
		checked, err = runTask(browser, first.ID)
		if err != nil {
			return err
		}
	}
	report["initialBrowserRunId"] = checked.ID
	if checked.Status != "succeeded" {
		report["initialBrowserFailurePreserved"] = true
		if !*repairBrowser {
			return fmt.Errorf("actual browser validation failed; original artifact and failed run preserved")
		}
		defects, listErr := store.List(projectID, "defect", goalID)
		if listErr != nil {
			return listErr
		}
		var defect p.Resource
		for _, item := range defects {
			if p.S(item.Data, "failedRunId") == checked.ID {
				defect = item
			}
		}
		if (defect.Status != "repairing" && !(defect.Status == "blocked" && defect.ID == *retryBrowserRepair)) || p.S(defect.Data, "repairTaskId") == "" {
			return fmt.Errorf("browser failure has no eligible bounded automatic repair")
		}
		report["repairDefectId"] = defect.ID
		knowledgeCommand := "knowledge.create"
		knowledge := p.M{"projectId": projectID, "title": "Actual browser failure and focused repair constraints", "source": "operator:actual-browser-source-review-v1", "content": "The protected native browser pipeline has failed on the actual generated artifact; its real log is attached to the defect. Preserve the approved scope, total budget and all QA. Inspect ONLY index.html and repair its UI integration; the unchanged app.js already passed independent API QA and its contract is in the goal, so do not read or modify app.js. Source review found reserve(1) / reserve(2) sends numeric IDs whereas APP_SLOTS and backend use exact strings slot-1 / slot-2. The booking-token input currently has readonly: let users paste a saved credential after page reload to cancel; do not expose it to other clients or persist plaintext on the server. Make minimal focused edits (a short targeted tool command is suitable), then finish promptly without another self-test or inspection loop. No server, background jobs or generated test files. The unchanged protected build, browser and API QA independently validate the repaired source. Never claim verification you did not execute."}
		known, err := store.List(projectID, "knowledge", "")
		if err != nil {
			return err
		}
		for _, item := range known {
			if item.Status != "superseded" && p.S(item.Data, "source") == p.S(knowledge, "source") {
				knowledgeCommand = "knowledge.revise"
				knowledge["id"], knowledge["reason"] = item.ID, "Bounded retry after recorded context-budget stop"
			}
		}
		_, err = command(knowledgeCommand, knowledge)
		if err != nil {
			return err
		}
		fmt.Println("Running platform-created bounded model repair for", mode)
		repaired, err := runTask(dev, p.S(defect.Data, "repairTaskId"))
		if err != nil || repaired.Status != "succeeded" {
			return fmt.Errorf("browser repair did not complete; original budget and failure retained: %v", err)
		}
		defect, err = store.Get(defect.ID)
		if err != nil {
			return err
		}
		rebuilt, err := runTask(dev, p.S(defect.Data, "buildTaskId"))
		if err != nil || rebuilt.Status != "succeeded" {
			return fmt.Errorf("browser repair build did not complete: %v", err)
		}
		artifact, err = store.Get(p.S(p.Obj(rebuilt.Data["result"]), "artifactId"))
		if err != nil {
			return err
		}
		fmt.Println("Rechecking the repaired artifact in the unchanged protected browser pipeline for", mode)
		checked, err = runTask(browser, p.S(defect.Data, "verificationTaskId"))
		if err != nil || checked.Status != "succeeded" {
			return fmt.Errorf("actual browser recheck did not pass: %v", err)
		}
		closed, err := store.Get(defect.ID)
		if err != nil || closed.Status != "closed" || p.S(closed.Data, "verifiedRunId") != checked.ID {
			return fmt.Errorf("defect was not closed by the actual independent browser recheck")
		}
		report["repairIndependentlyVerified"] = true
	}
	report["artifact"], report["browserRunId"] = artifact, checked.ID
	if apiVerifiedBeforeBrowser != artifact.ID {
		apiPipeline, err := command("pipelines.create", p.M{"projectId": projectID, "title": "Unchanged API contract on browser-verified artifact", "runtime": "linux", "method": "automated", "verificationEnvironment": "ci", "command": []any{"python3", "-c", string(qa), mode}})
		if err != nil {
			return err
		}
		_, standards := businessSpec(mode)
		all := []any{}
		for _, criterion := range standards {
			all = append(all, p.S(p.Obj(criterion), "id"))
		}
		apiTask, err := command("pipelines.run", p.M{"pipelineId": apiPipeline.ID, "artifactId": artifact.ID, "criteria": all})
		if err != nil {
			return err
		}
		apiRun, err := runTask(quality, apiTask.ID)
		if err != nil || apiRun.Status != "succeeded" {
			return fmt.Errorf("independent API contract failed on browser-verified artifact: %v", err)
		}
	}
	if err = svc.VerifyArtifact(artifact); err != nil {
		return err
	}
	candidate, err := command("releases.create", p.M{"goalId": goalID, "version": "0.1.0-browser-verified", "commit": p.S(artifact.Data, "commit"), "artifactIds": []any{artifact.ID}})
	if err != nil {
		return fmt.Errorf("independent API/browser evidence did not satisfy candidate gate: %v", err)
	}
	report["candidateRelease"], report["browserGateSatisfied"] = candidate, true
	report["published"], report["releaseApproved"], report["productPlanComplete"] = false, false, false
	return nil
}

func businessSpec(mode string) (string, []any) {
	common := `Build a working public Web/API application from this business baseline. Use Node.js standard libraries, no dependencies or external network resources, app.js and index.html. Start with node app.js. Bind HOST (default 0.0.0.0) and PORT (default 8080); STATE_FILE names the JSON state file. Persist with atomic write/rename and preserve data after server restart. GET /api/health returns JSON with status=ok; GET / serves index.html. Body parsing must reject invalid JSON and cap request size. Use a clean responsive Chinese interface with inline styles, visible focus and errors; all user text must use textContent, never innerHTML. Form submissions prevent duplicate clicks while pending; refresh can recover after network errors. Provide #status (aria-live), #refresh, and no horizontal overflow at 390px. The app must work inside a read-only container with only its state directory writable. Complete both application files; do not claim external publication or device testing. `
	if mode == "feedback" {
		return common + `Feedback: GET /api/feedback returns newest-first array of {id,title,content,createdAt}. POST /api/feedback accepts {title,content}; trim values, require nonempty title <=200 UTF-8 bytes and content <=8000 UTF-8 bytes. Invalid input returns 422 and leaves state unchanged. Success returns 201 with that new record. Never execute stored script text. Client has #title and #content inputs, #submit button, #list showing plain text. Preserve submitted content and display network errors with an enabled refresh path.`, []any{p.M{"id": "F1", "text": "Actual feedback submission and matching list", "method": "automated"}, p.M{"id": "F2", "text": "Invalid or oversized UTF-8 input rejected without partial writes", "method": "automated"}, p.M{"id": "F3", "text": "Feedback survives actual process restart", "method": "automated"}}
	}
	return common + `Booking: APP_SLOTS is a JSON array of initial {id,startsAt} slots, used only for new state. GET /api/slots returns all {id,startsAt,reserved}. POST /api/bookings takes {slotId}, returns 201 with {slotId,bookingToken}; concurrent requests for an occupied slot return 409 and only one succeeds. The credential must use crypto.randomBytes with at least 128 bits. Server persists ONLY SHA256 of the credential, never the plaintext. POST /api/bookings/cancel takes {bookingToken}; incorrect credentials return 403/404 without changing state, correct returns 200 and reopens its slot. Reservation and cancellation survive restart. Client uses #reserve-slot-1 and #reserve-slot-2 buttons for those IDs, #booking-token input retaining the returned credential for this client session, #cancel button. Clearly show availability, conflicts, network errors and cancellation results. No accounts, payments or notifications.`, []any{p.M{"id": "B1", "text": "Slots accurately show availability", "method": "automated"}, p.M{"id": "B2", "text": "Concurrent reservation has exactly one successful writer", "method": "automated"}, p.M{"id": "B3", "text": "Opaque distinct credentials and hash-only state", "method": "automated"}, p.M{"id": "B4", "text": "Only the correct credential can cancel", "method": "automated"}, p.M{"id": "B5", "text": "Reservation and cancellation survive process restarts", "method": "automated"}}
}

// Record bounded diagnostic fields from failed responses without logging the
// request, Authorization header, or successful model content. The original body
// remains intact for the governed gateway and no request is retried here.
type providerDiagnostics struct {
	base     http.RoundTripper
	mu       sync.Mutex
	failures []p.M
}

func (d *providerDiagnostics) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := d.base.RoundTrip(req)
	if err != nil || response.StatusCode < 400 {
		return response, err
	}
	prefix, readErr := io.ReadAll(io.LimitReader(response.Body, 8192))
	response.Body = &diagnosticBody{Reader: io.MultiReader(bytes.NewReader(prefix), response.Body), Closer: response.Body}
	entry := p.M{"httpStatus": response.StatusCode, "observedAt": time.Now().UTC(), "bodyReadError": readErr != nil}
	var body p.M
	if json.Unmarshal(prefix, &body) == nil {
		for _, key := range []string{"type", "code", "message"} {
			value := p.S(p.Obj(body["error"]), key)
			if key == "message" {
				for _, env := range []string{"IS_MODEL_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
					if secret := os.Getenv(env); secret != "" {
						value = strings.ReplaceAll(value, secret, "[redacted]")
					}
				}
			}
			if len(value) > 512 {
				value = value[:512]
			}
			entry[key] = value
		}
	}
	d.mu.Lock()
	d.failures = append(d.failures, entry)
	d.mu.Unlock()
	return response, nil
}

type diagnosticBody struct {
	io.Reader
	io.Closer
}
