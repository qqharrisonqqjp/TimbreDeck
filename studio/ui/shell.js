"use strict";
const sections=[
  ["工作區",[["synth","語音合成","◉","選擇角色同引擎，將台詞變成聲音。"],
    ["sts","流式 STS","≋","保留原句內容，改用目標音色；支援錄音與即時收音。"],
    ["voices","聲音庫","◯","集中管理角色、參考錄音同微調模型。"],
    ["clean","去除雜聲","✦","批量清理音訊，依順序合併。"],
    ["training","訓練素材","▤","選擇快速訓練，或逐段校對再訓練。"]]],
  ["比較",[["tts","TTS 擂臺","◫","同一句台詞，逐個比較聲線同引擎設定。"],
    ["asr","ASR 擂臺","≡","同一段錄音，並排比較辨識結果。"]]],
  ["管理",[["models","模型管理","▥","連接引擎、安裝權重及管理微調模型。"],
    ["history","輸出歷史","◷","繼續試聽、比較同匯出，以前嘅設定亦可以查返。"],
    ["storage","設定","⚙","管理聲台偏好、版本同儲存空間。"]]]
];
const routesByKey=Object.fromEntries(sections.flatMap(([,routes])=>routes.map(route=>[route[0],route])));
let workspace="synth";
const panel=()=>workspace;
function restoreWorkspace() {
  const previous=draft.workspace || d().tab;
  workspace=Object.hasOwn(routesByKey,previous)?previous:"synth";
  if(workspace==="synth" && !integrated(draft.engine)) workspace="models";
}
function setDrawer(open,returnFocus=false) {
  document.body.classList.toggle("drawer-open",open);
  $("nav-toggle").setAttribute("aria-expanded",String(open));
  if(returnFocus) $("nav-toggle").focus();
}
function drawSections() {
  const host=$("sections");
  if(!host.childNodes.length) {
    for(const [label,items] of sections) {
      host.append(el("small",{},label));
      for(const [key,title,icon] of items) {
        const node=btn("",()=>setTab(key,true));
        node.dataset.workspace=key;
        node.append(el("span",{className:"nav-icon","aria-hidden":"true"},icon),el("span",{},title));
        host.append(node);
      }
    }
    const exit = btn("",()=>{
      $("exit-feedback").textContent="";
      $("studio-exit-dialog").showModal();
    },{id:"studio-exit", "aria-haspopup":"dialog"});
    exit.append(el("span",{className:"nav-icon","aria-hidden":"true"},"⏻"),el("span",{},"結束工作室"));
    host.append(exit);
  }
  for(const item of host.querySelectorAll("button")) {
    if(item.dataset.workspace===workspace) item.setAttribute("aria-current","page");
    else item.removeAttribute("aria-current");
  }
  $("engine-open").hidden=!["synth","models"].includes(workspace);
  $("engine-open").textContent=(catalog[draft.engine]?.[0]||"合成引擎")+" ▾";
}
function showWorkspaceHeadline() {
  const key=workspace, before=document.body.dataset.workspace;
  document.body.dataset.workspace=key;
  document.body.dataset.motion=draft.reduceMotion?"reduce":"system";
  $("title").textContent=routesByKey[key][1];
  $("subtitle").textContent=routesByKey[key][3];
  const showEngine=key==="synth"||key==="models";
  $("state").hidden=!showEngine;
  if(showEngine) {
    const engine=engines[draft.engine];
    $("state").textContent=!engine?.engineInstalled?"未連接引擎":!engine.ready?"缺少模型":"模型已就緒";
  }
  document.body.dataset.recording=mic?"true":"false";
  if(before!==key && $("work").animate && !draft.reduceMotion && matchMedia("(prefers-reduced-motion: no-preference)").matches) {
    $("work").getAnimations().forEach(animation=>animation.cancel());
    $("work").animate([{opacity:.78},{opacity:1}],{duration:160,easing:"ease-out"});
  }
  scheduleDepth();
}
let depthFrame=0;
function scheduleDepth() {
  if(depthFrame) return;
  depthFrame=requestAnimationFrame(()=>{
    depthFrame=0;
    const enabled=!draft?.reduceMotion && !mic && matchMedia("(min-width: 900px) and (pointer: fine) and (prefers-reduced-motion: no-preference)").matches;
    $("hero-depth").style.transform="translateY("+(enabled?Math.min(8,window.scrollY*.045):0)+"px)";
  });
}
window.addEventListener("scroll",scheduleDepth,{passive:true});
window.addEventListener("resize",scheduleDepth,{passive:true});
matchMedia("(prefers-reduced-motion: reduce)").addEventListener("change",()=>{
  if(matchMedia("(prefers-reduced-motion: reduce)").matches) $("work").getAnimations().forEach(animation=>animation.cancel());
  scheduleDepth();
});
function mountShell() {
  $("nav-toggle").onclick=()=>setDrawer(!document.body.classList.contains("drawer-open"));
  document.addEventListener("keydown",ev=>{
    if(ev.key==="Escape" && !document.querySelector("dialog[open]") && document.body.classList.contains("drawer-open")) setDrawer(false,true);
  });
  $("studio-exit-dialog").addEventListener("cancel",ev=>{if(studioExitPending)ev.preventDefault()});
  $("exit-confirm").onclick=exitStudio;
  $("mini-close").onclick=()=>{
    const audio=$("mini-audio");
    audio.pause();audio.removeAttribute("src");audio.load();
    $("mini-player").hidden=true;
  };
}

let studioExitPending=false, studioExited=false;
async function exitStudio() {
  if (studioExitPending || studioExited) return;
  const feedback=$("exit-feedback"), yes=$("exit-confirm"), no=$("exit-cancel");
  if (mic) { feedback.textContent="正在使用咪高峰。請先停止收音，再結束工作室。"; return; }
  if (arena.tts.busy || arena.asr.busy || datasetState.importing || datasetState.submitting || trainingState.importing || trainingState.queued) {
    feedback.textContent="有作業進行中。請等完成，再結束工作室。";
    return;
  }
  studioExitPending=yes.disabled=no.disabled=true;
  feedback.textContent="正在儲存草稿及結束工作室…";
  try {
    if (jobActive(await api("job"))) throw Error("有作業進行中。請等完成，再結束工作室。");
    if (trainingState.dirty) await saveReview(trainingProject,true);
    if (revision!==savedRevision && !(await persist())) throw Error("草稿未能儲存。工作室仍然開啟，請先處理草稿再試。");
    if (!(await api("studio/exit","POST"))?.stopping) throw Error("未收到結束確認。請先檢查工作室狀態。");
    studioExited=true;
    renderID++;
    $("studio-exit-dialog").close();
    document.querySelectorAll("audio").forEach(audio=>audio.pause());
    stopSTSPreview();
    for(const key of ["sidebar","nav-toggle","engine-open","state","activity","saved"]) $(key).hidden=true;
    $("notice").textContent="";
    $("title").textContent="工作室已結束";
    $("subtitle").textContent="可以關閉此分頁。要再次使用，請雙擊 Open-TimbreDeck.cmd。";
    $("work").replaceChildren();
    $("title").focus();
  } catch(e) {
    feedback.textContent=e.message;
  } finally {
    studioExitPending=yes.disabled=no.disabled=false;
  }
}
async function playInDock(record) {
  const audio=$("mini-audio"), url="/api/audio/"+record.id;
  if(audio.getAttribute("src")!==url) audio.src=url;
  $("mini-title").textContent=record.voiceName || engineName(record.input?.engine);
  $("mini-player").hidden=false;
  await audio.play();
}
document.addEventListener("play",event=>{
  if(!(event.target instanceof HTMLAudioElement))return;
  for(const audio of document.querySelectorAll("audio")) {
    if(audio!==event.target && !audio.paused) audio.pause();
  }
},true);

const jobActive = job=>["running","cancelling"].includes(job?.status);
async function waitForJob(ack,{terminal=false,timeout=30*60*1000}={}) {
  if(!ack?.id) return ack;
  const end=performance.now()+timeout;
  let status=ack;
  while(jobActive(status)) {
    if(performance.now()>=end) throw Error("等候逾時；作業仍可喺作業島或輸出歷史查看。");
    await new Promise(resolve=>setTimeout(resolve,550));
    try { status=await api("jobs/"+ack.id); }
    catch(e) {
      if(e.status!==404)throw e;
      const latest=await api("job");
      if(latest?.id!==ack.id)throw Error("找不到此作業狀態；已輸出嘅檔案仍可喺歷史查看。");
      status=latest;
    }
  }
  if(!terminal && status.status!=="done")throw Error(status.detail || (status.status==="cancelled"?"已取消":"作業未完成。"));
  return status;
}
