// Isolated browser, CLI and real PCM playback check. No live StudioData.
const fs=require("node:fs"),path=require("node:path"),os=require("node:os");
const cp=require("node:child_process"),rl=require("node:readline"),assert=require("node:assert/strict");
const crypto=require("node:crypto"),{promisify}=require("node:util");

async function main(){
  const [exe, assets, source, reference, out, python]=process.argv.slice(2);
  const links=path.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const playwright=fs.readdirSync(links).map(x=>fs.readFileSync(path.join(links,x),"utf8").trim())
    .find(p=>p.endsWith("playwright-core")&&fs.existsSync(p));
  fs.mkdirSync(out,{recursive:true});
  const data=fs.mkdtempSync(path.join(out,"studio-"));
  const server=cp.spawn(exe,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","pipe"],
    env:{...process.env,TIMBREDECK_STS_ASSETS:assets}});
  server.stderr.pipe(fs.createWriteStream(path.join(out,"browser-studio.log")));
  let browser;
  try{
    const url=await new Promise((resolve,reject)=>{
      rl.createInterface({input:server.stdout}).once("line",s=>resolve(s.trim().split(/\s+/).pop()));
      server.once("exit",n=>reject(Error("Studio exited "+n)));
    });
    assert.match(url,/^http:\/\/127\.0\.0\.1:\d+$/);
    browser=await require(playwright).chromium.launch({headless:true,chromiumSandbox:true,
      args:["--autoplay-policy=no-user-gesture-required"]});
    const page=await browser.newPage({acceptDownloads:true,viewport:{width:1440,height:900}});
    const errors=[];
    page.on("pageerror",e=>errors.push(e.message));
    await page.addInitScript(()=>{
      const start=AudioBufferSourceNode.prototype.start;
      AudioBufferSourceNode.prototype.start=function(...args){
        (globalThis.__pcs ||= []).push([this.buffer.duration,this.context.state]);
        return start.apply(this,args);
      };
    });
    await page.goto(url);
    await page.locator("#sections").getByRole("button",{name:"流式 STS",exact:true}).click();
    const choices=page.locator("#sts-model");
    await choices.locator("option[value='tvtsyn1']:text-is('TVTSyn1')").waitFor({state:"attached"});
    assert.deepEqual(await choices.locator("option").evaluateAll(o=>o.map(x=>x.value)),
      ["cosyvoice3","x-vc","seed-vc-tiny","meanvc2","tvtsyn1"]);
    await choices.selectOption("seed-vc-tiny");
    await page.locator("#sts-source").setInputFiles(source);
    await page.getByText(new RegExp(path.basename(source).replaceAll(".","\\.")+" ·")).waitFor();
    await page.locator("#sts-reference").setInputFiles(reference);
    await page.getByText(new RegExp(path.basename(reference).replaceAll(".","\\.")+" ·")).waitFor();
    const mic=page.getByRole("button",{name:"啟動咪高峰",exact:true});
    assert.equal(await mic.isDisabled(),true);
    await page.getByRole("button",{name:"開始轉換及試聽",exact:true}).click();
    await page.getByText(/開始串流試聽 · 首段/).waitFor({timeout:150000});
    const packets=await page.evaluate(()=>globalThis.__pcs);
    assert.ok(packets.length&&packets.every(([len,state])=>len>0&&state==="running"));
    const link=page.getByRole("link",{name:"下載完整 WAV",exact:true});
    const downloading=page.waitForEvent("download");
    await link.click({timeout:20000});
    const uiWav=path.join(out,"seed-vc-tiny-ui.wav");
    await (await downloading).saveAs(uiWav);
    await page.screenshot({path:path.join(out,"sts-models.png")});
    const agent=path.join(__dirname,"../tools/td_agent.py");
    const args=[agent,"--url",url];
    const run=async more=>JSON.parse((await promisify(cp.execFile)(python,[...args,...more],
      {encoding:"utf8",timeout:100000,windowsHide:true})).stdout);
    const capabilities=await run(["capabilities"]);
    assert.equal(capabilities.stsModels.filter(x=>x.ready).length,4);
    const result=await run(["sts","--source",source,"--reference",reference,"--model","seed-vc-tiny","--output-dir",out]);
    assert.equal(result.ok,true);
    const artifact=result.artifacts[0];
    const sha=crypto.createHash("sha256").update(fs.readFileSync(artifact.path)).digest("hex");
    assert.equal(sha,artifact.sha256);
    assert.deepEqual(errors,[]);
    const report={choices:capabilities.stsModels,packets,artifact,uiWav,errors,isolatedData:data};
    fs.writeFileSync(path.join(out,"browser-cli.json"),JSON.stringify(report,null,2));
    console.log("UI+CLI PASS",uiWav,artifact.path);
  } finally {
    if(browser)await browser.close();
    server.kill();
  }
}
main().catch(e=>{console.error(e);process.exitCode=1});
