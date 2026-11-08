//go:build ignore

// Real platform builds and automatic emulator acceptance; no store operation.
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
		panic(fmt.Sprintf("%s: %v: %s", argv[0], err, output))
	}
	return strings.TrimSpace(string(output))
}
func main() {
	workDir := flag.String("work-dir", "", "New evidence directory")
	serial := flag.String("serial", "", "Explicit dedicated emulator serial")
	avd := flag.String("avd", "", "Exact dedicated AVD name")
	adbPort := flag.Int("adb-server-port", 0, "Already running dedicated loopback ADB server")
	sdk := flag.String("android-sdk", "", "Installed Android SDK including build-tools/36.0.0")
	wrapper := flag.String("native-wrapper", "work/run-native.py", "Administrator-provided local toolchain wrapper")
	flag.Parse()
	if *workDir == "" || *serial == "" || *avd == "" || *adbPort < 1024 || *sdk == "" {
		panic("explicit work-dir, emulator serial/AVD, ADB port and Android SDK required")
	}
	cwd, err := os.Getwd()
	must(err)
	root, err := filepath.Abs(*workDir)
	must(err)
	*sdk, err = filepath.Abs(*sdk)
	must(err)
	*wrapper, err = filepath.Abs(*wrapper)
	must(err)
	must(os.Mkdir(root, 0700))
	store, err := p.Open(filepath.Join(root, "control.db"))
	must(err)
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	must(h.Auth.Bootstrap("android-quality@example.test", "isolated-android-quality-password"))
	identity, err := h.Auth.IdentityByEmail("android-quality@example.test")
	must(err)
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	cmd := func(name string, in p.M) p.Resource { r, e := svc.Command(ctx, owner, name, in); must(e); return r }
	server := httptest.NewServer(h)
	defer server.Close()
	report := p.M{"kind": "same-apk-automatic-emulator-quality", "modelCalls": 0, "syntheticApprovals": true, "physicalDevice": false, "storePublished": false, "startedAt": time.Now().UTC()}
	results := []any{}
	save := func() {
		b, e := json.MarshalIndent(report, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(root, "result.json"), append(b, '\n'), 0600))
	}
	for index, mode := range []string{"feedback", "booking"} {
		name := "android_" + mode
		packageName := "com.intelligencesubstrate.quality." + name
		generated := filepath.Join(root, mode+"-generated")
		fmt.Println("Generating fresh Android application:", mode)
		fmt.Println(shell(cwd, "python3", *wrapper, "rtk", "proxy", "python3", "scripts/new-app.py", generated, "--name", name, "--mode", mode, "--organization", "com.intelligencesubstrate.quality", "--create-platforms", "--gradle-version", "9.4.1"))
		project := cmd("projects.create", p.M{"title": "Actual Android QA - " + mode})
		goal := cmd("goals.create", p.M{"projectId": project.ID, "title": "Reference Android simulator quality", "outcome": "Verify exact diagnostic APK; physical device and release remain mandatory", "targets": []any{"android-google-play"}, "criteria": []any{p.M{"id": "business-simulator", "text": "Native business, persistence, ownership and network recovery in dedicated emulator", "method": "simulator"}}, "budget": 100})
		cmd("goals.approve", p.M{"id": goal.ID})
		repository := cmd("repositories.create", p.M{"projectId": project.ID, "title": "Fresh Android " + mode})
		source := filepath.Join(root, mode+"-source")
		shell(cwd, "git", "clone", svc.RepoPath(repository), source)
		shell(source, "git", "checkout", "-b", "reference-android-quality")
		shell(source, "python3", "-c", `import shutil,sys;shutil.copytree(sys.argv[1],sys.argv[2],dirs_exist_ok=True,ignore=shutil.ignore_patterns('.git','.dart_tool','build','.gradle','Pods','ephemeral','.symlinks','local.properties','.flutter-plugins-dependencies','Generated.xcconfig','flutter_export_environment.sh'))`, generated, source)
		shell(source, "git", "add", ".")
		shell(source, "git", "-c", "user.name=Reference acceptance", "-c", "user.email=reference@example.test", "commit", "-m", "Import fresh Android reference application")
		commit := shell(source, "git", "rev-parse", "HEAD")
		shell(source, "git", "push", "origin", "reference-android-quality")
		apiPort := fmt.Sprint(18138 + index)
		plan := cmd("plans.propose", p.M{"goalId": goal.ID, "summary": "Build exact source Android debug diagnostic", "tasks": []any{p.M{"id": "android-build", "title": "Build actual Android " + mode, "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repository.ID, "commit": commit, "branch": "reference-android-quality", "command": []any{"rtk", "proxy", "python3", *wrapper, "rtk", "proxy", "python3", "scripts/build-reference-client.py", "--application", ".", "--target", "android-debug", "--api-url", "http://10.0.2.2:" + apiPort, "--local-testing", "--output", "artifacts/app-debug.apk"}, "outputPath": "artifacts/app-debug.apk", "target": "android-google-play", "timeoutSeconds": 900}}})
		cmd("plans.approve", p.M{"id": plan.ID})
		pipeline := cmd("pipelines.create", p.M{"projectId": project.ID, "title": "Exact APK native emulator acceptance", "command": []any{"rtk", "proxy", "python3", *wrapper, "rtk", "proxy", "python3", filepath.Join(cwd, "scripts/verify-reference-android-quality.py"), "--application", ".", "--api-port", apiPort, "--output", "quality/android.tar.gz", "--serial", *serial, "--avd", *avd, "--adb-server-port", fmt.Sprint(*adbPort), "--expected-package", packageName, "--adb", filepath.Join(*sdk, "platform-tools/adb"), "--aapt", filepath.Join(*sdk, "build-tools/36.0.0/aapt"), "--apksigner", filepath.Join(*sdk, "build-tools/36.0.0/apksigner")}, "reportPath": "quality/android.tar.gz", "method": "simulator", "runtime": runtime.GOOS, "verificationEnvironment": "simulator"})
		policy := cmd("quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": "android-google-play", "criteria": []any{"business-simulator"}, "budgetCents": 100, "key": "actual-android-emulator"})
		runner := func(role, capability string, quality bool) *worker.Runner {
			node := cmd("workers.register", p.M{"projectId": project.ID, "title": mode + "-" + role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": quality})
			token, _, e := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
			must(e)
			client, e := worker.NewClient(server.URL, token, true)
			must(e)
			return &worker.Runner{Client: client, Config: worker.Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, mode+"-"+role)}}
		}
		dump := func() []p.Resource {
			runs, e := store.List(project.ID, "run", goal.ID)
			must(e)
			b, _ := json.MarshalIndent(runs, "", "  ")
			must(os.WriteFile(filepath.Join(root, mode+"-runs.json"), b, 0600))
			return runs
		}
		fmt.Println("Running actual Worker Android build:", mode, commit)
		worked, err := runner("developer", "build", false).RunOnce(ctx)
		must(err)
		if !worked {
			panic("build was not claimed")
		}
		artifacts, err := store.List(project.ID, "artifact", goal.ID)
		must(err)
		if len(artifacts) != 1 {
			dump()
			panic("actual APK build failed; inspect " + root)
		}
		artifact := artifacts[0]
		must(svc.VerifyArtifact(artifact))
		qa := runner("quality", "test", true)
		fmt.Println("Polling automatic exact-APK emulator QA:", mode, artifact.ID)
		worked, err = qa.RunOnce(ctx)
		must(err)
		if !worked {
			panic("automatic Android quality was not claimed")
		}
		runs := dump()
		evidence, err := store.List(project.ID, "evidence", goal.ID)
		must(err)
		if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "method") != "simulator" || p.S(evidence[0].Data, "verificationEnvironment") != "simulator" {
			panic("actual emulator quality failed; inspect " + root)
		}
		proof, err := store.Get(p.S(evidence[0].Data, "proofArtifactId"))
		must(err)
		must(svc.VerifyArtifact(proof))
		if p.S(proof.Data, "testedArtifactId") != artifact.ID || p.S(proof.Data, "runId") != p.S(evidence[0].Data, "runId") || p.S(proof.Data, "digest") != p.S(evidence[0].Data, "proofDigest") {
			panic("report is not tied to the exact APK/run")
		}
		bytes, err := os.ReadFile(svc.ArtifactPath(p.S(proof.Data, "digest")))
		must(err)
		proofFile := filepath.Join(root, mode+"-android-report.tar.gz")
		must(os.WriteFile(proofFile, bytes, 0600))
		reportDir := filepath.Join(root, mode+"-android")
		fmt.Println(shell(cwd, "python3", "-c", `import hashlib,json,pathlib,subprocess,sys,tarfile
r=pathlib.Path(sys.argv[2]);r.mkdir();t=tarfile.open(sys.argv[1]);names=t.getnames();assert all('/' not in n and '..' not in n and t.getmember(n).isfile() for n in names);t.extractall(r,filter='data');v=json.loads((r/'result.json').read_text());assert v['passed'] and v['archive']['sha256']==v['installedAPKDigest']==sys.argv[3] and all(x['stopped'] for x in v['cleanup']);assert v['method']==v['verificationEnvironment']=='simulator' and not v['physicalDevice']
def git(*args):return subprocess.check_output(['rtk','proxy','git','-C',sys.argv[4],*args])
files=set(git('ls-tree','-r','--name-only','-z',sys.argv[5]).decode().split('\0'))-{''};assert files==set(v['sourceFiles']),{'missing':sorted(files-set(v['sourceFiles'])),'extra':sorted(set(v['sourceFiles'])-files)}
for name,digest in v['sourceFiles'].items():assert hashlib.sha256(git('show',sys.argv[5]+':'+name)).hexdigest()==digest,name
print('Exact installed APK, original report and complete Git source tree verified:',len(files))`, proofFile, reportDir, p.S(artifact.Data, "digest"), source, commit))
		_, releaseErr := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{artifact.ID}})
		if releaseErr == nil || !strings.Contains(releaseErr.Error(), "android-device") {
			panic(fmt.Sprintf("simulator evidence must not satisfy physical release gate: %v", releaseErr))
		}
		releases, err := store.List(project.ID, "release", goal.ID)
		must(err)
		if len(releases) != 0 {
			panic("blocked native candidate was persisted")
		}
		for range 2 {
			if idle, e := qa.RunOnce(ctx); e != nil || idle {
				panic("repeat poll duplicated QA")
			}
		}
		dispatches, err := store.List(project.ID, "quality_dispatch", goal.ID)
		must(err)
		if len(dispatches) != 1 {
			panic("duplicate Android quality dispatch")
		}
		results = append(results, p.M{"mode": mode, "projectId": project.ID, "goalId": goal.ID, "repositoryId": repository.ID, "commit": commit, "artifact": artifact, "reportArtifact": proof, "evidence": evidence[0], "runs": runs, "policy": policy, "dispatches": dispatches, "releaseBlocked": true, "releaseBlockedReason": releaseErr.Error(), "persistedReleases": len(releases), "reportPath": reportDir})
		report["applications"] = results
		save()
		fmt.Println("Actual Android QA passed; physical gate remains closed:", mode, releaseErr)
	}
	must(svc.VerifyIntegrity(ctx))
	report["passed"] = true
	report["finishedAt"] = time.Now().UTC()
	report["serverStoppedOnExit"] = true
	save()
	fmt.Println("Passed:", filepath.Join(root, "result.json"))
}
