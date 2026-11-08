//go:build ignore

// Opt-in local SDK/Worker/emulator acceptance. Never submits a store release.
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
	c := exec.Command("rtk", append([]string{"proxy"}, argv...)...)
	c.Dir = dir
	output, err := c.CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("tool %s failed: %v\n%s", argv[0], err, output))
	}
	return strings.TrimSpace(string(output))
}

func main() {
	workDir := flag.String("work-dir", "", "New private evidence directory")
	template := flag.String("signer-config-template", "", "Private administrator-owned config; uses only an explicit temporary test certificate")
	serial := flag.String("serial", "", "Dedicated emulator serial")
	avd := flag.String("avd", "", "Exact dedicated AVD name")
	port := flag.Int("adb-server-port", 0, "Existing dedicated loopback ADB port")
	sdk := flag.String("android-sdk", "", "Installed Android SDK including build-tools/36.0.0")
	exerciseRepair := flag.Bool("exercise-repair", false, "Introduce a deterministic release-only UI defect, then verify controlled source repair/rebuild/re-sign/recheck")
	flag.Parse()
	if *workDir == "" || *template == "" || *serial == "" || *avd == "" || *port < 1024 || *sdk == "" {
		panic("explicit new work-dir, private test signer config, SDK and emulator required")
	}
	cwd, err := os.Getwd()
	must(err)
	root, err := filepath.Abs(*workDir)
	must(err)
	must(os.Mkdir(root, 0700))
	*sdk, err = filepath.Abs(*sdk)
	must(err)
	tools := filepath.Join(*sdk, "build-tools/36.0.0")
	wrapper := filepath.Join(cwd, "work/run-native.py")
	private := filepath.Join(root, "private")
	must(os.Mkdir(private, 0700))
	configBytes, err := os.ReadFile(*template)
	must(err)
	var config p.M
	must(json.Unmarshal(configBytes, &config))
	profiles := p.Items(config["profiles"])
	if len(profiles) != 1 {
		panic("exactly one temporary test signing profile required")
	}
	profile := p.Obj(profiles[0])
	certificate := p.S(profile, "expectedSigner")
	if p.S(profile, "bundleId") != "com.intelligencesubstrate.quality.android_feedback" || p.S(profile, "target") != "android-huawei" {
		panic("only the explicit local reference/test profile is supported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	store, err := p.Open(filepath.Join(root, "control.db"))
	must(err)
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	must(h.Auth.Bootstrap("signing-acceptance@example.test", "isolated-signing-acceptance-password"))
	identity, err := h.Auth.IdentityByEmail("signing-acceptance@example.test")
	must(err)
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(name string, in p.M) p.Resource { r, e := svc.Command(ctx, owner, name, in); must(e); return r }
	server := httptest.NewServer(h)
	defer server.Close()
	report := p.M{"kind": "actual-trusted-signing-worker-and-offline-emulator", "startedAt": time.Now().UTC(), "modelCalls": 0, "syntheticApprovals": true, "enterpriseIdentity": false, "physicalDevice": false, "storePublished": false, "businessVerified": false, "overallP3Complete": false}
	report["exerciseRepair"] = *exerciseRepair
	save := func() {
		body, e := json.MarshalIndent(report, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600))
	}
	generated := filepath.Join(root, "generated")
	fmt.Println("Generate current unsigned reference source")
	fmt.Println(shell(cwd, "env", "DEVELOPER_DIR=/Library/Developer/CommandLineTools", "python3", wrapper, "rtk", "proxy", "python3", "scripts/new-app.py", generated, "--name", "android_feedback", "--mode", "feedback", "--organization", "com.intelligencesubstrate.quality", "--create-platforms", "--android-unsigned-release", "--gradle-version", "9.4.1"))
	project := command("projects.create", p.M{"title": "Actual trusted Android signing"})
	goal := command("goals.create", p.M{"projectId": project.ID, "title": "Release APK signed installation", "outcome": "Independently verify exact signed APK installation and offline UI; physical/online/store acceptance remains open", "targets": []any{"android-huawei"}, "criteria": []any{p.M{"id": "native-install", "text": "Exact temporary certificate, non-debuggable APK, same installed bytes, native launch and offline validation UI", "method": "simulator"}}, "budget": 100})
	command("goals.approve", p.M{"id": goal.ID})
	repository := command("repositories.create", p.M{"projectId": project.ID, "title": "Exact unsigned Android source"})
	source := filepath.Join(root, "source")
	shell(cwd, "git", "clone", svc.RepoPath(repository), source)
	shell(source, "git", "checkout", "-b", "reference-trusted-signing")
	shell(source, "python3", "-c", `import shutil,sys;shutil.copytree(sys.argv[1],sys.argv[2],dirs_exist_ok=True,ignore=shutil.ignore_patterns('.git','.dart_tool','build','.gradle','Pods','ephemeral','.symlinks','local.properties','.flutter-plugins-dependencies','Generated.xcconfig','flutter_export_environment.sh'))`, generated, source)
	shell(source, "git", "add", ".")
	shell(source, "git", "-c", "user.name=Signing acceptance", "-c", "user.email=signing@example.test", "commit", "-m", "Import exact unsigned reference source")
	commit := shell(source, "git", "rev-parse", "HEAD")
	shell(source, "git", "push", "origin", "reference-trusted-signing")
	tasks := []any{
		p.M{"id": "unsigned", "title": "Compile exact unsigned release", "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "target": "android-huawei", "artifactStage": "unsigned", "budgetCents": 100, "timeoutSeconds": 900, "command": []any{"rtk", "proxy", "env", "DEVELOPER_DIR=/Library/Developer/CommandLineTools", "python3", wrapper, "rtk", "proxy", "python3", "scripts/build-reference-client.py", "--application", ".", "--target", "android-unsigned-apk", "--api-url", "https://api.example.test", "--aapt", filepath.Join(tools, "aapt"), "--apksigner", filepath.Join(tools, "apksigner"), "--output", "artifacts/unsigned.apk"}, "outputPath": "artifacts/unsigned.apk"},
		p.M{"id": "sign", "title": "Sign exact APK with temporary certificate", "role": "release", "kind": "build", "runtime": runtime.GOOS, "target": "android-huawei", "signingMode": "android-apk", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": certificate, "budgetCents": 100, "timeoutSeconds": 300},
	}
	if *exerciseRepair {
		// Deterministic fixture, not model reasoning. Debug widget tests continue
		// to pass; only the independently installed release UI exposes the defect.
		code := p.M{"id": "code", "title": "Deterministic release-only defect and source repair fixture", "role": "developer", "kind": "code", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "branch": "fixture-implementation", "budgetCents": 100, "command": []any{"python3", "-c", `from pathlib import Path
p=Path('client/lib/main.dart');text=p.read_text()
good="'请输入反馈标题。'"
bad="(const bool.fromEnvironment('dart.vm.product') ? '请填写标题后再提交。' : '请输入反馈标题。')"
marker='// IS deterministic release defect fixture'
if marker in text:
 assert bad in text
 text=text.replace(bad,good,1).replace(marker,'// IS deterministic source repair fixture',1)
else:
 assert good in text
 text=text.replace(good,bad,1)+'\n'+marker+'\n'
p.write_text(text)
`}}
		unsigned := p.Obj(tasks[0])
		delete(unsigned, "commit")
		unsigned["dependsOn"] = []any{"code"}
		tasks = append([]any{code}, tasks...)
	}
	plan := command("plans.propose", p.M{"goalId": goal.ID, "summary": "Unsigned release, fixed trusted signing and independent final-package QA", "tasks": tasks})
	command("plans.approve", p.M{"id": plan.ID})
	goal, err = store.Get(goal.ID)
	must(err)
	profile["projectId"], profile["goalId"] = project.ID, goal.ID
	config["stateDir"] = filepath.Join(private, "state")
	configPath := filepath.Join(private, "signer.json")
	configBytes, err = json.Marshal(config)
	must(err)
	must(os.WriteFile(configPath, configBytes, 0600))
	pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Independent SDK and installed release APK smoke", "runtime": runtime.GOOS, "method": "simulator", "verificationEnvironment": "simulator", "reportPath": "quality/signed.tar.gz", "command": []any{"rtk", "proxy", "python3", wrapper, "rtk", "proxy", "python3", filepath.Join(cwd, "scripts/verify-reference-android-quality.py"), "--application", ".", "--api-port", "18142", "--output", "quality/signed.tar.gz", "--serial", *serial, "--avd", *avd, "--adb-server-port", fmt.Sprint(*port), "--expected-package", "com.intelligencesubstrate.quality.android_feedback", "--adb", filepath.Join(*sdk, "platform-tools/adb"), "--aapt", filepath.Join(tools, "aapt"), "--apksigner", filepath.Join(tools, "apksigner"), "--release-offline-smoke", "--expected-release-signer", certificate}})
	policy := command("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "target": "android-huawei", "pipelineId": pipeline.ID, "criteria": []any{"native-install"}, "budgetCents": 100, "key": "exact-release-installation"})
	runner := func(role string, capabilities []any, quality bool) *worker.Runner {
		node := command("workers.register", p.M{"projectId": project.ID, "title": "Actual " + role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": capabilities, "trustedQuality": quality})
		token, _, e := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
		must(e)
		client, e := worker.NewClient(server.URL, token, true)
		must(e)
		return &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, role+"-worker")}}
	}
	build, sign, qa := runner("developer", []any{"code", "build"}, false), runner("release", []any{"build"}, false), runner("quality", []any{"test"}, true)
	rtk, err := exec.LookPath("rtk")
	must(err)
	python, err := exec.LookPath("python3")
	must(err)
	sign.Config.Signers = map[string][]string{"android-huawei": {rtk, "proxy", python, filepath.Join(cwd, "scripts/sign-android-apk.py"), "--config", configPath}}
	dump := func() []p.Resource {
		runs, e := store.List(project.ID, "run", goal.ID)
		must(e)
		report["runs"] = runs
		save()
		return runs
	}
	work := func(r *worker.Runner) {
		worked, e := r.RunOnce(ctx)
		must(e)
		if !worked {
			panic("expected actual Worker task")
		}
		dump()
	}
	if *exerciseRepair {
		fmt.Println("Deterministic approved source task introduces a release-only defect")
		work(build)
	}
	fmt.Println("Worker compiles actual release APK from", commit)
	worked, err := build.RunOnce(ctx)
	must(err)
	if !worked {
		panic("unsigned build not claimed")
	}
	dump()
	artifacts, err := store.List(project.ID, "artifact", goal.ID)
	must(err)
	if len(artifacts) != 1 || p.S(artifacts[0].Data, "type") != "unsigned-package" {
		panic("unsigned build failed; inspect private evidence")
	}
	unsigned := artifacts[0]
	must(svc.VerifyArtifact(unsigned))
	if idle, e := qa.RunOnce(ctx); e != nil || idle {
		panic("unsigned APK was dispatched for final QA")
	}
	if _, e := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{unsigned.ID}}); e == nil {
		panic("unsigned APK accepted as candidate")
	}
	fmt.Println("Dedicated Worker invokes trusted offline signer")
	worked, err = sign.RunOnce(ctx)
	must(err)
	if !worked {
		panic("signing not claimed")
	}
	runs := dump()
	var signed p.Resource
	artifacts, err = store.List(project.ID, "artifact", goal.ID)
	must(err)
	for _, artifact := range artifacts {
		if p.S(artifact.Data, "signingMode") == "android-apk" {
			signed = artifact
		}
	}
	if signed.ID == "" || p.S(signed.Data, "inputArtifactId") != unsigned.ID {
		panic("signing failed; inspect private evidence")
	}
	must(svc.VerifyArtifact(signed))
	fmt.Println("Independent Worker verifies and installs exact final APK")
	worked, err = qa.RunOnce(ctx)
	must(err)
	if !worked {
		panic("signed automatic QA not claimed")
	}
	runs = dump()
	if *exerciseRepair {
		initial := signed
		report["initialSignedArtifact"], report["initialUnsignedArtifact"] = initial, unsigned
		defects, e := store.List(project.ID, "defect", goal.ID)
		must(e)
		if len(defects) != 1 || defects[0].Status != "repairing" {
			panic("actual installed release UI failure did not authorize controlled repair")
		}
		failedRun, e := store.Get(p.S(defects[0].Data, "failedRunId"))
		must(e)
		failedProof, e := store.Get(p.S(p.Obj(failedRun.Data["result"]), "reportArtifactId"))
		must(e)
		must(svc.VerifyArtifact(failedProof))
		failedBytes, e := os.ReadFile(svc.ArtifactPath(p.S(failedProof.Data, "digest")))
		must(e)
		must(os.WriteFile(filepath.Join(root, "failed-report.tar.gz"), failedBytes, 0600))
		fmt.Println(shell(cwd, "python3", "-c", `import json,pathlib,sys,tarfile
r=pathlib.Path(sys.argv[2]);r.mkdir();t=tarfile.open(sys.argv[1]);assert all('/' not in m.name and '..' not in m.name and m.isfile() for m in t.getmembers());t.extractall(r,filter='data');v=json.loads((r/'result.json').read_text());assert not v['passed'] and v['installedAPKDigest']==v['archive']['sha256']==sys.argv[3] and '请输入反馈标题。' in v['error'] and all(x['stopped'] for x in v['cleanup']);assert (r/'failure.png').is_file();print('Actual release-only native UI failure recorded and cleaned up')`, filepath.Join(root, "failed-report.tar.gz"), filepath.Join(root, "failed-report"), p.S(initial.Data, "digest")))
		report["failedRun"], report["failedReportArtifact"] = failedRun, failedProof
		fmt.Println("Approved deterministic recipe repairs source; compile a fresh unsigned APK")
		work(build)
		work(build)
		if idle, e := qa.RunOnce(ctx); e != nil || idle {
			panic("unsigned repaired APK was dispatched for final QA")
		}
		fmt.Println("Fresh controlled signing operation for the repaired unsigned APK")
		work(sign)
		d, e := store.Get(defects[0].ID)
		must(e)
		allRuns := dump()
		for _, run := range allRuns {
			if p.S(run.Data, "taskId") == p.S(d.Data, "signingTaskId") && run.Status == "succeeded" {
				signed, e = store.Get(p.S(p.Obj(run.Data["result"]), "artifactId"))
				must(e)
			}
		}
		if signed.ID == initial.ID || p.S(signed.Data, "commit") == p.S(initial.Data, "commit") || p.S(signed.Data, "digest") == p.S(initial.Data, "digest") || p.S(signed.Data, "expectedSigner") != p.S(initial.Data, "expectedSigner") {
			panic("repaired signing did not produce fresh source/package with the same certificate")
		}
		unsigned, e = store.Get(p.S(signed.Data, "inputArtifactId"))
		must(e)
		commit = p.S(signed.Data, "commit")
		shell(source, "git", "fetch", "origin")
		fmt.Println("Independent Worker installs and rechecks exact repaired final APK")
		work(qa)
		d, e = store.Get(defects[0].ID)
		must(e)
		if d.Status != "closed" || len(p.Items(d.Data["androidSigningRepairs"])) != 1 {
			panic("actual repaired native package did not close defect")
		}
		report["defect"] = d
		runs = dump()
	}
	evidence, err := store.List(project.ID, "evidence", goal.ID)
	must(err)
	if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "artifactId") != signed.ID {
		panic("independent signed APK acceptance failed; inspect evidence")
	}
	proof, err := store.Get(p.S(evidence[0].Data, "proofArtifactId"))
	must(err)
	must(svc.VerifyArtifact(proof))
	proofBytes, err := os.ReadFile(svc.ArtifactPath(p.S(proof.Data, "digest")))
	must(err)
	must(os.WriteFile(filepath.Join(root, "signed-report.tar.gz"), proofBytes, 0600))
	fmt.Println(shell(cwd, "python3", "-c", `import hashlib,json,pathlib,subprocess,sys,tarfile
r=pathlib.Path(sys.argv[2]);r.mkdir();t=tarfile.open(sys.argv[1]);assert all('/' not in m.name and '..' not in m.name and m.isfile() for m in t.getmembers());t.extractall(r,filter='data');v=json.loads((r/'result.json').read_text());assert v['passed'] and v['archive']['sha256']==v['installedAPKDigest']==sys.argv[3] and not v['businessVerified'] and not v['liveAPIVerified'] and all(x['stopped'] for x in v['cleanup'])
def git(*args):return subprocess.check_output(['rtk','proxy','git','-C',sys.argv[4],*args])
files=set(git('ls-tree','-r','--name-only','-z',sys.argv[5]).decode().split('\0'))-{''};assert files==set(v['sourceFiles']),{'missing':sorted(files-set(v['sourceFiles'])),'extra':sorted(set(v['sourceFiles'])-files)}
for name,digest in v['sourceFiles'].items():assert hashlib.sha256(git('show',sys.argv[5]+':'+name)).hexdigest()==digest,name
print('Actual signed installation and exact source tree read back:',len(files))`, filepath.Join(root, "signed-report.tar.gz"), filepath.Join(root, "report"), p.S(signed.Data, "digest"), source, commit))
	_, releaseErr := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{signed.ID}})
	if releaseErr == nil || !strings.Contains(releaseErr.Error(), "android-device") {
		panic(fmt.Sprintf("physical gate must reject: %v", releaseErr))
	}
	releases, err := store.List(project.ID, "release", goal.ID)
	must(err)
	if len(releases) != 0 {
		panic("blocked release persisted")
	}
	for range 2 {
		if idle, e := qa.RunOnce(ctx); e != nil || idle {
			panic("duplicate quality execution")
		}
	}
	dispatches, err := store.List(project.ID, "quality_dispatch", goal.ID)
	must(err)
	expectedDispatches, expectedFailures := 1, 0
	if *exerciseRepair {
		expectedDispatches, expectedFailures = 2, 1
	}
	if len(dispatches) != expectedDispatches {
		panic("duplicate QA dispatch")
	}
	failures := 0
	for _, run := range runs {
		if run.Status == "failed" && p.S(p.Obj(run.Data["task"]), "kind") == "test" {
			failures++
		} else if run.Status != "succeeded" {
			panic("not all actual executions succeeded")
		}
	}
	if failures != expectedFailures {
		panic("actual failing native quality run count does not match fixture")
	}
	must(svc.VerifyIntegrity(ctx))
	report["projectId"], report["goalId"], report["repositoryId"], report["commit"] = project.ID, goal.ID, repository.ID, commit
	report["unsignedArtifact"], report["signedArtifact"], report["reportArtifact"], report["evidence"], report["policy"], report["dispatches"] = unsigned, signed, proof, evidence[0], policy, dispatches
	report["releaseBlockedReason"], report["persistedReleases"], report["finishedAt"], report["passed"], report["serverStoppedOnExit"] = releaseErr.Error(), len(releases), time.Now().UTC(), true, true
	save()
	fmt.Println("Passed; physical, business, enterprise and store gates remain open:", filepath.Join(root, "result.json"))
}
