import { test, expect } from "./fixtures";
import { createHash } from "node:crypto";

// Synthetic iOS package and attestations exercise UI/approval only. No IPA,
// Apple account, physical device or real publication is implied.
test("reviewed store material is selectable, downloadable and bound to approval", async ({page,playwright,ownerSession},info) => {
  const origin="http://127.0.0.1:18082";
  const post=async (client: typeof page.request,path:string,data:unknown,csrf:string)=>{
    const response=await client.post(origin+"/api/v1/"+path,{data,headers:{Origin:origin,"X-CSRF-Token":csrf}});
    const result=await response.json();expect(response.ok(),JSON.stringify(result)).toBeTruthy();return result;
  };
  const command=(name:string,data:unknown)=>post(page.request,"commands/"+name,data,ownerSession.csrf);
  const project=await command("projects.create",{title:"App Store materials "+info.project.name+" "+Date.now()});
  const goal=await command("goals.create",{projectId:project.id,title:"审核固定商店资料",outcome:"发布审批包含可审阅的资料快照",targets:["ios-appstore"],criteria:[{id:"app",text:"目标流程通过",method:"device"}],budget:30});
  await command("goals.approve",{id:goal.id});
  const plan=await command("plans.propose",{goalId:goal.id,summary:"Synthetic browser contract",tasks:[{id:"build",title:"Build fixture",kind:"code",role:"developer",runtime:"darwin"}]});
  await command("plans.approve",{id:plan.id});
  const repo=await command("repositories.create",{projectId:project.id,title:"material-fixture"});
  const commit=await command("repositories.commit",{id:repo.id,branch:"candidate",message:"Synthetic materials fixture",files:{"README.md":"UI approval contract"}});
  const meta={goalId:goal.id,repositoryId:repo.id,commit:commit.data.lastCommit,target:"ios-appstore"};
  const upload=async (client:typeof page.request,csrf:string,type:string,name:string,content:string)=>{
    const query=new URLSearchParams({...meta,type,name,mediaType: "application/json"});
    const response=await client.post(origin+"/api/v1/artifacts/upload?"+query,{data:Buffer.from(content),headers:{Origin:origin,"X-CSRF-Token":csrf,"Content-Type":"application/octet-stream"}});
    const result=await response.json();expect(response.ok(),JSON.stringify(result)).toBeTruthy();return result;
  };
  const pkg=await upload(page.request,ownerSession.csrf,"package","synthetic.ipa","Synthetic UI contract only");
  const materialBody=JSON.stringify({schemaVersion:1,description:"Approved fixture material",privacyPolicyUrl:"https://company.example/privacy"});
  const materials=await upload(page.request,ownerSession.csrf,"report","reviewed-appstore.json",materialBody);
  const digest=createHash("sha256").update(materialBody).digest("hex");expect(materials.data.digest).toBe(digest);
  const email="material-qa-"+info.project.name+"-"+Date.now()+"@example.test";
  const identity=await post(page.request,"identities",{email,name:"Independent material reviewer",password:"material-browser-password-2026"},ownerSession.csrf);
  await command("members.grant",{projectId:project.id,identityId:identity.id,name:identity.name,roles:["quality","release"]});
  const qa=await playwright.request.newContext({baseURL:origin});
  try {
    const login=await post(qa,"auth/login",{email,password:"material-browser-password-2026"},"");
    const proof=await upload(qa,login.csrf,"report","synthetic-attestation.txt","Synthetic device attestation for browser contract only");
    await post(qa,"commands/evidence.record",{artifactId:pkg.id,proofArtifactId:proof.id,method:"device",verificationEnvironment:"ios-device",criteria:["app"],statement:"Synthetic UI fixture; no physical device acceptance"},login.csrf);
    await page.goto("/");
    if(info.project.name==="mobile") await page.getByRole("button",{name:"打开导航",exact:true}).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    await page.getByRole("navigation",{name:"主导航"}).getByRole("button",{name:"构建与发布",exact:true}).click();
    await page.getByRole("button",{name:"创建候选版本",exact:true}).click();
    const dialog=page.getByRole("dialog");
    await dialog.getByLabel("版本号",{exact:true}).fill("1.0.0");
    await dialog.getByLabel("App Store 资料清单",{exact:true}).selectOption(materials.id);
    await dialog.getByRole("button",{name:"检查质量并创建候选版本",exact:true}).click();
    await expect(dialog).not.toBeVisible();
    const materialSection=page.getByRole("region",{name:"已绑定商店资料"});
    await expect(materialSection.getByText(digest,{exact:true})).toBeVisible();
    const download=materialSection.getByRole("link",{name:"查看 App Store 资料清单",exact:true});
    const response=await page.request.get((await download.getAttribute("href"))!);expect(await response.text()).toBe(materialBody);
    await page.getByRole("button",{name:"审阅并批准",exact:true}).click();
    await expect(dialog.getByText("批准同时绑定所列商店资料；拒审后的文字修订需要单独提交资料审批。",{exact:true})).toBeVisible();
    await page.keyboard.press("Escape");await expect(dialog).not.toBeVisible();
    const snapshot=await page.request.get(origin+"/api/v1/projects/"+project.id+"/workspace");const state=await snapshot.json();
    const release=state.find((x:{kind:string})=>x.kind==="release");expect(release.data.storeMetadata[0].digest).toBe(digest);
    await post(qa,"commands/releases.approve",{id:release.id},login.csrf);
    await expect(page.getByText("已批准",{exact:true})).toBeVisible({timeout:15000});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
    await page.screenshot({path:info.outputPath("material-approval.png"),fullPage:true,animations:"disabled"});
  } finally {await qa.dispose()}
});
