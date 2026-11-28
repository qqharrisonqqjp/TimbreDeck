"use strict";
const reviewDraft = {sequence:0,timer:0,flight:null,error:"",conflict:false};
const reviewKey = key => "TimbreDeck.review."+key;
function reviewRead(key) {
  try { return JSON.parse(sessionStorage.getItem(reviewKey(key)) || "null"); } catch { return null; }
}
function reviewWrite(key,value) {
  try {
    if (value===null) sessionStorage.removeItem(reviewKey(key));
    else sessionStorage.setItem(reviewKey(key),JSON.stringify(value));
  } catch {}
}
function rememberReview() {
  const p=trainingProject;
  if(p && trainingState.dirty) reviewWrite("draft."+(p.id||"new"),p);
}
window.addEventListener("pagehide",rememberReview);
function openReview(project,recover=true) {
  rememberReview();
  clearTimeout(reviewDraft.timer);
  const key=project?.id || "new";
  const recovered=recover ? reviewRead("draft."+key) : null;
  trainingProject=recovered || (project ? structuredClone(project) : {name:"",clips:[]});
  trainingState.dirty=!!recovered;
  reviewDraft.sequence++;
  reviewDraft.conflict=!!(recovered && project && recovered.revision!==project.revision);
  reviewDraft.error="";
  reviewWrite("selected",key);
  if(trainingState.dirty && !reviewDraft.conflict) scheduleReviewSave();
}
function resumeReview(projects) {
  if(trainingProject) return;
  const key=reviewRead("selected");
  if(key==="new" && reviewRead("draft.new")) openReview(null);
  else {
    const saved=projects.find(p=>p.id===key);
    if(saved) openReview(saved);
  }
}
function scheduleReviewSave() {
  clearTimeout(reviewDraft.timer);
  if(reviewDraft.conflict) return;
  reviewDraft.timer=setTimeout(()=>{
    rememberReview();
    if(!trainingState.queued && !trainingLocked(trainingProject) && trainingProject?.name.trim()) {
      saveReview(trainingProject).catch(()=>{});
    }
  },450);
}
function reviewChanged() {
  reviewDraft.sequence++;
  reviewDraft.error="";
  trainingState.dirty=true;
  scheduleReviewSave();
  syncTrainingControls();
}
function reviewProblem(project) {
  if(!project.name.trim()) return "請先輸入專案名稱。";
  for(const clip of project.clips) {
    if(!Number.isInteger(clip.startMs)||!Number.isInteger(clip.endMs)||clip.startMs<0||clip.endMs<=clip.startMs) return "有片段起訖時間無效。";
    if(/[|\r\n]/.test(clip.transcript)) return "逐字稿請用單行，直線符號可改用全形「｜」。";
  }
  return "";
}
async function saveReview(project,explicit=false) {
  if(!project || project!==trainingProject) throw Error("請重新選擇專案再試。");
  if(reviewDraft.flight) {
    await reviewDraft.flight.catch(()=>{});
    if(project!==trainingProject) throw Error("目前已切換專案。");
  }
  if(reviewDraft.conflict) throw Error("專案喺另一個視窗有更新。請先重新載入，或將呢份草稿另存。");
  if(project.id && !trainingState.dirty) return project;
  const bad=reviewProblem(project);
  if(bad) {
    reviewDraft.error=bad;
    syncTrainingControls();
    throw Error(bad);
  }
  for(const clip of project.clips) clip.id ||= crypto.randomUUID().replaceAll("-","");
  const snapshot=structuredClone(project), sequence=reviewDraft.sequence, oldKey=project.id||"new";
  const save=api(snapshot.id ? "training/"+snapshot.id : "training",snapshot.id?"PUT":"POST",snapshot);
  reviewDraft.flight=save;
  syncTrainingControls();
  try {
    const saved=await save;
    project.id=saved.id;project.revision=saved.revision;
    trainingState.projects=[...trainingState.projects.filter(p=>p.id!==saved.id),saved];
    if(project===trainingProject) {
      trainingState.dirty=reviewDraft.sequence!==sequence;
      reviewDraft.error="";
      reviewWrite("draft."+oldKey,null);
      reviewWrite("selected",saved.id);
      if(trainingState.dirty) { rememberReview(); if(!explicit) scheduleReviewSave(); }
      else if(oldKey==="new" && $("training-select")) {
        $("training-select").add(new Option(saved.name,saved.id));
        $("training-select").value=saved.id;
      }
    }
    return saved;
  } catch(e) {
    if(project===trainingProject) {
      reviewDraft.conflict=e.status===409;
      reviewDraft.error=e.message;
      rememberReview();
    }
    throw e;
  } finally {
    if(reviewDraft.flight===save) reviewDraft.flight=null;
    syncTrainingControls();
  }
}
function reviewStatus() {
  if(reviewDraft.conflict) return "另一個視窗有新版本；呢份草稿仍保留。";
  if(reviewDraft.error) return "草稿未儲存："+reviewDraft.error;
  if(reviewDraft.flight) return "儲存中…";
  if(trainingState.dirty) return "有未儲存的變更";
  return trainingProject?.id ? "已儲存" : "";
}
async function reloadReview() {
  const current=trainingProject;
  if(!current?.id || !window.confirm("載入另一個視窗嘅已儲存版本，放棄本分頁草稿？")) return;
  const projects=await api("training");
  if(trainingProject!==current) return;
  const saved=projects.find(p=>p.id===current.id);
  if(!saved) throw Error("找不到已儲存版本。");
  trainingState.projects=projects;
  trainingState.dirty=false;
  reviewWrite("draft."+saved.id,null);
  openReview(saved,false);
  drawManualTraining();
}
async function copyReview() {
  const old=trainingProject;
  if(!old) return;
  rememberReview();
  const fork={...structuredClone(old),id:"",revision:0,name:Array.from(old.name.trim()||"訓練").slice(0,74).join("")+"（副本）"};
  trainingProject=fork;
  reviewDraft.conflict=false;
  reviewChanged();
  try {
    await storeTraining(fork);
    reviewWrite("draft."+(old.id||"new"),null);
    drawManualTraining();
  } catch(e) {
    trainingProject=old;
    reviewDraft.conflict=true;
    syncTrainingControls();
    throw e;
  }
}
