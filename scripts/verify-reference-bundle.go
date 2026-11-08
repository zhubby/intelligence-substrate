//go:build ignore

// Opt-in local Flutter/Java/HTTP/Git acceptance; never calls a store or model.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/worker"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func shell(dir string, argv ...string) string {
	cmd := exec.Command("rtk", append([]string{"proxy"}, argv...)...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%s failed: %v\n%s", argv[0], err, output))
	}
	return strings.TrimSpace(string(output))
}

func main() {
	work := flag.String("work-dir", "", "New private evidence directory")
	template := flag.String("signer-config-template", "", "Private configuration using an explicit temporary test upload identity")
	flag.Parse()
	if *work == "" || *template == "" {
		panic("explicit work directory and temporary test signing config required")
	}
	cwd, err := os.Getwd()
	must(err)
	root, err := filepath.Abs(*work)
	must(err)
	must(os.Mkdir(root, 0700))
	private := filepath.Join(root, "private")
	must(os.Mkdir(private, 0700))
	data, err := os.ReadFile(*template)
	must(err)
	var config p.M
	must(json.Unmarshal(data, &config))
	profiles := p.Items(config["profiles"])
	if len(profiles) != 1 {
		panic("exactly one local test profile required")
	}
	profile := p.Obj(profiles[0])
	certificate := p.S(profile, "expectedSigner")
	const target = "android-google-play"
	const packageName = "com.intelligencesubstrate.quality.android_feedback"
	if p.S(profile, "target") != target || p.S(profile, "bundleId") != packageName {
		panic("only the local reference bundle profile is supported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	store, err := p.Open(filepath.Join(root, "control.db"))
	must(err)
	defer store.Close()
	svc := p.NewService(store)
	handler := httpapi.New(svc, false)
	must(handler.Auth.Bootstrap("bundle-acceptance@example.test", "isolated-bundle-acceptance-password"))
	identity, err := handler.Auth.IdentityByEmail("bundle-acceptance@example.test")
	must(err)
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(name string, in p.M) p.Resource {
		value, e := svc.Command(ctx, owner, name, in)
		must(e)
		return value
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	report := p.M{"kind": "actual-controlled-upload-bundle", "startedAt": time.Now().UTC(), "modelCalls": 0,
		"syntheticApprovals": true, "enterpriseIdentity": false, "physicalDevice": false,
		"storePublished": false, "overallP3Complete": false, "serverURL": server.URL}
	save := func() {
		body, e := json.MarshalIndent(report, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600))
	}
	generated := filepath.Join(root, "generated")
	wrapper := filepath.Join(cwd, "work/run-native.py")
	fmt.Println("Generate current unsigned Flutter application")
	fmt.Println(shell(cwd, "env", "DEVELOPER_DIR=/Library/Developer/CommandLineTools", "python3", wrapper,
		"rtk", "proxy", "python3", "scripts/new-app.py", generated, "--name", "android_feedback", "--mode", "feedback",
		"--organization", "com.intelligencesubstrate.quality", "--create-platforms", "--android-unsigned-release", "--gradle-version", "9.4.1"))
	project := command("projects.create", p.M{"title": "Actual controlled Google Play AAB"})
	goal := command("goals.create", p.M{"projectId": project.ID, "title": "Exact upload bundle validation", "outcome": "Build, sign and independently verify the exact AAB; physical and store gates stay open", "targets": []any{target}, "criteria": []any{p.M{"id": "bundle-integrity", "text": "Exact upload certificate, release identity and all-entry cryptographic verification", "method": "automated"}}, "budget": 100})
	command("goals.approve", p.M{"id": goal.ID})
	repository := command("repositories.create", p.M{"projectId": project.ID, "title": "Exact bundle source"})
	source := filepath.Join(root, "source")
	shell(cwd, "git", "clone", svc.RepoPath(repository), source)
	shell(source, "git", "checkout", "-b", "reference-bundle")
	shell(source, "python3", "-c", `import shutil,sys;shutil.copytree(sys.argv[1],sys.argv[2],dirs_exist_ok=True,ignore=shutil.ignore_patterns('.git','.dart_tool','build','.gradle','Pods','ephemeral','.symlinks','local.properties','.flutter-plugins-dependencies','Generated.xcconfig','flutter_export_environment.sh'))`, generated, source)
	shell(source, "git", "add", ".")
	shell(source, "git", "-c", "user.name=Bundle acceptance", "-c", "user.email=bundle@example.test", "commit", "-m", "Import exact unsigned bundle source")
	commit := shell(source, "git", "rev-parse", "HEAD")
	shell(source, "git", "push", "origin", "reference-bundle")
	toolArgs := []any{"--java-home", profile["javaHome"], "--bundletool", profile["bundletoolPath"], "--bundletool-sha256", profile["bundletoolSHA256"], "--bundle-verifier", profile["bundleVerifierPath"]}
	buildCommand := []any{"rtk", "proxy", "env", "DEVELOPER_DIR=/Library/Developer/CommandLineTools", "python3", wrapper,
		"rtk", "proxy", "python3", "scripts/build-reference-client.py", "--application", ".", "--target", "android-unsigned-aab", "--api-url", "https://api.example.test", "--output", "artifacts/unsigned.aab"}
	buildCommand = append(buildCommand, toolArgs...)
	plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Unsigned AAB, fixed upload signer and independent CI bundle validation", "tasks": []any{
		p.M{"id": "unsigned", "title": "Compile exact unsigned bundle", "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "target": target, "artifactStage": "unsigned", "budgetCents": 100, "timeoutSeconds": 900, "command": buildCommand, "outputPath": "artifacts/unsigned.aab"},
		p.M{"id": "sign", "title": "Sign exact AAB with temporary upload certificate", "role": "release", "kind": "build", "runtime": runtime.GOOS, "target": target, "signingMode": "android-aab", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": certificate, "budgetCents": 100, "timeoutSeconds": 300},
	}})
	command("plans.approve", p.M{"id": plan.ID})
	goal, err = store.Get(goal.ID)
	must(err)
	profile["projectId"], profile["goalId"] = project.ID, goal.ID
	config["stateDir"] = filepath.Join(private, "state")
	configPath := filepath.Join(private, "signer.json")
	data, err = json.Marshal(config)
	must(err)
	must(os.WriteFile(configPath, data, 0600))
	qualityCommand := []any{"rtk", "proxy", "python3", filepath.Join(cwd, "scripts/verify-reference-bundle-quality.py"), "--application", ".", "--output", "quality/bundle.tar.gz", "--expected-package", packageName, "--expected-signer", certificate}
	qualityCommand = append(qualityCommand, toolArgs...)
	pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent exact upload bundle validation", "runtime": runtime.GOOS, "method": "automated", "verificationEnvironment": "ci", "reportPath": "quality/bundle.tar.gz", "command": qualityCommand})
	policy := command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": target, "pipelineId": pipeline.ID, "criteria": []any{"bundle-integrity"}, "budgetCents": 100, "key": "exact-upload-bundle"})
	runner := func(role string, quality bool) *worker.Runner {
		capability := "build"
		if quality {
			capability = "test"
		}
		node := command("workers.register", p.M{"projectId": project.ID, "title": "Actual " + role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": quality})
		token, _, e := handler.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
		must(e)
		client, e := worker.NewClient(server.URL, token, true)
		must(e)
		return &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, role+"-worker")}}
	}
	build, sign, qa := runner("developer", false), runner("release", false), runner("quality", true)
	python, err := exec.LookPath("python3")
	must(err)
	rtk, err := exec.LookPath("rtk")
	must(err)
	sign.Config.Signers = map[string][]string{target: {rtk, "proxy", python, filepath.Join(cwd, "scripts/sign-android-apk.py"), "--config", configPath}}
	run := func(r *worker.Runner) {
		worked, e := r.RunOnce(ctx)
		must(e)
		runs, e := store.List(project.ID, "run", goal.ID)
		must(e)
		report["runs"] = runs
		save()
		if !worked {
			panic("expected actual Worker task")
		}
	}
	fmt.Println("Actual Worker Flutter analyze/tests/release AAB build")
	run(build)
	artifacts, err := store.List(project.ID, "artifact", goal.ID)
	must(err)
	if len(artifacts) != 1 || p.S(artifacts[0].Data, "type") != "unsigned-package" {
		panic("actual unsigned bundle build failed; inspect runs")
	}
	unsigned := artifacts[0]
	must(svc.VerifyArtifact(unsigned))
	if worked, e := qa.RunOnce(ctx); e != nil || worked {
		panic("unsigned bundle dispatched for final QA")
	}
	if _, e := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{unsigned.ID}}); e == nil {
		panic("unsigned bundle accepted for release")
	}
	fmt.Println("Dedicated Worker performs actual JAR upload signing")
	run(sign)
	artifacts, err = store.List(project.ID, "artifact", goal.ID)
	must(err)
	var signed p.Resource
	for _, artifact := range artifacts {
		if p.S(artifact.Data, "signingMode") == "android-aab" {
			signed = artifact
		}
	}
	if signed.ID == "" || p.S(signed.Data, "inputArtifactId") != unsigned.ID {
		panic("actual bundle signing failed; inspect runs")
	}
	must(svc.VerifyArtifact(signed))
	fmt.Println("Independent Worker verifies all exact bundle entries and source")
	run(qa)
	evidence, err := store.List(project.ID, "evidence", goal.ID)
	must(err)
	if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != signed.ID {
		panic("independent exact bundle quality failed")
	}
	proof, err := store.Get(p.S(evidence[0].Data, "proofArtifactId"))
	must(err)
	must(svc.VerifyArtifact(proof))
	data, err = os.ReadFile(svc.ArtifactPath(p.S(proof.Data, "digest")))
	must(err)
	must(os.WriteFile(filepath.Join(root, "bundle-report.tar.gz"), data, 0600))
	_, releaseErr := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{signed.ID}})
	if releaseErr == nil || !strings.Contains(releaseErr.Error(), "android-device") {
		panic(fmt.Sprintf("physical gate did not reject: %v", releaseErr))
	}
	releases, err := store.List(project.ID, "release", goal.ID)
	must(err)
	if len(releases) != 0 {
		panic("blocked release persisted")
	}
	for range 2 {
		if worked, e := qa.RunOnce(ctx); e != nil || worked {
			panic("duplicate bundle quality dispatch")
		}
	}
	runs, err := store.List(project.ID, "run", goal.ID)
	must(err)
	if len(runs) != 3 {
		panic("unexpected actual execution count")
	}
	for _, r := range runs {
		if r.Status != "succeeded" {
			panic("actual Worker execution did not succeed")
		}
	}
	dispatches, err := store.List(project.ID, "quality_dispatch", goal.ID)
	must(err)
	if len(dispatches) != 1 {
		panic("duplicate QA dispatch")
	}
	must(svc.VerifyIntegrity(ctx))
	report["projectId"], report["goalId"], report["repositoryId"], report["commit"] = project.ID, goal.ID, repository.ID, commit
	report["unsignedArtifact"], report["signedArtifact"], report["reportArtifact"], report["evidence"], report["policy"], report["dispatches"] = unsigned, signed, proof, evidence[0], policy, dispatches
	report["releaseBlockedReason"], report["persistedReleases"], report["finishedAt"], report["passed"], report["serverStoppedOnExit"] = releaseErr.Error(), len(releases), time.Now().UTC(), true, true
	save()
	fmt.Println("Passed; physical and store gates remain open:", filepath.Join(root, "result.json"))
}
