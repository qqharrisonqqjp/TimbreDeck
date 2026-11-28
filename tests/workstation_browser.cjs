// Real Chromium + Studio source. All API requests are intercepted in memory.
const fs=require("node:fs"), p=require("node:path"), os=require("node:os"), assert=require("node:assert/strict");
const cp=require("node:child_process"), rl=require("node:readline");
let pw;
try { pw=require("playwright"); } catch {
  const links=p.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const base=fs.readdirSync(links).map(x=>fs.readFileSync(p.join(links,x),"utf8").trim()).find(x=>x.endsWith("playwright-core")&&fs.existsSync(x));
  pw=require(base);
}
const baseline=process.argv[2]==="baseline", ui=p.join(process.argv[3]||p.resolve(__dirname,".."),"studio","ui");
const id=(x,n=32)=>x.repeat(n), gpt="gpt-sovits", cosy="cosyvoice3";
const fixture = {
  draft:{engine:cosy,text:"Synthetic UI audit",drafts:{}},build:{version:"test-version",id:"test-build"},
  engines:{
    [gpt]:{ready:true,engineInstalled:true,variants:["v2Pro","v5turbo","v5dev"],availableVariants:["v2Pro","v5turbo","v5dev"],languageOptions:["yue","en","ja"],speed:true,modes:[{id:"clone",label:"Clone",reference:true,transcript:true,languages:true}]},
    [cosy]:{ready:true,engineInstalled:true,variants:["Fun-CosyVoice3-0.5B"],availableVariants:["Fun-CosyVoice3-0.5B"],speed:true,modes:[{id:"cross",label:"Cross-language",reference:true}]},
  },
};
let job=null, trainingWrites=0;
const records=[{id:id("a"),created:"2026-11-28T01:00:00Z",seconds:8,sampleRate:16000,audioAvailable:true,input:{engine:cosy,text:"Synthetic output",mode:"cross"}}];
let project={id:id("b"),name:"Synthetic review",revision:1,clips:[{id:id("c"),reference:id("d",64),sourceName:"synthetic.wav",startMs:0,endMs:1000,language:"en",transcript:"Saved transcript",use:"pending"}]};
const mime={".html":"text/html; charset=utf-8",".js":"text/javascript; charset=utf-8",".css":"text/css; charset=utf-8",".svg":"image/svg+xml"};
const rate=16000,samples=rate*8,wav=Buffer.alloc(44+samples*2);
wav.write("RIFF",0);wav.writeUInt32LE(wav.length-8,4);wav.write("WAVEfmt ",8);wav.writeUInt32LE(16,16);wav.writeUInt16LE(1,20);wav.writeUInt16LE(1,22);wav.writeUInt32LE(rate,24);wav.writeUInt32LE(rate*2,28);wav.writeUInt16LE(2,32);wav.writeUInt16LE(16,34);wav.write("data",36);wav.writeUInt32LE(samples*2,40);
for(let i=0;i<samples;i++)wav.writeInt16LE(Math.floor(Math.sin(i*Math.PI/20)*500),44+i*2);

(async()=>{
  const browser=await pw.chromium.launch({headless:true,chromiumSandbox:true});
  let server=null, origin="http://timbredeck.audit.test";
  try{
    if(process.env.TD_BROWSER_EXE){
      const data=process.env.TD_BROWSER_DATA;
      assert.ok(data && p.isAbsolute(data) && !fs.existsSync(data));
      server=cp.spawn(process.env.TD_BROWSER_EXE,["-no-browser","-data",data],{windowsHide:true,stdio:["ignore","pipe","inherit"]});
      origin=await new Promise((resolve,reject)=>{
        rl.createInterface({input:server.stdout}).once("line",line=>resolve(line.trim().split(/\s+/).pop()));
        server.once("error",reject);server.once("exit",code=>reject(Error("Studio exited "+code)));
      });
      assert.match(origin,/^http:\/\/127\.0\.0\.1:[0-9]+$/);
    }
    const page=await browser.newPage({viewport:{width:1440,height:704},reducedMotion:"no-preference"});
    const errors=[];page.on("pageerror",e=>errors.push(e.message));
    await page.route("**/*",async r=>{
      const u=new URL(r.request().url()), key=u.pathname;
      if(u.origin!==origin)return r.abort();
      if(key==="/api/history")return r.fulfill({json:records});
      if(key.startsWith("/api/history/"))return r.fulfill({json:records.find(row=>row.id===key.split("/").pop())});
      if(key==="/api/job")return r.fulfill({json:job});
      if(key.startsWith("/api/audio/"))return r.fulfill({contentType:"audio/wav",body:wav});
      if(key==="/api/draft"&&r.request().method()==="PUT"){fixture.draft=r.request().postDataJSON();return r.fulfill({json:{}});}
      if(key===("/api/training/"+project.id)&&r.request().method()==="PUT"){
        const input=r.request().postDataJSON();trainingWrites++;
        if(input.revision!==project.revision)return r.fulfill({status:409,json:{error:"Updated elsewhere"}});
        project={...input,revision:input.revision+1};
        return r.fulfill({json:project});
      }
      if(key==="/api/state")return r.fulfill({json:fixture});
      if(key==="/api/training")return r.fulfill({json:[project]});
      if(key==="/api/tts/resident")return r.fulfill({json:{busy:false}});
      if(key.startsWith("/api/"))return r.fulfill({json:[]});
      if(server)return r.continue();
      const name=key==="/"?"index.html":key.slice(1);
      if(!/^[a-z0-9.-]+$/.test(name))return r.abort();
      const path=p.join(ui,name);
      return fs.existsSync(path)?r.fulfill({body:fs.readFileSync(path),contentType:mime[p.extname(name)]}):r.fulfill({status:404,body:""});
    });
    await page.goto(origin);
    const side=name=>page.locator("#sections").getByRole("button",{name,exact:true});
    const engine=page.locator("#engine-open");
    await side("語音合成").waitFor();
    assert.match(await engine.innerText(),/CosyVoice/);
    await side("訓練素材").click();
    if(!baseline) {
      assert.equal(await engine.isVisible(),false);
      assert.equal(await page.locator("#title").innerText(),"訓練素材");
      assert.equal(await page.getByRole("button",{name:"一鍵生成及訓練"}).isVisible(),true);
    }
    await side("語音合成").click();
    assert.match(await engine.innerText(),baseline?/GPT-SoVITS/:/CosyVoice/);
    if(!baseline && process.env.TD_CAPTURE_UI) {
      await page.mouse.move(860,25);
      await page.screenshot({path:process.env.TD_CAPTURE_UI,animations:"disabled"});
    }
    console.log(baseline?"BASELINE: training switches synthesis to GPT-SoVITS":"PASS: workspace navigation keeps CosyVoice");

    await side("輸出歷史").click();
    await page.locator("#work audio").first().waitFor();
    const previous=await page.locator("#work audio").first().elementHandle();
    await page.locator("#work details summary").first().click();
    await previous.evaluate(a=>a.play());
    assert.equal(await previous.evaluate(a=>a.paused),false);
    records.unshift({id:id("e"),created:"2026-11-28T01:01:00Z",seconds:8,audioAvailable:true,input:{engine:gpt,text:"New background output"}});
    job={id:id("e"),kind:"synthesis",engine:gpt,status:"done"};
    await page.getByText("New background output",{exact:true}).waitFor();
    assert.equal(await previous.evaluate(n=>n.isConnected),!baseline);
    assert.equal(await page.locator("#work details[open]").count(),baseline?0:1);
    if(!baseline) assert.equal(await previous.evaluate(a=>a.paused),false);
    console.log(baseline?"BASELINE: a background job replaces the active history player":"PASS: new result keeps playing audio node and open details");

    await side("訓練素材").click();
    if(!baseline)await page.getByRole("button",{name:"手動校對",exact:true}).click();
    await page.locator("#training-select").selectOption(project.id);
    const transcript=page.locator(".training-clip textarea").first();
    await transcript.fill("Unsaved review");
    if(!baseline)await page.waitForFunction(()=>document.querySelector("#training-summary")?.textContent.includes("已儲存")&&!document.querySelector("#training-summary")?.textContent.includes("未儲存"));
    await page.reload();
    await page.locator("#training-select").selectOption(project.id);
    assert.equal(await transcript.inputValue(),baseline?"Saved transcript":"Unsaved review");
    if(!baseline){
      await transcript.fill("Immediately refreshed");
      await page.reload();
      await transcript.waitFor();
      assert.equal(await transcript.inputValue(),"Immediately refreshed");
      await page.waitForFunction(()=>document.querySelector("#training-summary")?.textContent.includes("已儲存")&&!document.querySelector("#training-summary")?.textContent.includes("未儲存"));
      project={...project,revision:project.revision+1,clips:[{...project.clips[0],transcript:"External edit"}]};
      await transcript.fill("Local conflict copy");
      await page.getByRole("button",{name:"另存草稿副本",exact:true}).waitFor();
      assert.equal(project.clips[0].transcript,"External edit");
      assert.equal(await transcript.inputValue(),"Local conflict copy");
      assert.ok(trainingWrites>=2);
    }else assert.equal(trainingWrites,0);
    console.log(baseline?"BASELINE: refreshing loses manual transcript edits":"PASS: autosave and immediate refresh recover edits; conflict preserves both versions");
    if(!baseline){
      await side("輸出歷史").click();
      await page.evaluate(()=>{
        document.querySelector("#work").style.minHeight="1100px";
        window.scrollTo(0,220);
      });
      await page.waitForFunction(()=>window.scrollY>0 && document.querySelector("#hero-depth").style.transform!=="translateY(0px)");
      await page.emulateMedia({reducedMotion:"reduce"});
      await page.waitForFunction(()=>document.querySelector("#hero-depth").style.transform==="translateY(0px)");
      console.log("PASS: decorative parallax responds only while motion is enabled");
      await side("語音合成").click();
      await engine.click();
      await page.locator("#engine-dialog").getByRole("button",{name:"GPT-SoVITS",exact:true}).click();
      const selector=page.getByLabel("GPT-SoVITS 基礎版本");
      await page.getByRole("button",{name:"匯入微調模型 ZIP"}).waitFor();
      for(const model of ["v5turbo","v5dev"]){
        await selector.selectOption(model);
        assert.equal(await page.getByRole("button",{name:"匯入微調模型 ZIP"}).count(),0);
        assert.equal(await page.locator("#trained-model").count(),0);
        const target=await page.evaluate(()=>selectedTTS("gpt-sovits",d(),engines[draft.engine],selectedMode(engines[draft.engine],d().mode)));
        assert.deepEqual([target.variant,target.modelProject],[model,""]);
      }
      await selector.selectOption("v2Pro");
      await page.getByRole("button",{name:"匯入微調模型 ZIP"}).waitFor();
      console.log("PASS: both V5 variants hide fine-tuning and keep v2Pro controls intact");
    }
    await page.setViewportSize({width:390,height:844});
    if(!baseline){
      assert.equal(await page.locator("#sidebar").isVisible(),false);
      await page.locator("#nav-toggle").click();
      await side("輸出歷史").waitFor();
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#sidebar").isVisible(),false);
      assert.equal(await page.locator("#nav-toggle").evaluate(n=>n===document.activeElement),true);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth),390);
      console.log("PASS: 390 px drawer keeps all workspaces reachable; Escape returns focus");
    }
    assert.deepEqual(errors,[]);
  }finally{await browser.close();if(server)server.kill();}
})().catch(e=>{console.error(e);process.exitCode=1;});
