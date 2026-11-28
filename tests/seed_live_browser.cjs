// Replay the user-selected WAV through Chromium's fake mic in an isolated Studio.
const fs=require("node:fs"), path=require("node:path"), os=require("node:os");
const cp=require("node:child_process"), rl=require("node:readline"), assert=require("node:assert/strict");

async function main(){
  const [exe, assets, source, reference, out] = process.argv.slice(2);
  const links=path.join(os.homedir(),"AppData/Local/ms-playwright/.links");
  const pw=fs.readdirSync(links).map(x=>fs.readFileSync(path.join(links,x),"utf8").trim())
    .find(p=>p.endsWith("playwright-core")&&fs.existsSync(p));
  fs.mkdirSync(out,{recursive:true});
  const db=fs.mkdtempSync(path.join(out,"browser-"));
  const fake=path.join(db,"mic-input.wav");
  cp.execFileSync("ffmpeg",["-nostdin","-v","error","-i",source,"-ar","16000","-ac","1","-c:a","pcm_s16le",fake]);
  const server=cp.spawn(exe,["-no-browser","-data",db],{windowsHide:true,stdio:["ignore","pipe","pipe"],
    env:{...process.env,TIMBREDECK_STS_ASSETS:assets}});
  server.stderr.pipe(fs.createWriteStream(path.join(out,"browser-server.log")));
  let browser;
  try {
    const url=await new Promise((ok,err)=>{
      rl.createInterface({input:server.stdout}).once("line",l=>ok(l.trim().split(/\s+/).pop()));
      server.once("exit",n=>err(Error("Studio exited "+n)));
    });
    assert.match(url,/^http:\/\/127\.0\.0\.1:\d+$/);
    browser=await require(pw).chromium.launch({headless:true,chromiumSandbox:true,args:[
      "--mute-audio","--use-fake-device-for-media-stream","--use-fake-ui-for-media-stream",
      "--use-file-for-fake-audio-capture="+fake+"%noloop"]});
    const page=await browser.newPage({permissions:["microphone"]});
    const failures=[], packets=[];
    page.on("pageerror",e=>failures.push(e.message));
    page.on("response",r=>{
      const u=new URL(r.url());
      if(u.pathname==="/api/sts/live")packets.push({seq:Number(u.searchParams.get("seq")),status:r.status()});
    });
    await page.addInitScript(()=>{
      const start=AudioBufferSourceNode.prototype.start;
      globalThis.output=[];
      AudioBufferSourceNode.prototype.start=function(...args){
        globalThis.output.push({duration:this.buffer.duration,context:this.context.state,when:args[0]});
        return start.apply(this,args);
      };
    });
    await page.goto(url);
    await page.locator("#sections").getByRole("button",{name:"流式 STS",exact:true}).click();
    await page.locator("#sts-model option[value='seed-vc-tiny']").waitFor({state:"attached"});
    await page.locator("#sts-model").selectOption("seed-vc-tiny");
    await page.locator("#sts-seed-mode").selectOption("realtime");
    await page.locator("#sts-reference").setInputFiles(reference);
    await page.getByText(path.basename(reference),{exact:false}).waitFor();
    await page.getByRole("button",{name:"開始啟動 STS 模型",exact:true}).click();
    await page.getByText("模型已啟動及預熱。",{exact:true}).waitFor({timeout:150000});
    const start=Date.now();
    await page.getByRole("button",{name:"啟動咪高峰",exact:true}).click();
    await page.waitForFunction(()=>document.body.dataset.recording==="true" &&
      getComputedStyle(document.querySelector("#hero-depth")).transform==="none");
    await page.waitForFunction(()=>{
      const m=document.querySelector("#sts-mic")?.textContent.match(/已轉換 (\d+) 段/);
      return !!m&&Number(m[1])>=77;
    },null,{timeout:44000});
    const elapsed=(Date.now()-start)/1000;
    const output=await page.evaluate(()=>globalThis.output);
    await page.getByRole("button",{name:"停止咪高峰",exact:true}).click();
    assert.ok(output.length>=77&&output.every(x=>x.duration>0&&x.context==="running"));
    assert.equal(packets.length>=77,true);
    packets.slice(0,77).forEach((row,i)=>assert.deepEqual(row,{seq:i,status:200}));
    assert.equal((await (await fetch(url+"/api/history")).json()).length,0);
    await page.getByRole("button",{name:"停止 STS 模型",exact:true}).click();
    await page.getByText("STS 模型已卸載。",{exact:true}).waitFor();
    assert.equal((await (await fetch(url+"/api/sts/worker")).json()).resident,false);
    assert.deepEqual(failures,[]);
    const report={elapsed,packets,playbackNodes:output.length,physicalDevice:false,unloaded:true,workspace:db};
    fs.writeFileSync(path.join(out,"seed-browser.json"),JSON.stringify(report,null,2));
    console.log("Chromium fake-microphone PASS",packets.length,"packets",output.length,"playbacks",elapsed,"seconds");
  } finally {
    if(browser)await browser.close();
    server.kill();
  }
}
main().catch(e=>{console.error(e);process.exitCode=1});
