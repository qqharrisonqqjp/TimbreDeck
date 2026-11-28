// Playwright regression against an isolated Windows Studio, not a daily workspace.
const assert=require("node:assert/strict"), fs=require("node:fs"), path=require("node:path"), os=require("node:os");
const cp=require("node:child_process"), rl=require("node:readline");

async function main(){
  const [scenario,exe,config,fixture,out]=process.argv.slice(2);
  fs.mkdirSync(out,{recursive:true});
  const links=path.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const modulePath=process.env.TD_PLAYWRIGHT || fs.readdirSync(links)
    .map(x=>fs.readFileSync(path.join(links,x),"utf8").trim()).find(x=>x.endsWith("playwright-core")&&fs.existsSync(x));
  const data=fs.mkdtempSync(path.join(out,scenario+"-"));
  fs.mkdirSync(path.join(data,"config"));fs.copyFileSync(config,path.join(data,"config/engines.json"));
  const server=cp.spawn(exe,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","inherit"]});
  let browser;
  try{
    const url=await new Promise((resolve,reject)=>{
      rl.createInterface({input:server.stdout}).once("line",line=>resolve(line.trim().split(/\s+/).pop()));
      server.once("exit",code=>reject(Error("Studio exited "+code)));
    });
    browser=await require(modulePath).chromium.launch({headless:true,chromiumSandbox:true});
    const page=await browser.newPage({viewport:{width:1350,height:900}});
    const errors=[];
    page.on("pageerror",e=>errors.push(e.message));
    const outputs=[
      {id:"1".repeat(32),created:"2026-11-28T05:00:00Z",seconds:4,input:{engine:"x-vc",mode:"voice-convert",sourceSpeech:"synthetic"},audioAvailable:true},
      {id:"2".repeat(32),created:"2026-11-28T04:30:00Z",input:{engine:"future-legacy",text:"Legacy record"},audioAvailable:false},
      {id:"3".repeat(32),created:"2026-11-28T04:00:00Z",seconds:2,input:{engine:"gpt-sovits",mode:"clone",speed:1.2,splitAt:100,splitSeparate:true},
        timing:{resident:true,generationSeconds:0.64,totalSeconds:0.92},parts:3,partsAvailable:true,audioAvailable:false,fineTuneName:"QA Alpha"},
    ];
    await page.route("**/api/history",r=>r.fulfill({json:outputs}));
    await page.goto(url);
    await page.getByRole("button",{name:"輸出歷史",exact:true}).click();
    if(scenario==="baseline"||scenario==="rollback"){
      await page.locator("#notice").getByText(/Cannot read properties of undefined/).waitFor();
      console.log(scenario+" history error reproduced: Cannot read properties of undefined (reading '0')");
      return;
    }
    await page.getByRole("heading",{name:"X-VC · STS"}).waitFor();
    await page.getByRole("heading",{name:"future-legacy · 全文"}).waitFor();
    const split=page.getByRole("heading",{name:"GPT-SoVITS · 全文",exact:true}).locator("..");
    assert.equal(await split.getByText("不適用（模型已常駐）").isVisible(),false);
    await split.getByText("耗時、模型及設定").click();
    await split.getByText("不適用（模型已常駐）").waitFor();
    assert.equal(await split.getByRole("link",{name:"下載 3 段 WAV（ZIP）"}).getAttribute("href"),"/api/audio/"+"3".repeat(32)+"/parts");
    await page.getByLabel("類別").selectOption("sts");
    assert.equal(await page.getByRole("heading",{name:"future-legacy · 全文"}).count(),0);
    console.log("history: STS + legacy render; collapsed timing and independent split ZIP PASS");

    await page.getByRole("button",{name:"訓練素材",exact:true}).click();
    await page.locator(".dataset-auto").screenshot({path:path.join(out,"one-click-training.png")});
    await page.getByRole("button",{name:"手動校對",exact:true}).click();
    await page.getByRole("button",{name:"新增專案",exact:true}).click();
    await page.getByLabel("專案名稱",{exact:true}).fill("Browser archive test");
    await page.locator("#training-media").setInputFiles(fixture);
    const dialog=page.getByRole("dialog",{name:"匯入錄音選項"});
    await dialog.getByRole("checkbox",{name:"按停頓自動切分"}).isChecked();
    await dialog.getByRole("checkbox",{name:"自動辨識台詞"}).uncheck();
    await dialog.getByLabel("錄音語言").selectOption("en");
    await dialog.getByRole("button",{name:"匯入錄音",exact:true}).click();
    const first=page.locator(".training-clip").first();
    await page.waitForFunction(()=>document.querySelectorAll(".training-group .training-clip").length>=2);
    await first.getByRole("button",{name:"試聽片段",exact:true}).click();
    await first.locator("audio").waitFor({state:"visible"});
    await page.waitForFunction(()=>document.querySelector(".training-clip audio")?.duration>0);
    assert.match(await first.locator("audio").getAttribute("src"),/^blob:/);
    const transcript=first.getByLabel("台詞／逐字稿");
    await transcript.fill("The river flows quietly through the valley.");
    await first.getByLabel("用途").selectOption("train");
    const bar=page.locator(".training-actionbar");
    await bar.getByRole("button",{name:"開始訓練",exact:true}).waitFor();
    const settings=bar.getByRole("button",{name:"訓練設定",exact:true});
    assert.equal(await bar.evaluate(node=>getComputedStyle(node).position),"sticky");
    await settings.click();
    await bar.getByLabel("訓練輪數").fill("1");
    await bar.getByLabel("批次大小").fill("1");
    await bar.getByLabel("微調模型名稱").fill("Browser QA model");
    assert.equal(await bar.evaluate(node=>getComputedStyle(node).position),"static");
    await page.locator("#training-editor").screenshot({path:path.join(out,"training-settings.png")});
    let submitted;
    await page.route(/\/api\/training\/[\da-f]{32}\/train$/,r=>{
      submitted=r.request().postDataJSON();
      return r.fulfill({status:202,json:{id:"4".repeat(32),kind:"training",engine:"gpt-sovits",status:"running"}});
    });
    await bar.getByRole("button",{name:"開始訓練",exact:true}).click();
    await page.waitForResponse(r=>r.url().endsWith("/train") && r.status()===202).catch(()=>{if(!submitted)throw Error("missing train POST")});
    assert.equal(submitted.options.epochs,1);assert.equal(submitted.options.batch,1);
    assert.equal(submitted.options.name,"Browser QA model");assert.ok(submitted.revision>0);
    console.log("training: import dialog, source grouping, actual segment player, editable text and adjacent options PASS");

    await page.getByRole("button",{name:"TTS 擂臺",exact:true}).click();
    const add=page.getByRole("button",{name:"添加模型",exact:true});
    await add.click();await add.click();
    const cards=page.locator(".arena-config");
    assert.equal(await cards.count(),2);
    await page.getByLabel("共同台詞").fill("This is an arena comparison.");
    await page.getByLabel("參考逐字稿（留空可自動辨識）").fill("The river flows quietly through the valley.");
    await page.getByLabel("共同參考錄音").setInputFiles(fixture);
    await page.getByText(path.basename(fixture),{exact:true}).waitFor();
    await cards.first().getByLabel("語速").fill("0.95");
    await cards.last().getByLabel("語速").fill("1.30");
    const requests=[];
    let finishedJob=null,lastJobObserved=false;
    await page.route("**/api/job",r=>{if(finishedJob)lastJobObserved=true;return r.fulfill({json:finishedJob});});
    await page.route("**/api/synthesize",r=>{
      if (requests.length>0) assert.ok(lastJobObserved,"the previous row wasn't awaited");
      requests.push(r.request().postDataJSON());
      const ack={id:String(requests.length+4).repeat(32),kind:"synthesis",engine:"gpt-sovits",status:"running"};
      finishedJob={...ack,status:"done"};lastJobObserved=false;
      return r.fulfill({status:202,json:ack});
    });
    await page.getByRole("button",{name:"開始比較",exact:true}).click();
    await page.waitForFunction(()=>document.querySelectorAll(".arena-result audio").length===2);
    assert.deepEqual(requests.map(x=>[x.engine,x.speed]),[["gpt-sovits",0.95],["gpt-sovits",1.3]]);
    assert.deepEqual(errors,[]);
    await page.screenshot({path:path.join(out,"arena.png")});
    fs.writeFileSync(path.join(out,"browser-result.json"),JSON.stringify({requests,errors,segmentPlayback:true,legacyHistory:true},null,2));
    console.log("arena: same model twice, independent settings, sequential jobs and playable results PASS");
  }finally{if(browser)await browser.close();server.kill();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});
