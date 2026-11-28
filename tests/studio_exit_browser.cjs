// Real Chromium and local process. No inference or user workspace required.
const assert=require("node:assert/strict"), fs=require("node:fs"), os=require("node:os"), path=require("node:path");
const {spawn}=require("node:child_process"), {createInterface}=require("node:readline");
let chromium;
try { ({chromium}=require("playwright")); } catch {
  const links=path.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const install=fs.readdirSync(links).map(f=>fs.readFileSync(path.join(links,f),"utf8").trim())
    .find(p=>p.endsWith("playwright-core")&&fs.existsSync(p));
  ({chromium}=require(install));
}
const [scenario,exe,out]=process.argv.slice(2);
function launch(data) {
  const process=spawn(exe,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","inherit"]});
  const closed=new Promise(resolve=>process.once("exit",(code,signal)=>resolve({code,signal})));
  const url=new Promise((resolve,reject)=>{
    createInterface({input:process.stdout}).once("line",l=>resolve(l.trim().split(/\s+/).pop()));
    process.once("error",reject);process.once("exit",code=>reject(Error("early exit "+code)));
  });
  return {process,url,closed};
}
async function main() {
  fs.mkdirSync(out,{recursive:true});
  const data=fs.mkdtempSync(path.join(out,"data-"));
  const browser=await chromium.launch({headless:true,chromiumSandbox:true});
  let app;
  try {
    app=launch(data);
    const url=await app.url;
    const page=await browser.newPage({viewport:{width:1280,height:760},reducedMotion:"reduce"});
    const errors=[];
    page.on("pageerror",e=>errors.push(e.message));
    await page.goto(url);
    await page.getByRole("button",{name:"設定",exact:true}).waitFor();
    const exit=page.locator("#studio-exit");
    if (scenario==="baseline" || scenario==="rollback") {
      assert.equal(await exit.count(),0);
      assert.equal((await page.request.post(url+"/api/studio/exit",{headers:{Origin:url,"X-TimbreDeck":"1"}})).status(),405);
      console.log(scenario.toUpperCase()+": no exit button; POST /api/studio/exit = 405");
      return;
    }

    const dialog=page.getByRole("dialog",{name:"結束 TimbreDeck？"});
    const yes=dialog.getByRole("button",{name:"結束工作室",exact:true});
    await exit.click();
    if (out) await page.screenshot({path:path.join(out,"exit-dialog.png"),animations:"disabled"});
    assert.equal(await dialog.getByRole("button",{name:"返回工作室"}).evaluate(e=>e===document.activeElement),true);
    await dialog.getByRole("button",{name:"返回工作室"}).click();
    assert.equal(await dialog.isVisible(),false);
    assert.equal((await page.request.get(url+"/api/state")).status(),200);
    console.log("PASS: cancel keeps Studio running");

    await page.setViewportSize({width:390,height:700});
    await page.locator("#nav-toggle").click();
    await exit.click();
    const busy={id:"a".repeat(32),kind:"training",engine:"gpt-sovits",status:"running"};
    await page.route("**/api/job",r=>r.fulfill({json:busy}));
    await yes.click();
    await dialog.getByText(/有作業進行中/).waitFor();
    assert.equal((await page.request.get(url+"/api/state")).status(),200);
    await page.unroute("**/api/job");
    await page.keyboard.press("Escape");
    assert.equal(await dialog.isVisible(),false);
    assert.equal(await exit.isVisible(),true);
    console.log("PASS: mobile entry and busy UI keep the service running");

    await page.getByRole("button",{name:"設定",exact:true}).click();
    await page.route("**/api/draft",r=>r.fulfill({status:500,json:{error:"Fixture save failed"}}));
    await page.getByRole("checkbox",{name:"減少介面動效及視差"}).check();
    await page.locator("#nav-toggle").click();
    await exit.click();
    await yes.click();
    await dialog.getByText(/草稿未能儲存/).waitFor();
    assert.equal(app.process.exitCode,null);
    await page.unroute("**/api/draft");
    console.log("PASS: save failure blocks exit");

    await Promise.all([page.waitForResponse(r=>r.url().endsWith("/api/studio/exit") && r.status()===200),yes.click()]);
    await page.getByRole("heading",{name:"工作室已結束"}).waitFor();
    const done=await Promise.race([app.closed,new Promise((_,reject)=>{const t=setTimeout(()=>reject(Error("Studio still running")),8000);t.unref()})]);
    assert.deepEqual(done,{code:0,signal:null});
    assert.equal(fs.existsSync(path.join(data,"cache","agent-session.json")),false);
    const draft=JSON.parse(fs.readFileSync(path.join(data,"config","drafts.json"),"utf8"));
    assert.equal(draft.reduceMotion,true);
    assert.deepEqual(errors,[]);
    app=launch(data);
    await page.goto(await app.url);
    await page.locator("#nav-toggle").click();
    await page.getByRole("button",{name:"設定",exact:true}).click();
    assert.equal(await page.getByRole("checkbox",{name:"減少介面動效及視差"}).isChecked(),true);
    console.log("PASS: served acknowledgement, process exited 0, session removed; reopening preserves the draft");
  } finally {
    if(app?.process.exitCode===null && app?.process.signalCode===null) app.process.kill();
    await browser.close();
  }
}
main().catch(e=>{console.error(e);process.exitCode=1});
