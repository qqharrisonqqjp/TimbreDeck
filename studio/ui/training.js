"use strict";
const datasetMax = 24, datasetSeconds = 180;
const datasetLanguages = Object.fromEntries(Object.entries(referenceLanguages).filter(([key]) => key !== "auto"));
const trainingInputs = {
  epochs: { label:"訓練輪數", initial:8, max:100, tip:"整份資料重複訓練的次數（1–100）。輪數越多通常越久，小資料集也較易過度擬合。" },
  batch: { label:"批次大小", initial:2, max:8, tip:"每批處理的片段數（1–8）。批次較大通常用更多 GPU 顯存；也受可用訓練片段數限制。" },
};
const datasetState = { clips: [], language: "yue", speaker: "", cleanMode: "", split:true, transcribe:true,
  skipUnrecognized:false, importing: false, submitting: false, settings:{}, settingsOpen:false };
let datasetHistoryTicket = 0;
function datasetControls() {
  const start = $("dataset-run");
  if (!start) return;
  const total = datasetState.clips.reduce((sum, { seconds }) => sum + (seconds || 0), 0);
  const locked = datasetState.importing || datasetState.submitting;
  const speaker = datasetState.speaker.trim();
  const speakerInvalid = Array.from(speaker).length > 64 || /[|\p{Cc}]/u.test(speaker);
  const busy = ["running", "cancelling"].includes(currentJob?.status);
  start.disabled = locked || busy || !datasetState.clips.length || total > datasetSeconds || speakerInvalid;
  $("auto-training-run").disabled = start.disabled;
  $("dataset-files-input").disabled = locked || datasetState.clips.length >= datasetMax;
  $("dataset-speaker").setAttribute("aria-invalid", String(speakerInvalid));
  $("dataset-status").textContent = [
    datasetState.clips.length + " / " + datasetMax + " 段 · " + total.toFixed(1) + " / " + datasetSeconds + " 秒",
    speakerInvalid && "說話者名稱最多 64 字，不可包含直線符號或控制字元。",
    datasetState.importing && "正在匯入所選錄音…",
    datasetState.submitting && "正在提交…",
    total > datasetSeconds && "總長度過長，請移除部份錄音。",
  ].filter(Boolean).join("　");
}
function drawDatasetFiles() {
  const list = $("dataset-files");
  if (!list) return;
  list.replaceChildren(...datasetState.clips.map((clip, i) =>
    el("li", {}, box("actions",
      el("span", { className: "file-name" }, clip.name + (Number.isFinite(clip.seconds) ? " · " + clip.seconds.toFixed(1) + " 秒" : "")),
      btn("移除", () => {
        if (datasetState.importing || datasetState.submitting) return;
        datasetState.clips.splice(i, 1);
        drawDatasetFiles();
      }, { "aria-label": "移除訓練檔 " + clip.name, disabled: datasetState.importing || datasetState.submitting }),
    )),
  ));
  datasetControls();
}
async function importDatasetClips(input) {
  if (datasetState.importing || datasetState.submitting) return;
  const files = Array.from(input.files);
  if (!files.length) return;
  input.value = "";
  datasetState.importing = true;
  drawDatasetFiles();
  const messages = [];
  try {
    for (const file of files) {
      if (datasetState.clips.length >= datasetMax) {
        messages.push("只匯入上限以內的檔案；每次最多 24 段。");
        break;
      }
      try {
        const result = await api("reference", "POST", file);
        if (!validRef(result.id)) throw Error("啟動器沒有回傳錄音編號。");
        if (datasetState.clips.some((clip) => clip.id === result.id)) {
          messages.push(file.name + " 與已選錄音相同，未重複加入。");
        } else {
          datasetState.clips.push({ id: result.id, name: file.name, seconds: result.seconds });
        }
      } catch (err) { messages.push(file.name + "：" + err.message); }
    }
  } finally {
    datasetState.importing = false;
    drawDatasetFiles();
    if (messages.length && panel() === "training") notice(messages.join(" "));
  }
}
async function submitDataset(train) {
  const button = $(train ? "auto-training-run" : "dataset-run");
  if (!button || button.disabled) return;
  const options = train ? requestedTrainingSettings("quick", datasetState) : null;
  if (train && !options) return;
  datasetState.submitting = true;
  drawDatasetFiles();
  try {
    if (train && !datasetState.transcribe &&
      !window.confirm("沒有啟用自動辨識。這次只會準備音檔與校對專案；填好台詞先可以訓練。繼續？")) return;
    const resident = train ? await trainingResident() : "";
    if (resident === null) return;
    currentJob = await api("datasets/auto", "POST", {
      references: datasetState.clips.map(({ id }) => id),
      sourceNames: datasetState.clips.map(({ name }) => name),
      language: datasetState.language,
      speaker: datasetState.speaker.trim() || "speaker-1",
      cleanMode: datasetState.cleanMode,
      split: datasetState.split,
      transcribe: datasetState.transcribe,
      skipUnrecognized: datasetState.skipUnrecognized,
      asrModel: datasetState.asrModel ?? asrChoice,
      releaseResident: resident,
      ...(train ? {training:options} : {}),
    });
    showJob();
  } finally {
    datasetState.submitting = false;
    drawDatasetFiles();
  }
}
async function loadDatasetHistory(mark = renderID) {
  const element = $("dataset-history");
  if (!element) return;
  const ticket = ++datasetHistoryTicket;
  const live = () => ticket === datasetHistoryTicket && mark === renderID && element.isConnected;
  try {
    const items = await api("datasets/auto");
    if (!live()) return;
    const cases = (items || []).filter((item) => /^[a-f0-9]{32}$/.test(item.id || ""));
    element.replaceChildren(...(cases.length ? cases.map((item) => box("actions",
      hint(item.speaker + " · " + item.count + " 段有台詞" + (item.pending ? " · "+item.pending+" 段待校對" : "") + (item.skipped ? " · 已跳過 "+item.skipped+" 段" : "")),
      el("a", { className: "linkbtn", href: "/api/datasets/auto/" + item.id + "/download", download: "" }, "下載資料集"),
      btn("匯入手動專案", async () => {
        if (discardTrainingChanges()) await selectImportedProject(await api("datasets/auto/"+item.id+"/project","POST",{}));
      }),
    )) : [hint("完成的資料集會在這裏顯示。")]));
  } catch (e) {
    if (live()) element.replaceChildren(hint("未能讀取資料集：" + e.message));
  }
}
function autoDatasetPanel() {
  const choice = asrModelPicker(v => { datasetState.asrModel=v; }, datasetState.asrModel ?? asrChoice, "一鍵流程辨識模型");
  choice.hidden = !datasetState.transcribe;
  const settings = trainingSettings("quick", datasetState, "說話者名稱");
  const section = card("GPT-SoVITS 一鍵訓練",
    hint("「一鍵生成及訓練」會準備資料並微調 GPT 和 SoVITS；「只建立資料集」只輸出資料，不會訓練。兩者都只用下方選取的錄音。"),
    field("選取訓練錄音（可選多個）", el("input", {
      type: "file", accept: mediaAccept, multiple: true, id: "dataset-files-input",
      onchange: (e) => importDatasetClips(e.target).catch(notice),
    })),
    el("ol", { id: "dataset-files", className: "dataset-files", "aria-label": "已選訓練錄音" }),
    box("row",
      field("訓練錄音語言", select(datasetLanguages, datasetState.language, (v) => { datasetState.language = v; }, "dataset-language")),
      field("說話者（可選）", el("input", {
        type: "text", id: "dataset-speaker", value: datasetState.speaker, placeholder: "speaker-1", maxLength: 128,
        oninput: (e) => { datasetState.speaker = e.target.value; datasetControls(); },
      })),
    ),
    box("training-toggles",

      trainingToggle("按停頓自動切分",datasetState.split,v=>{datasetState.split=v;},"dataset-split"),
      trainingToggle("辨識每段台詞",datasetState.transcribe,v=>{datasetState.transcribe=v;choice.hidden=!v;},"dataset-transcribe"),
      trainingToggle("跳過沒有辨識到台詞的片段",datasetState.skipUnrecognized,v=>{datasetState.skipUnrecognized=v;},"dataset-skip")),
    trainingCleanPicker(datasetState.cleanMode,v=>{datasetState.cleanMode=v;},"dataset-clean-mode"),
    choice,
    el("p", { id: "dataset-status", className: "asr-status hint", role: "status" }),
    box("training-bar-actions",
      btn("一鍵生成及訓練", () => submitDataset(true), { id:"auto-training-run", className:"primary", disabled:true }),
      settings.trigger,
      btn("只建立資料集", () => submitDataset(false), { id: "dataset-run", disabled: true })),
    settings.options,
    hint("一鍵訓練前，請確認素材清晰、同一人聲、沒有重疊對白，並選對語言。自動辨識可能出錯；要逐句檢查時，請只建立資料集，再匯入手動專案。"),
    el("h3", {}, "已建立的資料集"),
    el("div", { id: "dataset-history", "aria-live": "polite" }, hint("正在讀取資料集…")),
  );
  section.classList.add("dataset-auto");
  return section;
}

const trainingState = { projects:[], saving:false, importing:false, dirty:false, settings:{},
  settingsOpen:false, groups:new Map(), previews:new Set(), ticket:0, pendingASR:null, queued:false, view:null };

function trainingToggle(label, value, save, id) {
  return el("label", {className:"dataset-option"},
    el("input", {id, type:"checkbox", checked:value, onchange:e=>save(e.target.checked)}), label);
}

function trainingSettings(scope, ui, placeholder) {
  const value = ui.settings;
  const sectionID = scope+"-training-options", triggerID = scope+"-training-settings";
  const trigger = btn("訓練設定", () => toggleTrainingSettings(scope, ui, !ui.settingsOpen), {
    id:triggerID, className:"training-settings", "aria-expanded":String(ui.settingsOpen), "aria-controls":sectionID,
  });
  const name = el("input", {type:"text", maxLength:80, placeholder:placeholder || "跟專案同名", value:value.name ?? "",
    oninput:e=>{value.name=e.target.value.trim();}});
  const numbers = Object.entries(trainingInputs).map(([key, {label,initial,max,tip}]) => {
    const tipID = `${sectionID}-${key}-hint`;
    const input = el("input", {type:"number", min:1, max, step:1, required:true, inputMode:"numeric",
      "aria-describedby":tipID, value:Number.isNaN(value[key]) ? "" : value[key] ?? initial,
      oninput:e=>{value[key]=e.target.valueAsNumber;}});
    return {key,initial,input,field:el("div", {},field(label,input),el("p",{id:tipID,className:"hint"},tip))};
  });
  const advice = (title, detail) => el("li", {},el("strong",{},title+"："),detail);
  const options = el("section", {id:sectionID,className:"training-options",hidden:!ui.settingsOpen,"aria-labelledby":triggerID},
    field("微調模型名稱",name),
    box("training-option-numbers",...numbers.map(number=>number.field)),
    box("training-guidance",
      box("training-guidance-header",el("h3",{},"建議起點 · GPT-SoVITS v2Pro"),
        btn("還原預設數值", () => {
          for (const {key,initial,input} of numbers) { delete value[key]; input.value=initial; }
        }, {className:"training-reset"})),
      el("ul", {className:"hint"},
        advice("首次微調", "用 TimbreDeck 預設：8 輪、批次 2。適合用清晰、同一人聲且已核對台詞的素材先試聽。"),
        advice("少量素材或只測流程", "先用 2–4 輪、批次 1。若只有幾句，先當試跑；想改善成品，優先補錄乾淨素材。"),
        advice("顯存不足", "先把批次降到 1，輪數可不變。素材較多、批次 2 穩定且仍有顯存餘量時，才試批次 4；不一定較好聽。"),
        advice("比較音質", "若素材已校對、8 輪的音色仍未夠接近，可另試 10–12 輪。用同一參考錄音和沒參與訓練的台詞，對比發音和音色。"))));
  return {trigger,options};
}

function toggleTrainingSettings(scope, ui, open) {
  const trigger = $(scope+"-training-settings"), options = $(scope+"-training-options");
  ui.settingsOpen = open;
  trigger.setAttribute("aria-expanded", String(open));
  options.hidden = !open;
  // On a long manual project, the closed toolbar might be stuck far below its original position.
  if (open && (trigger.getBoundingClientRect().top < 12 || options.getBoundingClientRect().top > innerHeight-120)) {
    trigger.scrollIntoView({block:"start",behavior:"auto"});
  }
}

function requestedTrainingSettings(scope, ui) {
  const options = $(scope+"-training-options");
  const invalid = options.querySelector("input[type=number]:invalid");
  if (!invalid) return {...ui.settings};
  if (options.hidden) toggleTrainingSettings(scope, ui, true);
  invalid.reportValidity();
  return null;
}

async function trainingResident() {
  const p = await api("tts/resident");
  if (p.busy) throw Error("已有作業進行中，請等完成後再試。");
  if (!p.id) return "";
  return window.confirm("目前常駐："+residentName(p)+"。\n先停止佢，再使用訓練或辨識工具？") ? p.id : null;
}

function clearTrainingPreviews() {
  for (const cleanup of trainingState.previews) cleanup();
  trainingState.previews.clear();
}

function trainingLocked(project) {
  return trainingState.saving || trainingState.importing || (project?.id && trainingState.pendingASR?.project === project.id);
}

function discardTrainingChanges() {
  if(reviewDraft.flight) { notice("儲存中，請稍等再切換專案。");return false; }
  rememberReview();
  return true;
}

function selectProject(project) {
  openReview(project);
  trainingState.groups.clear();
  trainingState.settings = {};
  trainingState.settingsOpen = false;
}

async function selectImportedProject(project) {
  selectProject(project);
  trainingState.view="manual";
  if (panel() === "training" && !arenaOpen) {
    await training();
    notice("資料集已匯入。你可以逐段試聽、編輯台詞、改語言或剔除片段。");
    $("training-editor")?.scrollIntoView({block:"start",behavior:"auto"});
  }
}

function activateTrainingView(key) {
  if(key!=="manual")clearTrainingPreviews();
  trainingState.view=key;
  if($("training-quick")) $("training-quick").hidden=key!=="quick";
  if($("training-manual")) $("training-manual").hidden=key!=="manual";
  for(const v of ["quick","manual"]) $("training-view-"+v)?.setAttribute("aria-pressed",String(key===v));
}

async function training() {
  const mark = renderID, ticket = ++trainingState.ticket;
  clearTrainingPreviews();
  const manual = box("training-manual", hint("正在讀取訓練專案…"));
  manual.id = "training-manual";
  const quick=autoDatasetPanel();
  quick.id="training-quick";
  const chooser=box("segmented",
    btn("快速訓練",()=>activateTrainingView("quick"),{id:"training-view-quick"}),
    btn("手動校對",()=>activateTrainingView("manual"),{id:"training-view-manual"}));
  $("work").replaceChildren(chooser,quick,manual,card("訓練結果",el("div",{id:"training-runs"})));
  activateTrainingView(trainingState.view || (reviewRead("selected")?"manual":"quick"));
  drawDatasetFiles();
  loadDatasetHistory(mark);
  loadTrainingRuns(mark);
  try {
    const projects = await api("training");
    if (mark !== renderID || ticket !== trainingState.ticket || !manual.isConnected) return;
    trainingState.projects = projects;
    resumeReview(projects);
    drawManualTraining();
  } catch (e) {
    if (mark === renderID && ticket === trainingState.ticket) manual.replaceChildren(hint("未能讀取訓練專案："+e.message));
  }
}

async function loadTrainingRuns(mark = renderID) {
  const host = $("training-runs");
  if (!host) return;
  try {
    const records = await api("training-runs");
    if (mark !== renderID || !host.isConnected) return;
    const states = {running:"訓練中",done:"已完成",failed:"訓練未完成",cancelled:"已取消",interrupted:"中斷"};
    host.replaceChildren(...(records.length ? records.map(r => box("training-run",
      el("strong",{},r.name), hint([new Date(r.created).toLocaleString("zh-HK"),states[r.status] || r.status,
        r.epochs+" 輪 · 批次 "+r.batch,r.error].filter(Boolean).join(" · ")),
      box("actions",
        ...(r.dataset ? [el("a",{className:"linkbtn",href:"/api/training-runs/"+r.id+"/dataset",download:""},"下載訓練資料集 ZIP")] : []),
        ...(r.modelId ? [el("a",{className:"linkbtn",href:"/api/trained-models/"+r.modelId+"/download",download:""},"下載微調模型 ZIP")] : []),
        ...(r.project ? [btn("開啟校對專案",async()=>{
          if (!discardTrainingChanges()) return;
          const project=(await api("training")).find(p=>p.id===r.project);
          if (!project) throw Error("找不到此專案。");
          selectProject(project);activateTrainingView("manual");drawManualTraining();
          $("training-editor")?.scrollIntoView({block:"start"});
        })] : [])),
    )) : [hint("真正完成訓練後，這裏會分開顯示資料集 ZIP 與可用於語音合成的模型 ZIP。")]));
  } catch (e) { if (host.isConnected) host.replaceChildren(hint("未能讀取訓練結果："+e.message)); }
}

function pickDataZip() {
  if (!discardTrainingChanges()) return;
  trainingZipDialog("匯入訓練資料集", "專案名稱（可留空）", async (file, name) => {
    const p = await api("training/import?name="+encodeURIComponent(name), "POST", file);
    await selectImportedProject(p);
  });
}

function trainingZipDialog(title, label, submit) {
  const name = el("input",{type:"text",maxLength:80});
  const file = el("input",{type:"file",accept:".zip",required:true});
  const feedback = el("p",{className:"hint",role:"status"},"選擇 TimbreDeck 匯出的 ZIP。整個流程在本機執行。");
  const cancel = btn("取消",()=>dialog.close());
  const start = btn("匯入",async()=>{
    const input = file.files[0];
    if (!input) {feedback.textContent="請先選擇 ZIP。";return;}
    start.disabled = cancel.disabled = true;
    feedback.textContent = "正在校驗及匯入 ZIP…";
    try { await submit(input,name.value.trim()); dialog.close(); }
    catch(e) { feedback.textContent = e.message; }
    finally { start.disabled = cancel.disabled = false; }
  },{className:"primary"});
  const dialog = el("dialog", {className:"studio-dialog","aria-label":title,oncancel:e=>{if(start.disabled)e.preventDefault();},
    onclose:()=>dialog.remove()},el("h2",{},title),field("ZIP 檔案",file),field(label,name),feedback,box("actions",start,cancel));
  document.body.append(dialog);
  dialog.showModal();
}

function drawManualTraining() {
  const host = $("training-manual");
  if (!host || panel() !== "training" || arenaOpen) return;
  clearTrainingPreviews();
  const project = trainingProject;
  const list = card("手動訓練專案", box("actions",
    btn("新增專案",()=>{if(discardTrainingChanges()){selectProject(null);drawManualTraining();}},
      {className: project ? "" : "primary", disabled:!!trainingLocked(project)}),
    btn("匯入資料集 ZIP",pickDataZip,{disabled:!!trainingLocked(project)})),
    ...(trainingState.projects.length ? [field("切換專案", select({
      "":"選擇已儲存的專案",...Object.fromEntries(trainingState.projects.map(p=>[p.id,p.name]))},project?.id || "",key=>{
        const chosen = trainingState.projects.find(p=>p.id===key);
        if(chosen && discardTrainingChanges()){selectProject(chosen);}
        drawManualTraining();
      },"training-select"))] : [hint("可從上方一鍵資料集歷史匯入，亦可直接新增原始錄音。")]));
  if (!project) { host.replaceChildren(list); return; }
  const disabled = !!trainingLocked(project);
  const summary = el("p", {id:"training-summary", className:"hint", role:"status"});
  const name = el("input", {type:"text", value:project.name, maxLength:80, oninput:e=>{
    project.name=e.target.value; trainingChanged();
  }});
  const upload = el("input",{id:"training-media",type:"file",accept:mediaAccept,multiple:true,onchange:e=>{
    const files = Array.from(e.target.files);
    e.target.value="";
    if (files.length) manualImportDialog(project,files);
  }});
  const model = asrModelPicker(v=>{trainingState.asrModel=v;},trainingState.asrModel ?? asrChoice,"手動專案辨識模型");
  const settings = trainingSettings("manual",trainingState,project.name);
  const primary = btn("開始訓練",()=>startManualTraining(project),{id:"training-start",className:"primary"});
  const bar = box("training-actionbar", box("training-bar-actions", primary,settings.trigger,
    btn("儲存專案",async()=>{await storeTraining(project);drawManualTraining();notice("訓練專案已儲存。");},{id:"training-save"}),
    btn("匯出校對後資料集",async()=>{
      const saved=await storeTraining(project);
      drawManualTraining();
      const a=el("a",{href:"/api/training/"+saved.id+"/dataset",download:""});a.click();
    },{id:"training-export"})),settings.options,summary,
    box("actions review-conflict",btn("另存草稿副本",copyReview),btn("載入已儲存版本",reloadReview)));
  bar.querySelector(".review-conflict").hidden=!reviewDraft.conflict;
  const editor = card("片段校對",
    hint("每段錄音可獨立重切、試聽、辨識與編輯。選「用於訓練」才會納入；剔除不會刪原檔。"),
    el("fieldset",{className:"training-fields",disabled},
      box("row",field("專案名稱",name),field("匯入錄音",upload)),
      box("row",field("辨識模型",model),btn("辨識全部空白片段",()=>transcribeManual(project),{id:"training-transcribe"}),
        btn("重試辨識失敗片段",()=>transcribeManual(project,failedTrainingClips(project)),{id:"training-retry"}),
        btn("批量採用已核對的台詞",()=>{
          const clips=project.clips.filter(c=>c.use==="pending" && c.transcript.trim());
          if (!clips.length || !window.confirm("將 "+clips.length+" 段有台詞、待校對的片段標記為訓練？未填台詞及已剔除的片段不變。")) return;
          for (const c of clips) c.use="train";
          trainingChanged();drawManualTraining();
        },{id:"training-adopt"})),
      bar, ...trainingGroups(project)));
  editor.id = "training-editor";
  host.replaceChildren(list,editor);
  $("training-select") && ($("training-select").disabled = disabled);
  syncTrainingControls();
}

function syncTrainingControls() {
  const summary = $("training-summary"), p = trainingProject;
  if (!summary || !p) return;
  const busy = ["running","cancelling"].includes(currentJob?.status) || trainingLocked(p) || trainingState.queued;
  const hasName = !!p.name.trim();
  const adopted = p.clips.filter(c=>c.use==="train" && c.transcript.trim()).length;
  const waiting = p.clips.filter(c=>c.use==="pending").length;
  const blank = p.clips.some(c=>c.use==="pending" && !c.transcript.trim());
  $("training-start").disabled = !!busy || !hasName || !adopted || reviewDraft.conflict;
  $("training-save").disabled = !!trainingLocked(p) || !hasName;
  $("training-export").disabled = !!trainingLocked(p) || !hasName || !adopted || reviewDraft.conflict;
  $("training-transcribe").disabled = !!busy || !hasName || !blank || reviewDraft.conflict;
  const retry=$("training-retry");
  retry.hidden=!failedTrainingClips(p).length;
  retry.disabled=!!busy || !hasName || reviewDraft.conflict;
  const conflict = $("training-editor")?.querySelector(".review-conflict");
  if(conflict) conflict.hidden=!reviewDraft.conflict;
  $("training-adopt").disabled = !p.clips.some(c=>c.use==="pending" && c.transcript.trim());
  for (const control of document.querySelectorAll(".clip-recognize")) control.disabled = !!busy;
  summary.textContent = [
    p.clips.length+" 段 · "+adopted+" 訓練 · "+waiting+" 待校對",
    !hasName && "請輸入專案名稱",
    reviewStatus(),
    trainingState.importing && "正在匯入錄音",
    p.id && trainingState.pendingASR?.project===p.id && "正在辨識台詞",
  ].filter(Boolean).join("　");
}

function failedTrainingClips(project) {
  return project.clips.filter(c=>c.asrFailed && c.use==="pending" && !c.transcript.trim());
}

function trainingChanged() { reviewChanged(); }

async function storeTraining(project) {
  if (!project.name.trim()) throw Error("請先輸入專案名稱。");
  if (trainingState.saving || trainingProject !== project) throw Error("請重新選擇專案再試。");
  trainingState.saving = true;
  if ($("training-editor")) $("training-editor").querySelector("fieldset").disabled = true;
  syncTrainingControls();
  try {
    return await saveReview(project,true);
  } finally {
    trainingState.saving=false;
    if ($("training-editor")) $("training-editor").querySelector("fieldset").disabled = !!trainingLocked(trainingProject);
    syncTrainingControls();
  }
}

async function startManualTraining(project) {
  if (trainingState.queued) return;
  const opt = requestedTrainingSettings("manual", trainingState);
  if (!opt) return;
  trainingState.queued = true;
  $("training-start").disabled = true;
  try {
    const resident = await trainingResident();
    if (resident===null || trainingProject!==project) return;
    const saved = await storeTraining(project);
    drawManualTraining();
    currentJob = await api("training/"+saved.id+"/train","POST",{revision:saved.revision,options:{...opt,releaseResident:resident}});
    showJob();
  } finally { trainingState.queued=false;syncTrainingControls(); }
}

async function transcribeManual(project, clips, current) {
  if (trainingState.queued) return;
  const indices = clips?.map(c=>project.clips.indexOf(c)).filter(i=>i>=0);
  const overwrite = !!clips?.some(c=>c.transcript.trim());
  if (overwrite && !window.confirm("用辨識結果覆寫所選片段的台詞？成功後片段會重新標為待校對。")) return;
  trainingState.queued = true;
  try {
    const saved = await storeTraining(project);
    drawManualTraining();
    currentJob = await api("training/"+saved.id+"/transcribe","POST",{
      revision:saved.revision, asrModel:current ?? trainingState.asrModel ?? asrChoice,
      ...(indices ? {clips:indices.map(i=>saved.clips[i].id),replace:overwrite} : {}),
    });
    trainingState.pendingASR={id:currentJob.id,project:saved.id};
    drawManualTraining();
    showJob();
    observeTrainingJob(currentJob);
  } finally { trainingState.queued=false;syncTrainingControls(); }
}

function manualImportDialog(project, files) {
  let language = trainingState.language || "yue";
  let cleanMode="", split = true, recognize = true, model = trainingState.asrModel ?? asrChoice;
  const picker = asrModelPicker(v=>{model=v;},model,"匯入錄音辨識模型");
  const confirm = btn("匯入錄音",async()=>{
    dialog.close();
    await importManualAudio(project,files,{split, recognize,model,language,cleanMode});
  },{className:"primary"});
  const dialog = el("dialog",{className:"studio-dialog","aria-label":"匯入錄音選項",onclose:()=>dialog.remove()},
    el("h2",{},"匯入錄音選項"),hint("已選 "+files.length+" 個錄音。先決定要做咩，之後可以逐段修改。"),
    field("錄音語言",select(datasetLanguages,language,v=>{language=v;trainingState.language=v;},"training-import-lang")),
    trainingToggle("按停頓自動切分",split,v=>{split=v;},"training-import-split"),
    trainingToggle("自動辨識台詞",recognize,v=>{recognize=v;picker.hidden=!v;},"training-import-asr"),
    trainingCleanPicker(cleanMode,v=>{cleanMode=v;},"manual-clean-mode"),
    picker, box("actions",confirm,btn("取消",()=>dialog.close())));
  document.body.append(dialog);dialog.showModal();
}

async function importManualAudio(project, files, opts) {
  if (trainingProject!==project || trainingLocked(project)) return;
  trainingState.importing = true;
  drawManualTraining();
  const selected = [], warnings = [];
  try {
    for (const file of files) {
      if (project.clips.length >= 300) { warnings.push("每個專案最多 300 個片段。"); break; }
      const filename = file.name.slice(0,255);
      try {
        let ref=await api("reference","POST",file);
        if (opts.cleanMode && validRef(ref.id)) {
          const job=await api("clean","POST",{references:[ref.id],mode:opts.cleanMode,gapMs:0});
          await waitForJob(job);
          const cleaned=await api("clean/audio/"+job.id,"GET",undefined,{responseType:"blob"});
          ref=await api("reference","POST",cleaned);
        }
        if (!validRef(ref.id)) throw Error("未收到錄音編號。");
        const clip={reference:ref.id,sourceName:filename,startMs:0,endMs:Math.floor(ref.seconds*1000),
          language:opts.language,use:"pending",transcript:""};
        let added = [clip];
        if (opts.split) {
          try { added=await api("training/split","POST",clip); }
          catch(e) {warnings.push(filename+"："+e.message+" 已保留整段錄音。");}
        }
        if (project.clips.length+added.length>300) throw Error("切分後會超過 300 個片段，未加入此檔案。");
        project.clips.push(...added);selected.push(...added);
        trainingState.groups.set(ref.id,true);
        if (!project.name) project.name = filename.replace(/\.[^.]*$/,"").slice(0,80) || "新錄音專案";
        trainingChanged();
      } catch(e) {warnings.push(filename+"："+e.message);}
    }
  } finally {trainingState.importing=false;drawManualTraining();}
  if (warnings.length) notice(warnings.join(" "));
  if (selected.length && opts.recognize) await transcribeManual(project,selected,opts.model);
}

function trainingGroups(project) {
  const grouped = Map.groupBy ? Map.groupBy(project.clips,c=>c.sourceGroup||c.reference) : new Map();
  if (!Map.groupBy) for (const c of project.clips) {
    const key=c.sourceGroup||c.reference;
    if(!grouped.has(key))grouped.set(key,[]);
    grouped.get(key).push(c);
  }
  if (!grouped.size) return [hint("匯入一個錄音或資料集，就會在此顯示片段。")];
  return Array.from(grouped,([key,clips],groupIndex)=>{
    clips.sort((a,b)=>(a.sourceOffsetMs||0)+a.startMs-(b.sourceOffsetMs||0)-b.startMs);
    const details = el("details",{className:"training-group",open:trainingState.groups.get(key) ?? (groupIndex===0),
      ontoggle:e=>trainingState.groups.set(key,e.currentTarget.open)},
      el("summary",{},el("span",{className:"file-name"},clips[0].sourceName||"錄音 "+(groupIndex+1)),
        el("small",{},clips.length+" 段")),
      box("training-segments",...clips.map((c,index)=>trainingClipFields(project,c,index))));
    return details;
  });
}

function trainingClipFields(project, clip, index) {
  const status = el("p",{className:"hint",role:"status"},clip.asrStatus||"");
  const player = el("audio",{className:"training-audio",controls:true,preload:"none",hidden:true,"aria-label":"試聽片段 "+(index+1)});
  let url, controller;
  const clear = () => {
    controller?.abort();
    controller=undefined;
    if(url) URL.revokeObjectURL(url);
    url=undefined;
    player.pause();player.removeAttribute("src");player.hidden=true;
  };
  trainingState.previews.add(clear);
  const markClipChanged = rangeChanged => {
    if (rangeChanged) {
      clear();
      if (clip.transcript) clip.use="pending";
      use.value=clip.use;
      clip.asrStatus="已改時間，請重新試聽並核對台詞。";
      clip.asrFailed=false;
      status.textContent=clip.asrStatus;
    }
    trainingChanged();
  };
  const millis = key => el("input",{type:"number",step:1,min:0,value:clip[key],oninput:e=>{
    clip[key]=e.target.valueAsNumber;markClipChanged(true);
  }});
  const use=select({pending:"待校對",train:"用於訓練",holdout:"保留作測試",skip:"剔除"},clip.use,v=>{
    if ((v==="train" || v==="holdout") && !clip.transcript.trim()) {
      use.value=clip.use;status.textContent="先辨識或輸入台詞，再採用呢段。";return;
    }
    clip.use=v;trainingChanged();
  });
  const hintOffset=(clip.sourceOffsetMs||0)+clip.startMs;
  const base = box("training-clip",
    box("training-clip-heading",el("h3",{},"片段 "+(index+1)),hint((hintOffset/1000).toFixed(2)+" 秒起")),
    box("row",field("開始（ms）",millis("startMs")),field("結束（ms）",millis("endMs")),
      field("語言",select(datasetLanguages,clip.language,v=>{clip.language=v;markClipChanged(true);})),field("用途",use)),
    field("台詞／逐字稿",area(undefined,clip.transcript,"可自動辨識，或輸入實際對白",v=>{
      clip.transcript=v; if(!v.trim() && (clip.use==="train"||clip.use==="holdout")) {clip.use="pending";use.value=clip.use;}
      if(v.trim())clip.asrFailed=false;
      trainingChanged();
    })),
    player,status,
    box("actions",
      btn("試聽片段",async()=>{
        clear();
        const attempt = controller = new AbortController();
        status.textContent="正在準備目前範圍的音訊…";
        try {
          const blob=await api("training/preview","POST",clip,{responseType:"blob",signal:attempt.signal});
          if (attempt.signal.aborted || !player.isConnected) return;
          url=URL.createObjectURL(blob);player.src=url;player.hidden=false;
          status.textContent=clip.asrStatus||"";
          await player.play().catch(()=>{});
        } catch(e) {if(e.name!=="AbortError")status.textContent=e.message;}
      }),
      btn("辨識此段台詞",()=>transcribeManual(project,[clip]),{className:"clip-recognize"}),
      btn("按停頓切分此段",async()=>{
        if(clip.transcript.trim()&&!window.confirm("再次切分會清除這一段原有台詞。繼續？"))return;
        const rows=await api("training/split","POST",clip);
        if (trainingProject!==project) return;
        const at=project.clips.indexOf(clip);
        if(at<0)return;
        if (rows.length<2) {status.textContent="未找到可切分的停頓，原片段不變。";return;}
        if(project.clips.length-1+rows.length>300)throw Error("每個專案最多 300 個片段。");
        project.clips.splice(at,1,...rows);trainingChanged();drawManualTraining();
        notice("「"+clip.sourceName+"」片段 "+(index+1)+" 已分成 "+rows.length+" 段。");
      }),
      btn("剔除片段",()=>{clip.use="skip";use.value="skip";trainingChanged();status.textContent="已剔除，原錄音仍保留。";})));
  return base;
}

async function trainingJobChanged(j) {
  if (!j || !["done","failed","cancelled"].includes(j.status)) return;
  const inTraining = () => !arenaOpen && !cleanOpen && panel()==="training";
  if (["dataset","auto-training"].includes(j.kind) && inTraining()) loadDatasetHistory();
  if (["training","auto-training"].includes(j.kind)) {
    if (j.status==="done") trainedModels=await api("trained-models");
    if (inTraining()) {
      loadTrainingRuns();
      const projects=await api("training");
      if (inTraining()) {
        trainingState.projects=projects;
        if ($("training-manual")) drawManualTraining();
      }
    }
  }
  if (trainingState.pendingASR?.id===j.id) {
    const project = trainingState.pendingASR.project;
    trainingState.pendingASR=null;
    if (trainingProject?.id===project) {
      const projects=await api("training");
      trainingState.projects=projects;
      const fresh=projects.find(p=>p.id===project);
      if(fresh && !trainingState.dirty) trainingProject=fresh;
      if (inTraining()) drawManualTraining();
    }
  }
}

function observeTrainingJob(j) {
  waitForJob(j,{terminal:true}).then(trainingJobChanged).catch(notice);
}

function showModelImport(picked) {
  trainingZipDialog("匯入 GPT-SoVITS 微調模型", "微調模型名稱（可留空，保留 ZIP 內名稱）",async(file,name)=>{
    const entry=await api("trained-models/import?name="+encodeURIComponent(name),"POST",file);
    trainedModels=await api("trained-models");
    if (picked) picked(entry);
    render();
    notice("已匯入微調模型「"+entry.name+"」。");
  });
}
