// Isolated browser test for cancellation and the compare-and-switch request.
const fs=require("node:fs"), path=require("node:path"), os=require("node:os");
const cp=require("node:child_process"), rl=require("node:readline"), assert=require("node:assert/strict");
(async()=>{
  const [exe,config,out]=process.argv.slice(2);
  fs.mkdirSync(out,{recursive:true});
  const data=fs.mkdtempSync(path.join(out,"dialog-"));
  fs.mkdirSync(path.join(data,"config"));fs.copyFileSync(config,path.join(data,"config/engines.json"));
  const links=path.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const pw=fs.readdirSync(links).map(x=>fs.readFileSync(path.join(links,x),"utf8").trim()).find(x=>x.endsWith("playwright-core")&&fs.existsSync(x));
  const server=cp.spawn(exe,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","pipe"]});
  let browser;
  try {
    const url=await new Promise((resolve,reject)=>{
      rl.createInterface({input:server.stdout}).once("line",s=>resolve(s.trim().split(/\s+/).pop()));
      server.once("exit",reject);
    });
    browser=await require(pw).chromium.launch({headless:true,chromiumSandbox:true});
    const page=await browser.newPage();
    let active={id:"old-slot",kind:"tts",engine:"qwen3-tts",variant:"Base",busy:false};
    const warm=[], prompts=[], errors=[];
    page.on("pageerror",error=>errors.push(error.message));
    let yes=false;
    page.on("dialog",async d=>{prompts.push(d.message());if(yes)await d.accept();else await d.dismiss()});
    await page.route("**/api/tts/resident",r=>r.fulfill({json:active}));
    await page.route("**/api/tts/warm",r=>{
      const q=r.request().postDataJSON();warm.push(q);
      active={id:"new-slot",kind:"tts",engine:q.engine,variant:q.variant,busy:false};
      return r.fulfill({json:active});
    });
    await page.goto(url);
    const control=page.getByRole("button",{name:"開始啟動 TTS 模型",exact:true});
    await page.getByText(/常駐中：Qwen3-TTS/).waitFor();
    await control.click();await page.waitForFunction(()=>!document.querySelector("#tts-resident-status").parentElement.querySelector("button").disabled);
    assert.equal(prompts.length,1);
    assert.equal(warm.length,0);
    yes=true;
    await control.click();
    await page.getByText(/常駐中：GPT-SoVITS/).waitFor();
    assert.equal(prompts.length,2);
    assert.deepEqual(warm,[{engine:"gpt-sovits",variant:"v2Pro",modelProject:"",replace:"old-slot"}]);
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(out,"dialog.json"),JSON.stringify({prompts,warm,errors},null,2));
    console.log("DIALOG PASS cancel=no POST; confirmed switch matches the old resident ID");
  }finally{if(browser)await browser.close();server.kill();}
})().catch(e=>{console.error(e);process.exitCode=1});
