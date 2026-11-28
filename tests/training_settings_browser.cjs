// Exercises actual Chromium layout and input; API calls are intercepted, so no training runs.
const assert = require("node:assert/strict");
const fs = require("node:fs"), os = require("node:os"), path = require("node:path");
const {spawn} = require("node:child_process"), {createInterface} = require("node:readline");
let playwright;
try { playwright = require("playwright"); } catch {
  const links = path.join(os.homedir(), "AppData/Local/ms-playwright/.links");
  const install = fs.readdirSync(links).map(file => fs.readFileSync(path.join(links,file),"utf8").trim())
    .find(root => root.endsWith("playwright-core") && fs.existsSync(root));
  playwright = require(install);
}

const [scenario = "modified", studio, output] = process.argv.slice(2);
const projectId = "a".repeat(32), reference = "b".repeat(64);
const project = {id:projectId,revision:1,name:"Synthetic review",clips:Array.from({length:3},(_,i) => ({
  id:String(i+1).repeat(32),reference,sourceName:"test.wav",startMs:i*1000,endMs:(i+1)*1000,
  language:"en",transcript:"Test clip "+i,use:"train",
}))};
const state = {draft:{engine:"gpt-sovits",drafts:{}},build:{version:"test",id:"test"},engines:{
  "gpt-sovits":{ready:true,engineInstalled:true,variants:["v2Pro"],availableVariants:["v2Pro"],
    modes:[{id:"clone",label:"Clone",reference:true,transcript:true,languages:true}]},
}};
const assets = path.resolve(__dirname,"../studio/ui");
const mime = {".html":"text/html", ".js":"text/javascript", ".css":"text/css", ".svg":"image/svg+xml"};
const decimal = field => field.evaluate(input => {
  const {left,width,top,height} = input.getBoundingClientRect();
  const hit = document.elementFromPoint(left+width/2,top+height/2);
  return {reachesInput:hit===input,cover:hit?.closest(".card")?.querySelector("h2")?.textContent};
});
async function type(field, value) {
  await field.scrollIntoViewIfNeeded();
  assert.equal((await decimal(field)).reachesInput,true,"Control is covered: "+await field.getAttribute("id"));
  await field.click();
  await field.press("ControlOrMeta+a");
  await field.pressSequentially(String(value));
  assert.equal(await field.inputValue(),String(value));
}

async function main() {
  const browser = await playwright.chromium.launch({headless:true,chromiumSandbox:true});
  const requests = [];
  let server, origin = "http://timbredeck.training.test";
  try {
    if (output) fs.mkdirSync(output,{recursive:true});
    if (studio) {
      const data = fs.mkdtempSync(path.join(output,"data-"));
      server = spawn(studio,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","inherit"]});
      origin = await new Promise((resolve,reject) => {
        createInterface({input:server.stdout}).once("line",line => resolve(line.trim().split(/\s+/).pop()));
        server.once("error",reject); server.once("exit",code => reject(Error("Studio exited "+code)));
      });
      assert.match(origin,/^http:\/\/127\.0\.0\.1:\d+$/);
    }
    const page = await browser.newPage({viewport:{width:1280,height:800},reducedMotion:"no-preference"});
    const errors = [];
    page.on("pageerror",error => errors.push(error.message));
    await page.route("**/*", async route => {
      const {origin:host,pathname:key} = new URL(route.request().url());
      if (host!==origin) return route.abort();
      const method = route.request().method();
      if (key==="/api/state") return route.fulfill({json:state});
      if (key==="/api/training") return route.fulfill({json:[project]});
      if (key==="/api/training/"+projectId && method==="PUT") return route.fulfill({json:{...project,revision:2}});
      if (key==="/api/tts/resident") return route.fulfill({json:{busy:false}});
      if (key==="/api/reference") return route.fulfill({json:{id:reference,seconds:19.4}});
      if (key==="/api/datasets/auto" && method==="GET") return route.fulfill({json:[{id:projectId,speaker:"speaker-1",count:13}]});
      if ((key==="/api/datasets/auto" || key==="/api/training/"+projectId+"/train") && method==="POST") {
        requests.push({key,body:route.request().postDataJSON()});
        return route.fulfill({status:202,json:{id:projectId,kind:"training",engine:"gpt-sovits",status:"done"}});
      }
      if (key==="/api/job") return route.fulfill({json:null});
      if (key.startsWith("/api/")) return route.fulfill({json:[]});
      if (server) return route.continue();
      const name = key==="/" ? "index.html" : key.slice(1);
      const file = path.join(assets,name);
      return /^[a-z0-9.-]+$/.test(name) && fs.existsSync(file) ?
        route.fulfill({body:fs.readFileSync(file),contentType:mime[path.extname(name)]}) : route.abort();
    });
    await page.goto(origin);
    await page.getByRole("button",{name:"訓練素材",exact:true}).click();
    const quick = page.locator("#training-quick");
    const label = "訓練設定";
    const epochs = quick.getByLabel("訓練輪數",{exact:true});
    const batch = quick.getByLabel("批次大小",{exact:true});
    if (scenario==="baseline" || scenario==="rollback") {
      await quick.getByText(label,{exact:true}).click();
      await epochs.scrollIntoViewIfNeeded();
      await page.getByRole("heading",{name:"訓練結果",exact:true}).evaluate(node => Promise.all(node.closest(".card").getAnimations().map(a=>a.finished)));
      const coverage = await Promise.all([decimal(epochs),decimal(batch)]);
      assert.deepEqual(coverage,[{reachesInput:false,cover:"訓練結果"},{reachesInput:false,cover:"訓練結果"}]);
      if (output) await page.screenshot({path:path.join(output,"covered.png")});
      console.log(scenario.toUpperCase()+": 訓練結果 intercepts 訓練輪數 and 批次大小");
      return;
    }

    const quickToggle = quick.getByRole("button",{name:label,exact:true});
    await quickToggle.focus();
    await quickToggle.press("Enter");
    assert.equal(await quickToggle.getAttribute("aria-expanded"),"true");
    assert.equal(await epochs.inputValue(),"8");
    assert.equal(await batch.inputValue(),"2");
    await epochs.scrollIntoViewIfNeeded();
    if (output) await page.screenshot({path:path.join(output,"training-settings-desktop.png"),animations:"disabled"});
    await type(epochs,10);
    await type(batch,3);
    const name = quick.getByLabel("微調模型名稱");
    await name.fill("Browser setup");
    await quickToggle.click();
    assert.equal(await epochs.isVisible(),false);
    await quickToggle.click();
    assert.equal(await epochs.inputValue(),"10");
    assert.equal(await batch.inputValue(),"3");
    await quick.getByRole("button",{name:"還原預設數值"}).click();
    assert.equal(await epochs.inputValue(),"8");
    assert.equal(await batch.inputValue(),"2");
    assert.equal(await name.inputValue(),"Browser setup");
    assert.match(await quick.locator(".training-guidance").innerText(),/首次微調[\s\S]*少量素材[\s\S]*顯存不足[\s\S]*比較音質/);
    console.log("PASS: desktop controls clickable; help and reset preserve model name");

    await page.setViewportSize({width:390,height:600});
    await quick.locator("#dataset-files-input").setInputFiles({name:"test.wav",mimeType:"audio/wav",buffer:Buffer.from("mock reference")});
    await quick.getByText(/1 \/ 24 段 · 19.4/).waitFor();
    await type(epochs,0);
    await quickToggle.click();
    const run = quick.getByRole("button",{name:"一鍵生成及訓練",exact:true});
    await run.click();
    assert.equal(await quickToggle.getAttribute("aria-expanded"),"true");
    assert.equal(await epochs.evaluate(node=>document.activeElement===node),true);
    assert.equal(requests.length,0);
    await type(epochs,4);
    await type(batch,1);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth),390);
    if (output) await quick.locator(".training-options").screenshot({path:path.join(output,"training-settings-narrow.png"),animations:"disabled"});
    await Promise.all([page.waitForResponse(r=>r.url().endsWith("/datasets/auto") && r.status()===202),run.click()]);
    assert.deepEqual(requests[0].body.training,{name:"Browser setup",epochs:4,batch:1});
    console.log("PASS: 390px layout, invalid value refocus and exact quick-training POST");

    await page.setViewportSize({width:1280,height:670});
    await page.getByRole("button",{name:"手動校對",exact:true}).click();
    await page.locator("#training-select").selectOption(projectId);
    const bar = page.locator(".training-actionbar"), manualToggle = bar.getByRole("button",{name:label,exact:true});
    assert.equal(await bar.evaluate(node=>getComputedStyle(node).position),"sticky");
    await page.locator(".training-clip").last().scrollIntoViewIfNeeded();
    await manualToggle.click();
    assert.equal(await bar.evaluate(node=>getComputedStyle(node).position),"static");
    const manualEpochs = bar.getByLabel("訓練輪數",{exact:true});
    assert.equal(await manualEpochs.inputValue(),"8");
    await type(manualEpochs,12);
    await type(bar.getByLabel("批次大小",{exact:true}),3);
    await manualToggle.press("Enter");
    assert.equal(await bar.evaluate(node=>getComputedStyle(node).position),"sticky");
    await Promise.all([page.waitForResponse(r=>r.url().endsWith("/train") && r.status()===202),
      bar.getByRole("button",{name:"開始訓練",exact:true}).click()]);
    assert.equal(requests[1].body.options.epochs,12);
    assert.equal(requests[1].body.options.batch,3);
    assert.deepEqual(errors,[]);
    console.log("PASS: manual sticky toolbar, separate editable values and exact manual-training POST");
  } finally {
    await browser.close();
    if (server) server.kill();
  }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
