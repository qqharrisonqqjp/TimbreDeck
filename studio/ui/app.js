"use strict";
const mediaAccept = ".wav,.mp3,.mp4,.m4a,.aac,.mov";
const $ = (id) => document.getElementById(id);
const catalog = {
  "gpt-sovits": ["GPT-SoVITS", "以參考錄音及逐字稿複製聲線。"],
  cosyvoice3: [
    "CosyVoice 3",
    "以同一段錄音進行聲音複製、跨語言朗讀或指令朗讀。",
  ],
  voxcpm2: ["VoxCPM2", "錄音複製、錄音延續與文字聲線設計。"],
  indextts2: ["IndexTTS-2", "分開選擇角色音色與情緒示範。"],
  "qwen3-tts": [
    "Qwen3-TTS",
    "以 Base、CustomVoice 或 VoiceDesign 選擇聲線。",
  ],
};
const cleanWorkspace = ["去除雜聲", "批量清理本機 WAV，按指定次序合併及匯出。"];
let cleanOpen = false;
const integrated = (k) => Boolean(engines[k]?.modes?.length);
const lang = {
  yue: "粵語（可夾英語）",
  zh: "普通話",
  en: "英語",
  ja: "日語",
  ko: "韓語",
  de: "德語",
  fr: "法語",
  ru: "俄語",
  pt: "葡萄牙語",
  es: "西班牙語",
  it: "意大利語",
  auto: "自動辨識",
};
const referenceLanguages = Object.fromEntries(
  ["yue", "zh", "en", "ja", "ko", "auto"].map((id) => [id, lang[id]]),
);
const emotions = ["喜悅", "憤怒", "悲傷", "恐懼", "厭惡", "憂鬱", "驚訝", "平靜"];
let draft,
  engines,
  studioBuild,
  voices = [],
  editingVoice = null,
  trainingProject = null,
  trainedModels = [],
  historySelection = new Set(),
  currentJob,
  timer,
  writes = Promise.resolve(),
  revision = 0, savedRevision = 0,
  renderID = 0;
function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, val] of Object.entries(props)) {
    if (val === undefined || val === null) continue;
    if (key.startsWith("aria-")) node.setAttribute(key, val);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), val);
    else node[key] = val;
  }
  node.append(...children);
  return node;
}
function btn(label, fn, props = {}) {
  return el(
    "button",
    {
      type: "button",
      onclick: () => Promise.resolve().then(fn).catch(notice),
      ...props,
    },
    label,
  );
}
function notice(e) {
  $("notice").textContent = e?.message || String(e);
}
function field(title, control) {
  return el("label", { className: "field" }, el("span", {}, title), control);
}
function card(title, ...body) {
  return el("section", { className: "card" }, el("h2", {}, title), ...body);
}
function hint(text) {
  return el("p", { className: "hint" }, text);
}
function box(cls, ...nodes) {
  return el("div", { className: cls }, ...nodes);
}
async function api(path, method = "GET", data, {responseType = "json", signal} = {}) {
  const blob = data instanceof Blob;
  const opts = { method, signal, headers: { "X-TimbreDeck": "1" } };
  if (data !== undefined) {
    opts.headers["Content-Type"] = blob
      ? "application/octet-stream"
      : "application/json";
    opts.body = blob ? data : JSON.stringify(data);
  }
  const res = await fetch("/api/" + path, opts);
  if (res.ok && responseType === "blob") return res.blob();
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err=Error(body.error || "HTTP " + res.status);
    err.status=res.status;
    throw err;
  }
  return body;
}
function d() {
  return (draft.drafts[draft.engine] ||= {});
}
function selectedMode(engine, id) {
  return engine.modes.find((mode) => mode.id === id) ??
    engine.modes.find((mode) => engine.availableVariants?.includes(mode.variant)) ??
    engine.modes[0];
}
function canRun(engine, mode, variant = mode.variant) {
  return engine.ready && (!variant || engine.availableVariants?.includes(variant));
}
function persist() {
  clearTimeout(timer);
  const rev = ++revision;
  const copy = structuredClone(draft);
  writes = writes
    .catch(() => {})
    .then(() => api("draft", "PUT", copy))
    .then(() => {
      savedRevision = rev;
      if (rev === revision) $("saved").textContent = "草稿已儲存至本機";
      return true;
    })
    .catch((e) => {
      notice(e);
      $("saved").textContent = "草稿儲存失敗";
      return false;
    });
  return writes;
}
function changed() {
  clearTimeout(timer);
  revision++;
  $("saved").textContent = "正在儲存草稿…";
  timer = setTimeout(persist, 250);
}
function select(values, current, fn, id) {
  const node = el("select", { id, onchange: (e) => fn(e.target.value) });
  for (const [k, v] of Object.entries(values)) node.add(new Option(v, k));
  node.value = current;
  return node;
}
function area(id, value, placeholder, update) {
  return el("textarea", {
    id,
    value,
    placeholder,
    oninput: (e) => update(e.target.value),
  });
}
function setEngine(k) {
  if(k==="clean")return setTab("clean");
  if(!engines[k])return;
  if($("engine-dialog").open)$("engine-dialog").close();
  if(draft.engine!==k){draft.engine=k;persist()}
  render();
}
function setTab(key,focus=false) {
  if(!Object.hasOwn(routesByKey,key))return;
  if(workspace==="sts" && key!=="sts" && mic) {
    if(!window.confirm("離開流式 STS 會停止收音。要離開嗎？"))return;
    stopMic();
  }
  if(workspace==="training" && key!=="training")rememberReview();
  workspace=key;
  setWorkspaceFlags();
  draft.workspace=key;
  persist();
  setDrawer(false);
  render();
  if(focus)$("title").focus({preventScroll:true});
}
function setWorkspaceFlags() {
  cleanOpen=workspace==="clean";
  arenaOpen=["sts","tts","asr"].includes(workspace)?workspace:"";
}
function selectedVoice() {
  return voices.find((voice) => voice.id === draft.voiceId);
}
let referenceMutation = 0, voiceUploadTicket = 0, emotionUploadTicket = 0;
function useVoice(voice) {
  referenceMutation++;
  draft.voiceId = voice.id;
  draft.reference = voice.reference;
  draft.referenceName = voice.name;
}
function selectedTrainedModel(settings) {
  if (settings.modelVoice === (draft.voiceId || "")) return settings.modelProject || "";
  const key = selectedVoice()?.modelProject;
  return trainedModels.some(m => m.id === key) ? key : "";
}
function modelOptions(base, selected) {
  const models = { "": base, ...Object.fromEntries(trainedModels.map((model) => [model.id, model.name])) };
  if (selected && !models[selected]) models[selected] = "找不到已選模型";
  return models;
}
function variantName(value) {
  if (value === "v5turbo") return "V5 Turbo";
  if (value === "v5dev") return "V5 Dev";
  return value === "Fun-CosyVoice3-0.5B" ? "Fun-CosyVoice3-0.5B-2512" : value;
}
function referenceTranscript(settings) {
  const voice = selectedVoice();
  const belongsToReference = settings.transcriptFor === draft.reference;
  if (voice) {
    return belongsToReference && settings.transcriptVoice === voice.id
      ? settings.transcript
      : voice.transcript;
  }
  return (!settings.transcriptFor || belongsToReference) ? settings.transcript : "";
}
function replaceReference(reference, name) {
  referenceMutation++;
  for (const settings of Object.values(draft.drafts)) {
    if (!settings.transcriptFor && settings.transcript)
      settings.transcriptFor = draft.reference || "";
  }
  draft.reference = reference;
  draft.referenceName = name;
  delete draft.voiceId;
}
function render() {
  clearTrainingPreviews();
  renderID++;
  const k=draft.engine;
  document.body.dataset.engine=cleanOpen?"clean":k;
  showWorkspaceHeadline();
  $("notice").textContent="";
  $("switcher").replaceChildren(...Object.keys(engines).map(key=>
    btn(catalog[key]?.[0]||key,()=>setEngine(key),{"aria-pressed":String(key===k)})));
  drawSections();
  switch(workspace) {
    case "sts": drawSTS();break;
    case "asr": case "tts": drawArena();break;
    case "clean": cleaning();break;
    case "synth":
      if(integrated(k))synthesis();
      else $("work").replaceChildren(card("先連接合成引擎",btn("開啟模型管理",()=>setTab("models"),{className:"primary"})));
      break;
    case "models": models();break;
    case "voices": voiceLibrary();break;
    case "training": training();break;
    case "storage": storageSettings();break;
    default: history();
  }
  showJob();
}
const cleanModes = {
  denoise: "基礎去噪",
  mossformer: "ClearerVoice · MossFormer2",
  lavasr: "LavaSR v2 · 頻寬修復",
  "lavasr-denoise": "LavaSR v2 · 頻寬修復＋去噪",
  uvr: "UVR 人聲分離",
  both: "先用 UVR5 分離人聲，再做語音降噪",
};
const cleanHelp = {
  "":"已清理或原聲乾淨嘅素材可跳過。",
  denoise:"一般背景底噪，可先試基礎去噪。",
  mossformer:"適合有底噪、但聲線清楚嘅訓練素材。48 kHz 遮罩式去噪；不等於超解析。先試聽齒音同尾音。",
  lavasr:"適合低採樣率、電話或舊影片嘅悶聲；會重建部分高頻，可能改變音色。乾淨素材毋須用。",
  "lavasr-denoise":"適合又悶又嘈嘅素材。比單純頻寬修復多做去噪；建議先試聽。",
};
function cleanPicker(options, value, change, id) {
  const help = hint(cleanHelp[value] || "將人聲與伴奏分開，視乎所選模式再去噪。");
  help.id = id+"-help";
  const picker = select(options,value,v=>{change(v);help.textContent=cleanHelp[v]||"將人聲與伴奏分開，視乎所選模式再去噪。";},id);
  picker.setAttribute("aria-describedby",help.id);
  return box("stack",field("清理模式",picker),help);
}
function trainingCleanPicker(value, change, id) {
  const options = {"":"不處理"};
  for (const k of ["denoise","mossformer","lavasr","lavasr-denoise"]) options[k]=cleanModes[k];
  return cleanPicker(options,value,change,id);
}
const cleanMaxClips = 24;
const cleanMaxGap = 3000;
const cleanMaxSeconds = 180;
const cleanQueue = [];
const cleanNames = new Map();
let cleanChoices = new Map(),
  cleanChoice = "",
  cleanMode = "denoise",
  cleanGap = "0",
  cleanLoading = false,
  cleanImporting = false,
  cleanSubmitting = false;
const cleanRefURL = (id) => "/api/reference/" + id;
const cleanAudioURL = (id) => "/api/clean/audio/" + id;
const validRef = (id) => typeof id === "string" && /^[a-f0-9]{64}$/.test(id);
const clipName = (id) => "參考錄音 " + id.slice(0, 8);
function cleanGapValue() {
  const n = Number(cleanGap);
  return cleanGap.trim() && Number.isInteger(n) && n >= 0 && n <= cleanMaxGap ? n : null;
}
function cleanDuration() {
  const gap = cleanGapValue();
  if (gap === null || !cleanQueue.length || !cleanQueue.every((clip) => Number.isFinite(clip.seconds))) return null;
  return cleanQueue.reduce((total, clip) => total + clip.seconds, 0) + (cleanQueue.length - 1) * gap / 1000;
}
function cleanEditLocked() {
  return cleanSubmitting || cleanImporting;
}
function cleanStatus() {
  const run = $("clean-run");
  if (!run) return;
  const busy = ["running", "cancelling"].includes(currentJob?.status);
  const gap = cleanGapValue(), duration = cleanDuration();
  run.disabled = busy || cleanEditLocked() || !cleanQueue.length || gap === null ||
    (duration !== null && duration > cleanMaxSeconds);
  const capacity = cleanQueue.length >= cleanMaxClips;
  $("clean-add").disabled = cleanEditLocked() || capacity || !cleanChoices.has(cleanChoice);
  $("clean-upload").disabled = cleanEditLocked() || capacity;
  const errors = [
    gap === null && "間隔必須是 0 至 3,000 毫秒的整數。",
    duration !== null && duration > cleanMaxSeconds && "合併後超過 180 秒，請減少音檔或縮短間隔。",
  ].filter(Boolean);
  $("clean-gap").setAttribute("aria-invalid", String(gap === null));
  $("clean-length").textContent = [
    cleanQueue.length + " / " + cleanMaxClips + " 段",
    duration !== null && "預計 " + duration.toFixed(1) + " 秒",
    cleanImporting && "正在匯入…",
    cleanSubmitting && "正在提交…",
    ...errors,
  ].filter(Boolean).join(" · ");
}
function cleanPreview() {
  const p = $("clean-reference-preview");
  if (!p) return;
  const clip = cleanChoices.get(cleanChoice);
  p.replaceChildren(...(clip ? [
    el("audio", { controls: true, preload: "none", src: cleanRefURL(clip.id), "aria-label": "試聽已選參考錄音" }),
  ] : []));
}
function fillCleanPicker() {
  const picker = $("clean-existing");
  if (!picker) return;
  if (!cleanChoices.has(cleanChoice)) cleanChoice = "";
  picker.replaceChildren(new Option(cleanChoices.size ? "選擇一段參考錄音" :
    cleanLoading ? "正在尋找本機錄音…" : "沒有已存參考錄音", ""), ...Array.from(
    cleanChoices.values(), ({ id, name }) => new Option(name, id),
  ));
  picker.value = cleanChoice;
  const shown = $("clean-reference-preview")?.querySelector("audio")?.getAttribute("src");
  if (shown !== (cleanChoice ? cleanRefURL(cleanChoice) : undefined)) cleanPreview();
  cleanStatus();
}
function collectCleanRefs(...groups) {
  const choices = new Map();
  function add(clip) {
    if (!validRef(clip.id)) return;
    const old = choices.get(clip.id);
    const known = cleanNames.get(clip.id);
    choices.set(clip.id, {
      id: clip.id,
      name: known?.name || old?.name || clip.name || clipName(clip.id),
      seconds: known?.seconds ?? old?.seconds ?? clip.seconds,
    });
  }
  for (const v of voices) add({ id: v.reference, name: v.name });
  add({ id: draft.reference, name: draft.referenceName });
  for (const settings of Object.values(draft.drafts)) {
    if (!settings) continue;
    for (const field of ["reference", "emotionReference", "transcriptFor", "refLangFor"]) {
      const name = field === "emotionReference" ? settings.emotionReferenceName : undefined;
      add({ id: settings[field], name });
    }
  }
  for (const group of groups) group.forEach(add);
  for (const clip of cleanNames.values()) add(clip);
  return choices;
}
async function loadCleanRefs(mark) {
  cleanLoading = true;
  fillCleanPicker();
  const settled = await Promise.allSettled([api("unused-recordings"), api("history"), api("training")]);
  if (mark !== renderID || !cleanOpen) return;
  const [unused, history, projects] = settled.map((r) => Array.isArray(r.value) ? r.value : []);
  cleanChoices = collectCleanRefs(
    projects.flatMap((p) => (p.clips || []).map((clip) => ({
      id: clip.reference, name: clip.sourceName || p.name,
    }))),
    history.flatMap(({ input, voiceName }) => input ? [
      { id: input.reference, name: voiceName }, { id: input.emotionReference },
    ] : []),
    unused,
  );
  cleanLoading = false;
  fillCleanPicker();
  $("clean-refs-info").textContent = settled.some((r) => r.status === "rejected")
    ? "部分舊錄音未能列出；仍可匯入錄音，或使用目前可見的錄音。" : "";
}
function drawCleanQueue() {
  const list = $("clean-queue");
  if (!list) return;
  function changeClip(index, shift) {
    if (cleanEditLocked()) return;
    if (shift) {
      const to = index + shift;
      [cleanQueue[index], cleanQueue[to]] = [cleanQueue[to], cleanQueue[index]];
      drawCleanQueue();
      const focus = $("clean-" + (shift < 0 ? "up" : "down") + "-" + to);
      (focus.disabled ? $("clean-remove-" + to) : focus).focus();
    } else {
      cleanQueue.splice(index, 1);
      drawCleanQueue();
      ($("clean-remove-" + Math.min(index, cleanQueue.length - 1)) || $("clean-upload")).focus();
    }
  }
  list.replaceChildren(...cleanQueue.map((clip, i) =>
    el("li", { className: "clean-clip" },
      box("clean-row",
        el("strong", { className: "file-name" }, clip.name),
        Number.isFinite(clip.seconds) ? el("small", {}, clip.seconds.toFixed(1) + " 秒") : "",
        box("actions clean-actions",
          btn("上移", () => changeClip(i, -1), { id: "clean-up-" + i, disabled: i === 0 || cleanEditLocked(), "aria-label": "上移第 " + (i + 1) + " 段" }),
          btn("下移", () => changeClip(i, 1), { id: "clean-down-" + i, disabled: i === cleanQueue.length - 1 || cleanEditLocked(), "aria-label": "下移第 " + (i + 1) + " 段" }),
          btn("移除", () => changeClip(i, 0), { id: "clean-remove-" + i, disabled: cleanEditLocked(), "aria-label": "移除第 " + (i + 1) + " 段" }),
          btn("辨識", () => { cleanASRReference = clip.id; drawCleanASR(); }, { "aria-label": "辨識第 " + (i + 1) + " 段" }),
        ),
      ),
      el("audio", { controls: true, preload: "none", src: cleanRefURL(clip.id), "aria-label": "試聽 " + clip.name }),
    ),
  ));
  $("clean-queue-empty").hidden = cleanQueue.length > 0;
  cleanStatus();
  drawCleanASR();
}
async function cleanUpload(input) {
  if (cleanImporting || cleanSubmitting) return;
  const files = Array.from(input.files);
  if (!files.length) return;
  input.value = "";
  cleanImporting = true;
  drawCleanQueue();
  const problems = [];
  let accepted = 0;
  try {
    for (const file of files) {
      if (cleanQueue.length >= cleanMaxClips) {
        problems.push("待清理清單上限為 24 段，未匯入其餘檔案。");
        break;
      }
      try {
        const r = await api("reference", "POST", file);
        if (!validRef(r.id)) throw Error("啟動器沒有回傳錄音編號。");
        const clip = { id: r.id, name: file.name, seconds: r.seconds };
        if (!cleanNames.has(r.id)) cleanNames.set(r.id, clip);
        cleanChoices.set(r.id, cleanNames.get(r.id));
        cleanQueue.push(clip);
        accepted++;
      } catch (e) {
        problems.push(file.name + "：" + e.message);
      }
    }
  } finally {
    cleanImporting = false;
    if (cleanOpen) {
      drawCleanQueue();
      fillCleanPicker();
      notice([accepted && "已匯入 " + accepted + " 段 WAV。", ...problems].filter(Boolean).join(" "));
    }
  }
}
async function startClean() {
  if ($("clean-run").disabled) return;
  cleanSubmitting = true;
  drawCleanQueue();
  try {
    currentJob = await api("clean", "POST", {
      references: cleanQueue.map(({ id }) => id),
      mode: cleanMode,
      gapMs: cleanGapValue(),
    });
    showJob();
  } finally {
    cleanSubmitting = false;
    if (cleanOpen) drawCleanQueue();
  }
}
let cleanHistoryRequest = 0;
async function loadCleanHistory(mark = renderID) {
  const history = $("clean-history-content");
  if (!history) return;
  const request = ++cleanHistoryRequest;
  const live = () => mark === renderID && cleanOpen && request === cleanHistoryRequest;
  try {
    const rows = await api("clean/history");
    if (!live()) return;
    const files = (rows || []).filter((row) => typeof row.id === "string" && /^[a-f0-9]{32}$/.test(row.id));
    history.replaceChildren(...(files.length ? files.map(({ id, mode, count, seconds }) =>
      el("article", { className: "clean-result" },
        el("h3", {}, cleanModes[mode] || "清理完成"),
        hint(count + " 段 · " + Number(seconds).toFixed(1) + " 秒"),
        box("history-actions",
          el("audio", { controls: true, preload: "none", src: cleanAudioURL(id), "aria-label": "試聽清理結果 " + id.slice(0, 8) }),
          el("a", { className: "linkbtn", href: cleanAudioURL(id) + "?download=1", download: "" }, "匯出 WAV"),
        ),
      ),
    ) : [hint("完成清理後，可在這裏試聽和下載 WAV。")]));
  } catch (e) {
    if (live()) history.replaceChildren(hint("未能讀取清理歷史：" + e.message));
  }
}
function cleaning() {
  const realMark = renderID;
  cleanChoices = collectCleanRefs(Array.from(cleanChoices.values()));
  const upload = el("input", {
    id: "clean-upload", type: "file", accept: mediaAccept, multiple: true,
    onchange: (e) => cleanUpload(e.target).catch(notice),
  });
  const picker = el("select", {
    id: "clean-existing",
    onchange: (e) => {
      cleanChoice = e.target.value;
      cleanPreview();
      cleanStatus();
    },
  });
  const mode = cleanPicker(cleanModes, cleanMode, (v) => { cleanMode = v; }, "clean-mode");
  const gap = el("input", {
    type: "number", id: "clean-gap", min: "0", max: String(cleanMaxGap), step: "1", value: cleanGap,
    "aria-describedby": "clean-length",
    oninput: (e) => { cleanGap = e.target.value; cleanStatus(); },
  });
  $("work").replaceChildren(
    box("grid",
      card("加入錄音",
        field("匯入錄音（可選多個）", upload),
        field("已存參考錄音", picker),
        el("p", { id: "clean-refs-info", className: "hint", role: "status" }),
        el("div", { id: "clean-reference-preview", className: "clean-preview" }),
        btn("加入所選錄音", () => {
          const clip = cleanChoices.get(cleanChoice);
          if (!clip || cleanEditLocked() || cleanQueue.length >= cleanMaxClips) return;
          cleanQueue.push(clip);
          drawCleanQueue();
        }, { id: "clean-add" }),
      ),
      card("清理及合併",
        mode,
        hint("清理和訓練流程統一輸出 32 kHz WAV。"),
        field("段與段之間的間隔（毫秒）", gap),
        hint("最多 24 段；合併後連間隔最長 180 秒。"),
        el("p", { id: "clean-length", className: "hint", role: "status" }),
        btn("開始清理", startClean, { id: "clean-run", className: "primary" }),
      ),
    ),
    card("清理次序",
      el("p", { id: "clean-queue-empty", className: "hint" }, "先匯入錄音 或加入已存錄音。順序會用於合併。"),
      el("ol", { id: "clean-queue", className: "clean-queue", "aria-label": "待清理錄音次序" }),
      el("div", { id: "clean-asr-work" }),
    ),
    card("清理歷史",
      el("div", { id: "clean-history-content", "aria-live": "polite" }, hint("正在讀取清理歷史…")),
    ),
  );
  drawCleanQueue();
  loadCleanRefs(realMark);
  loadCleanHistory(realMark);
}

const asrContexts = new Map();
let asrOwnerID = 0;
const asrOwners = new WeakMap();
function ownerID(owner) {
  if (!asrOwners.has(owner)) asrOwners.set(owner, ++asrOwnerID);
  return asrOwners.get(owner);
}
function knownRefLanguage(v, fallback = "auto") {
  return Object.hasOwn(referenceLanguages, v) ? v : fallback;
}
function refLanguage(k, settings, ref = draft.reference || "", voice = selectedVoice()) {
  const fallback = k === "gpt-sovits" ? "yue" : "auto";
  const matching = settings.refLangFor === ref || !settings.refLangFor;
  return knownRefLanguage(
    (settings.refLangFor === ref && settings.refLang) ||
    voice?.language || (matching && settings.refLang), fallback);
}
function asrContext(key, source) {
  let ctx = asrContexts.get(key);
  if (!ctx) {
    ctx = {
      reference: source.reference,
      language: knownRefLanguage(source.language),
      text: source.text || "",
      manual: !!source.manual,
      ticket: 0, pending: false, requested: false, message: "", suggestion: null,
    };
    asrContexts.set(key, ctx);
  }
  ctx.commit = source.commit;
  return ctx;
}
function speakerASR(k, settings) {
  const reference = draft.reference || "", voice = selectedVoice(), identity = voice?.id || "";
  const key = ["synth", k, reference, identity, voice?.transcript || ""].join("|");
  const initialText = reference ? referenceTranscript(settings) || "" : "";
  const bound = settings.transcriptFor === reference && (!voice || settings.transcriptVoice === identity);
  const owner = asrContext(key, {
    reference, text: initialText, language: refLanguage(k, settings, reference, voice),
    manual: bound ? settings.transcriptManual !== false && (!!initialText || settings.transcriptManual === true) : !!initialText,
    commit: (ctx) => {
      if ((draft.reference || "") !== reference || (draft.voiceId || "") !== identity || !reference) return;
      settings.refLangFor = reference;
      settings.refLang = ctx.language;
      settings.transcriptFor = reference;
      settings.transcriptVoice = identity;
      settings.transcript = ctx.text;
      settings.transcriptManual = ctx.manual;
      changed();
    },
  });
  return owner;
}
function emotionASR(settings) {
  const reference = settings.emotionReference || "";
  const saved = settings.emotionASR?.reference === reference ? settings.emotionASR : {};
  return asrContext(["emotion", ownerID(settings), reference].join("|"), {
    reference, text: saved.text, language: saved.language, manual: saved.manual,
    commit: (ctx) => {
      if (!reference || settings.emotionReference !== reference) return;
      settings.emotionASR = { reference, language: ctx.language, text: ctx.text, manual: ctx.manual };
      changed();
    },
  });
}
function profileASR(voice) {
  const reference = voice.reference || "";
  return asrContext(["voice", ownerID(voice), reference].join("|"), {
    reference, language: voice.language, text: voice.transcript, manual: !!voice.transcript,
    commit: (ctx) => {
      if (voice === editingVoice && reference === voice.reference) {
        voice.language = ctx.language;
        voice.transcript = ctx.text;
      }
    },
  });
}
function asrPaint(ctx) {
  const bound = ctx.view;
  if (!bound) return;
  if (bound.text.value !== ctx.text) bound.text.value = ctx.text;
  if (bound.language.value !== ctx.language) bound.language.value = ctx.language;
  bound.status.textContent = ctx.message || (!ctx.reference ? "先匯入或選擇參考錄音。" : "");
  bound.retry.disabled = !ctx.reference || ctx.pending;
  bound.apply.hidden = !ctx.suggestion;
  bound.apply.disabled = !ctx.suggestion;
  bound.proposal.hidden = !ctx.suggestion;
  bound.proposal.textContent = ctx.suggestion?.text || "";
  if (bound.text.id === "transcript" && bound.root.isConnected) syncSynthesisControls();
}
async function recognizeReference(ctx) {
  if (!ctx.reference) return;
  const ticket = ++ctx.ticket, language = ctx.language, reference = ctx.reference;
  ctx.requested = true;
  ctx.pending = true;
  ctx.suggestion = null;
  ctx.message = "正在辨識本機錄音…";
  asrPaint(ctx);
  try {
    const result = await api("asr", "POST", { reference, language, ...(asrChoice ? {model:asrChoice} : {}) });
    if (ticket !== ctx.ticket) return;
    const text = typeof result.text === "string" ? result.text.trim().slice(0, 10000) : "";
    if (!text) throw Error("未辨識出清楚對白。");
    const label = [result.model || (typeof result.provider === "string" && result.provider),
      typeof result.language === "string" && (lang[result.language] || result.language)].filter(Boolean).join(" · ");
    if (ctx.manual) {
      ctx.suggestion = text === ctx.text ? null : { text };
      ctx.message = "已保留手動編輯" + (ctx.suggestion ? "；可另行採用辨識結果" : "") + (label ? "（" + label + "）" : "") + "。";
    } else {
      ctx.text = text;
      ctx.commit?.(ctx);
      ctx.message = "逐字稿已自動填入，可先校對" + (label ? "（" + label + "）" : "") + "。";
    }
    if(result.warning) ctx.message+=" "+result.warning;
  } catch (e) {
    if (ticket !== ctx.ticket) return;
    ctx.message = "辨識未完成：" + e.message + " 可重試或手動輸入。";
  } finally {
    if (ticket === ctx.ticket) {
      ctx.pending = false;
      asrPaint(ctx);
    }
  }
}
function asrPanel(prefix, ctx, options = {}) {
  const language = select(referenceLanguages, ctx.language, (value) => {
    if (value === ctx.language) return;
    ctx.language = knownRefLanguage(value);
    if (!ctx.manual) ctx.text = "";
    ctx.commit?.(ctx);
    recognizeReference(ctx);
  }, options.languageID || prefix + "-language");
  language.disabled = !ctx.reference && !!options.disableLanguageWithoutRef;
  const text = area(options.textID || prefix + "-transcript", ctx.text, "可手動校對錄音中的實際對白", (value) => {
    ctx.text = value;
    ctx.manual = true;
    ctx.commit?.(ctx);
    asrPaint(ctx);
  });
  text.maxLength = 10000;
  text.disabled = !ctx.reference;
  const status = el("p", { id: prefix + "-status", className: "asr-status hint", role: "status" });
  const proposal = el("p", { className: "asr-status hint", hidden: true });
  const retry = btn("重新辨識", () => recognizeReference(ctx));
  const apply = btn("採用辨識結果", () => {
    if (!ctx.suggestion) return;
    ctx.text = ctx.suggestion.text;
    ctx.suggestion = null;
    ctx.manual = false;
    ctx.message = "已採用辨識結果，可繼續校對。";
    ctx.commit?.(ctx);
    asrPaint(ctx);
  });
  const root = box("asr-panel",
    field(options.languageLabel || "錄音語言", language),
    field("辨識模型", asrModelPicker(model=>{ saveASRChoice(model);recognizeReference(ctx); })),
    field(options.textLabel || "參考逐字稿", text),
    status, proposal, box("actions asr-actions", retry, apply),
    ...(options.hint ? [hint(options.hint)] : []),
  );
  ctx.view = { root, text, language, status, proposal, retry, apply };
  asrPaint(ctx);
  if (ctx.reference && !ctx.requested && !ctx.text && !ctx.manual) recognizeReference(ctx);
  return root;
}

let cleanASRReference = "";
function drawCleanASR() {
  const panel = $("clean-asr-work");
  if (!panel) return;
  const clip = cleanQueue.find(({ id }) => id === cleanASRReference);
  if (!clip) {
    cleanASRReference = "";
    panel.replaceChildren();
    return;
  }
  if (panel.dataset.reference === clip.id && panel.firstChild) return;
  panel.dataset.reference = clip.id;
  const ctx = asrContext("clean|" + clip.id, { reference: clip.id, language: "auto" });
  panel.replaceChildren(el("h3", {}, "辨識：" + clip.name),
    asrPanel("clean-asr", ctx, { hint: "逐字稿只供校對，不會改變清理或合併作業。" }));
}

let ttsSwitching=false;
function gptVariant(settings, engine) {
  if (engine.variants.includes(settings.variant)) return settings.variant;
  if (engine.availableVariants?.length && !engine.availableVariants.includes("v2Pro")) return engine.availableVariants[0];
  return "v2Pro";
}
function selectedTTS(k, settings, engine, mode){
  const variant=k==="gpt-sovits"?gptVariant(settings,engine):mode.variant||engine.variants[0];
  return {engine:k,variant,modelProject:k==="gpt-sovits"&&variant==="v2Pro"?selectedTrainedModel(settings):""};
}
function residentName(p){
  const project=trainedModels.find(m=>m.id===p.modelProject)?.name;
  return [p.kind==="sts"?(stsNames[p.engine]||p.engine):catalog[p.engine]?.[0]||p.engine,
    variantName(p.variant),project||(p.modelProject?"微調權重":"")].filter(Boolean).join(" · ");
}
function sameTTS(a,b){
  return a.kind==="tts"&&a.engine===b.engine&&a.variant===b.variant&&(a.modelProject||"")===(b.modelProject||"");
}
async function ttsWaitJob(job) {
  if(job?.kind!=="tts-warm")return;
  currentJob=job;showJob();
  await waitForJob(job,{timeout:5*60*1000});
}
async function startResidentTTS(target){
  if(ttsSwitching)return false;
  ttsSwitching=true;
  try{
    const resident=await api("tts/resident");
    if(resident.busy)throw Error("請等目前作業完成，才切換模型。");
    if(!resident.id||sameTTS(resident,target)){
      try{
        await ttsWaitJob(await api("tts/warm","POST",target));
        return true;
      }catch(e){if(!resident.id||e.status!==409)throw e;}
    }
    if(!window.confirm("目前常駐："+residentName(resident)+"。\n先停止佢，再啟動所選 TTS 模型？"))return false;
    await ttsWaitJob(await api("tts/warm","POST",{...target,replace:resident.id}));
    return true;
  }finally{ttsSwitching=false;}
}
async function confirmSTSResident() {
  const current=await api("tts/resident");
  if(current.busy)throw Error("請等目前作業完成。");
  if(!current.id || current.kind==="sts"&&current.engine===sts.model)return "";
  return window.confirm("目前常駐："+residentName(current)+"。先停止佢，再使用所選 STS 模型？") ? current.id : null;
}
function ttsResidentPanel(target){
  const status=el("p",{role:"status",id:"tts-resident-status"},"正在讀取常駐模型…");
  const start=btn("開始啟動 TTS 模型",async()=>{
    start.disabled=true;
    try{await startResidentTTS(target)}finally{if(status.isConnected)refresh()}
  });
  const stop=btn("停止 TTS 模型",async()=>{
    const p=await api("tts/resident");
    if(p.kind==="tts")await api("tts/stop","POST",{id:p.id});
    if(status.isConnected)refresh();
  },{disabled:true});
  async function refresh(){
    try{
      const p=await api("tts/resident");
      status.textContent=p.id?"常駐中："+residentName(p):p.busy?"正在處理作業。":"目前無常駐模型。";
      stop.disabled=p.kind!=="tts"||p.busy;
    }catch(e){status.textContent=e.message}
    start.disabled=false;
  }
  refresh();
  return box("stack",status,box("actions",start,stop));
}
function synthesis() {
  const k = draft.engine,
    settings = d(),
    engine = engines[k];
  const mode = selectedMode(engine, settings.mode);
  function update(key, v) {
    settings[key] = v;
    changed();
  }
  const script = area(
    "text",
    draft.text || "",
    "輸入要生成的台詞，例如「今晚八點半喺旺角站等。」",
    (v) => {
      draft.text = v;
      changed();
    },
  );
  script.maxLength = 10000;
  const modes = Object.fromEntries(
    engine.modes.map(({ id, label }) => [id, label]),
  );
  const modeCtrl = select(
    modes,
    mode.id,
    (v) => {
      update("mode", v);
      render();
    },
    "mode",
  );
  const speed = select(
    {
      0.5: "0.5 ×",
      0.75: "0.75 ×",
      1: "1.0 ×",
      1.25: "1.25 ×",
      1.5: "1.5 ×",
      2: "2.0 ×",
    },
    String(settings.speed || 1),
    (v) => update("speed", Number(v)),
    "speed",
  );
  const ref = el("input", {
    type: "file",
    id: "reference",
    accept: mediaAccept,
    onchange: async (e) => {
      const file = e.target.files[0];
      if (!file) return;
      const upload = ++referenceMutation;
      try {
        const res = await api("reference", "POST", file);
        if (upload !== referenceMutation || draft.engine !== k || panel() !== "synth" ||
          !selectedMode(engines[k], settings.mode).reference) return;
        const chosen = speakerASR(k, settings).language;
        replaceReference(res.id, file.name);
        settings.refLang = chosen;
        settings.refLangFor = res.id;
        const asr = speakerASR(k, settings);
        asr.language = chosen;
        recognizeReference(asr);
        persist();
        render();
      } catch (err) {
        if (upload === referenceMutation && draft.engine === k && panel() === "synth") notice(err);
      }
    },
  });
  const source = [];
  const right = [box("row", field("工作模式", modeCtrl), ...(engine.speed ? [field("語速", speed)] : []))];
  const gptModel = k === "gpt-sovits" ? gptVariant(settings,engine) : "";
  if (k === "gpt-sovits" && engine.variants.length > 1) {
    right.push(field("GPT-SoVITS 基礎版本", select(Object.fromEntries(engine.variants.map(v=>
      [v,variantName(v)+(engine.availableVariants?.includes(v)?"":"（未就緒）")])),gptModel,
      v=>{update("variant",v);render();},"gpt-variant")));
  }
  if (gptModel === "v2Pro") {
    const selected = selectedTrainedModel(settings);
    right.push(
      field("GPT-SoVITS 模型", select(modelOptions("v2Pro 基礎模型", selected), selected, (id) => {
        settings.modelVoice = draft.voiceId || "";
        update("modelProject", id);
        render();
      }, "trained-model")),
      box("actions",
        btn("匯入微調模型 ZIP", () => showModelImport(model => {
          settings.modelVoice = draft.voiceId || "";
          update("modelProject", model.id);
          persist();
        })),
        ...(selectedVoice() && selected && !selectedVoice().modelProjects?.includes(selected) ? [
          btn("將所選模型加入此角色", async () => {
            const voice = selectedVoice();
            const updated = await api("profiles/"+voice.id,"PUT",{...voice,modelProjects:[...(voice.modelProjects||[]),selected]});
            voices = voices.map(item=>item.id===updated.id?updated:item);
            render();
            notice("已連結「"+updated.name+"」；可在聲音庫設定預設模型。");
          })
        ] : [])),
    );
  }
  if (mode.reference) {
    if (voices.length)
      source.push(field("角色", select(
        { "": "單次錄音", ...Object.fromEntries(voices.map((voice) => [voice.id, voice.name])) },
        draft.voiceId || "",
        (value) => {
          const voice = voices.find((item) => item.id === value);
          if (voice) {
            useVoice(voice);
            const asr = speakerASR(k, settings);
            if (!asr.text && !asr.requested && !asr.manual) recognizeReference(asr);
          } else replaceReference("", "");
          persist();
          render();
        },
        "voice",
      )));
    if (voices.length) source.push(box("voice-choices",...voices.map(voice=>{
      const b=btn("",()=>{useVoice(voice);const asr=speakerASR(k,settings);if(!asr.text&&!asr.pending&&!asr.manual)recognizeReference(asr);persist();render()},
        {"aria-pressed":String(draft.voiceId===voice.id),"aria-label":"選擇角色 "+voice.name,title:voice.name,className:"voice-choice"});
      b.append(avatarIcon(voice),el("span",{},voice.name));return b;
    })));
    source.push(field("參考錄音", el("span", { className: "filebtn" }, ref)));
    if (engine.minReference)
      source.push(hint(`使用 ${engine.minReference} 至 ${engine.maxReference} 秒的單人錄音。`));
    else if (engine.maxReference)
      source.push(hint(`使用最多 ${engine.maxReference} 秒的清晰單人錄音。`));
    if (draft.reference)
      source.push(
        el("p", { className: "file-name" }, draft.referenceName || "已匯入錄音"),
        el("audio", {
          controls: true,
          preload: "metadata",
          src: "/api/reference/" + draft.reference,
        }),
      );
  }
  if (mode.reference && draft.reference)
    source.push(asrPanel("reference-asr", speakerASR(k, settings), {
      textID: "transcript", languageID: "refLang",
      textLabel: "參考逐字稿",
      hint: mode.transcript ? "" : "此模式只用參考音訊；逐字稿只供查看與校對。",
    }));
  else if (mode.reference) source.push(hint("匯入錄音後，可揀語言並自動辨識逐字稿。"));
  if (mode.instruction || mode.style)
    right.push(
      field(
        mode.id === "design" ? "聲線描述" : mode.emotion === "text" ? "情緒描述" : "講法指示",
        area(
          "instruction",
          settings.instructions?.[mode.id] ?? settings.instruction ?? "",
          "例如：請用廣東話，節奏稍慢，像跟朋友聊天。",
          (v) => {
            settings.instructions ||= {};
            settings.instructions[mode.id] = v;
            changed();
          },
        ),
      ),
    );
  if (mode.speaker)
    right.push(field("預設聲線", select(
      Object.fromEntries(engine.speakers.map((name) => [name, name])),
      settings.speaker || engine.speakers[0], (value) => update("speaker", value), "speaker",
    )));
  if (mode.emotion) right.push(...emotionFields(mode.emotion, settings, update));
  if (mode.languages) {
    const options = Object.fromEntries(engine.languageOptions.map((id) => [id, lang[id]]));
    right.push(
      box(
        "row",
        field(
          "台詞語言",
          select(
            options,
            options[settings.language] ? settings.language : engine.languageOptions[0],
            (v) => update("language", v),
            "language",
          ),
        ),
      ),
    );
  }
  const controls = box(
    "actions",
    btn("試讀第一句", () => submit(true), { className: "run" }),
    btn("生成全文", () => submit(false), { className: "primary run" }),
  );
  const gap=field("段落停頓（ms）",el("input",{type:"number",min:0,max:5000,value:settings.splitGapMs??150,onchange:e=>update("splitGapMs",Number(e.target.value))}));
  gap.hidden=!!settings.splitSeparate;
  const splitFields = box("row",
    field("每段最多字數", el("input", { type:"number", min:20, max:2000, value:settings.splitAt || 160, onchange:e=>update("splitAt", Number(e.target.value)) })),
    gap,field("合併輸出",el("input",{type:"checkbox",checked:!settings.splitSeparate,onchange:e=>{update("splitSeparate",!e.target.checked);gap.hidden=!e.target.checked}})));
  splitFields.hidden = !settings.splitEnabled;
  const splitting=el("label",{className:"dataset-option"},el("input",{type:"checkbox",checked:!!settings.splitEnabled,onchange:e=>{update("splitEnabled", e.target.checked);splitFields.hidden=!e.target.checked;}}),"切分生成");
  const left = card("2 · 朗讀台詞", field("要讀出的文字", script), el("details",{},el("summary",{},"切分及輸出設定"),splitting,splitFields), box("studio-generate",el("strong",{},"3 · 生成"),controls));
  if (!canRun(engine, mode, gptModel || mode.variant))
    left.append(
      box(
        "actions",
        hint(gptModel || mode.variant ? "合成前請先連接 " + variantName(gptModel || mode.variant) + " 模型。" : "合成前請先連接引擎。"),
        btn("前往模型管理", () => setTab("models")),
      ),
    );
  const setup=card("1 · 角色及參考",...(source.length?source:[hint("呢個模式毋須參考錄音。可喺右邊調整所需設定。")]));
  const inspector=card("引擎設定",hint(variantName(gptModel || mode.variant)||engine.variants.map(variantName).join(" · ")),...right,
    el("details",{open:true},el("summary",{},"常駐及停止模型"),ttsResidentPanel(selectedTTS(k,settings,engine,mode))));
  $("work").replaceChildren(box("studio-editor",box("stack studio-main",setup,left),box("studio-inspector",inspector)));
}
function emotionFields(kind, settings, update, options = {}) {
  const fields = [];
  if (kind === "audio") {
    const input = el("input", {
      type: "file", accept: mediaAccept, id: options.id ?? "emotion-reference",
      onchange: async (event) => {
        const file = event.target.files[0];
        if (!file) return;
        if (options.upload) return options.upload(file);
        const upload = ++emotionUploadTicket;
        try {
          const ref = await api("reference", "POST", file);
          if (upload !== emotionUploadTicket || draft.engine !== "indextts2" || panel() !== "synth" ||
            selectedMode(engines.indextts2, settings.mode).emotion !== "audio") return;
          const language = emotionASR(settings).language;
          update("emotionReference", ref.id);
          update("emotionName", file.name);
          const asr = emotionASR(settings);
          asr.language = language;
          recognizeReference(asr);
          render();
        } catch (err) {
          if (upload === emotionUploadTicket && draft.engine === "indextts2" && panel() === "synth") notice(err);
        }
      },
    });
    fields.push(field("情緒參考錄音（最多 15 秒）", input));
    if (settings.emotionReference) fields.push(
      hint(settings.emotionName || "已匯入情緒錄音"),
      el("audio", { controls: true, preload: "none", src: "/api/reference/" + settings.emotionReference }),
    );
    if (!options.upload) fields.push(asrPanel("emotion-asr", emotionASR(settings), {
      languageLabel: "情緒錄音語言", textLabel: "情緒錄音逐字稿",
      hint: "只供查看與校對，不會送入情緒合成。",
    }));
  }
  if (kind === "vector") {
    const weights = settings.emotionWeights || Array(8).fill(0);
    fields.push(box("grid", ...emotions.map((name, index) => field(name, el("input", {
      type: "number", step: "0.05", min: 0, max: 1, value: weights[index],
      onchange: (event) => {
        weights[index] = event.target.valueAsNumber || 0;
        update("emotionWeights", weights);
      },
    })))));
  }
  fields.push(field("情緒強度（0 至 1）", el("input", {
    type: "number", step: "0.05", min: 0, max: 1, value: settings.emotionStrength ?? 1,
    onchange: (event) => update("emotionStrength", event.target.valueAsNumber || 0),
  })));
  return fields;
}
async function submit(preview) {
  const k = draft.engine,
    s = d();
  const engine = engines[k];
  const mode = selectedMode(engine, s.mode);
  const payload = {
    engine: k,
    mode: mode.id,
    text: draft.text || "",
    ...(mode.transcript ? { transcript: speakerASR(k, s).text } : {}),
    instruction: mode.instruction || mode.style ? s.instructions?.[mode.id] ?? s.instruction ?? "" : "",
    reference: mode.reference ? draft.reference || "" : "",
    voiceId: mode.reference ? draft.voiceId || "" : "",
    language: engine.languageOptions?.includes(s.language) ? s.language : engine.languageOptions?.[0] || "yue",
    ...(k === "gpt-sovits" && mode.reference ? { refLang: speakerASR(k, s).language } : {}),
    speed: engine.speed ? s.speed || 1 : 1,
    preview,
  };
  if (s.splitEnabled && !preview) {
    payload.splitAt=s.splitAt||160;
    payload.splitGapMs=s.splitGapMs??150;
    payload.splitSeparate=!!s.splitSeparate;
  }
  if (mode.variant) payload.variant = mode.variant;
  if (k === "gpt-sovits") {
    payload.variant = gptVariant(s,engine);
    if(payload.variant==="v2Pro") payload.modelProject = selectedTrainedModel(s);
    payload.useBaseModel = !payload.modelProject;
  }
  if (mode.speaker) payload.speaker = s.speaker || engine.speakers[0];
  if (mode.emotion) payload.emotionStrength = s.emotionStrength ?? 1;
  if (mode.emotion === "audio") payload.emotionReference = s.emotionReference || "";
  if (mode.emotion === "vector") payload.emotionWeights = s.emotionWeights || Array(8).fill(0);
  const resident=await api("tts/resident");
  const target=selectedTTS(k,s,engine,mode);
  if(resident.id&&!sameTTS(resident,target) && !await startResidentTTS(target))return;
  persist();
  currentJob = await api("synthesize", "POST", payload);
  showJob();
}
function voiceLibrary() {
  const list = card(
    "角色",
    btn("新增角色", () => {
      editingVoice = {
        name: "",
        reference: draft.reference || "",
        transcript: speakerASR(draft.engine, d()).text,
        language: speakerASR(draft.engine, d()).language,
      };
      render();
    }, { className: "primary" }),
  );
  for (const voice of voices) {
    list.append(box(
      "stack",
      el("h3", {className:"voice-title"}, avatarIcon(voice), voice.name),
      el("audio", { controls: true, preload: "none", src: "/api/reference/" + voice.reference }),
      box("actions",
        btn("使用", () => {
          useVoice(voice);
          setTab("synth");
        }),
        btn("編輯", () => { editingVoice = { ...voice }; render(); }),
      ),
    ));
  }
  if (!editingVoice) {
    $("work").replaceChildren(list);
    return;
  }
  const voice = editingVoice;
  const upload = el("input", {
    type: "file", accept: mediaAccept,
    onchange: async (event) => {
      const file = event.target.files[0];
      if (!file) return;
      const upload = ++voiceUploadTicket;
      try {
        const ref = await api("reference", "POST", file);
        if (upload !== voiceUploadTicket || editingVoice !== voice || panel() !== "voices") return;
        if (ref.id !== voice.reference) voice.transcript = "";
        voice.reference = ref.id;
        recognizeReference(profileASR(voice));
        render();
      } catch (err) {
        if (upload === voiceUploadTicket && editingVoice === voice && panel() === "voices") notice(err);
      }
    },
  });
  const edit = card(
    voice.id ? "編輯角色" : "新增角色",
    field("名稱", el("input", {
      type: "text", value: voice.name, maxLength: 80,
      oninput: (event) => { voice.name = event.target.value; },
    })),
    field("參考錄音", el("span", { className: "filebtn" }, upload)),
    field("角色頭像（JPEG／PNG）",box("avatar-picker",avatarIcon(voice),el("input",{type:"file",accept:".png,.jpg,.jpeg",onchange:async(event)=>{
      const f=event.target.files[0];if(!f)return;
      try{const v=await api("avatars","POST",f);if(editingVoice===voice){voice.avatar=v.id;render()}}catch(e){notice(e)}
    }}),voice.avatar?btn("移除頭像",()=>{delete voice.avatar;render()}):el("span"))),
    ...(voice.reference ? [el("audio", {
      controls: true, preload: "metadata", src: "/api/reference/" + voice.reference,
    })] : []),
    asrPanel("voice", profileASR(voice)),
    profileModelControls(voice),
    box("actions",
      btn("儲存角色", async () => {
        const saved = await api(voice.id ? "profiles/" + voice.id : "profiles", voice.id ? "PUT" : "POST", voice);
        voices = await api("profiles");
        if (draft.voiceId === saved.id) {
          useVoice(saved);
          persist();
        }
        editingVoice = null;
        render();
        notice("角色已儲存。");
      }, { className: "primary" }),
      btn("取消", () => { editingVoice = null; render(); }),
    ),
  );
  if (voice.id) edit.append(btn("移除角色", async () => {
    if (!window.confirm("移除「" + voice.name + "」？已儲存錄音和歷史仍可使用。")) return;
    await api("profiles/" + voice.id, "DELETE");
    voices = voices.filter((item) => item.id !== voice.id);
    if (draft.voiceId === voice.id) {
      delete draft.voiceId;
      persist();
    }
    editingVoice = null;
    render();
  }));
  $("work").replaceChildren(box("grid", list, edit));
}
function profileModelControls(voice) {
  const keys = [...new Set([voice.modelProject, ...(voice.modelProjects||[])].filter(Boolean))];
  const attached = {"":"v2Pro 基礎模型"};
  for (const key of keys) attached[key] = trainedModels.find(m=>m.id===key)?.name || "找不到已連結的模型";
  const available = trainedModels.filter(m=>!keys.includes(m.id));
  return box("stack",
    field("預設 GPT-SoVITS 模型",select(attached,voice.modelProject||"",key=>{voice.modelProject=key;},"voice-trained-model")),
    ...(keys.length ? keys.map(key=>box("model-binding",el("span",{},attached[key]),
      btn("解除連結",()=>{voice.modelProjects=keys.filter(x=>x!==key);if(voice.modelProject===key)voice.modelProject="";render();}))) :
      [hint("此角色未連結微調模型。")]),
    ...(available.length ? [field("加入另一個微調模型",select(
      {"":"選擇要加入的模型",...Object.fromEntries(available.map(m=>[m.id,m.name]))},"",key=>{
        if(key){voice.modelProjects=[...keys,key];render();}
      },"voice-add-model"))] : []));
}
const upstreamEngineLinks = {
  "gpt-sovits": "https://huggingface.co/lj1995/GPT-SoVITS-windows-package/tree/main",
  cosyvoice3: "https://github.com/FunAudioLLM/CosyVoice",
  voxcpm2: "https://github.com/OpenBMB/VoxCPM",
  indextts2: "https://github.com/index-tts/index-tts",
  "qwen3-tts": "https://github.com/QwenLM/Qwen3-TTS",
};
const upstreamModelLinks = {
  "gpt-sovits": "https://huggingface.co/lj1995/GPT-SoVITS",
  cosyvoice3: "https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512",
  voxcpm2: "https://huggingface.co/openbmb/VoxCPM2",
  indextts2: "https://huggingface.co/IndexTeam/IndexTTS-2",
};
function downloadControl(engine, kind, label, variant = "") {
  const suffix = new URLSearchParams({ kind, variant }).toString();
  const route = engine + "?" + suffix;
  const info = el("div", { className: "stack", role: "status" });
  const gb = (bytes) => (bytes / 1e9).toFixed(2) + " GB";
  const lookup = async () => {
    info.textContent = "正在查詢下載版本…";
    try {
      const p = await api("pack/" + route);
      info.replaceChildren(
        hint(p.source + " · " + p.version + "　下載 " + gb(p.bytes) + "　安裝後 " + gb(p.expanded)
          + "　可用空間 " + gb(p.free)),
        btn("確認下載並安裝", async () => {
          currentJob = await api("install/" + route, "POST", { hash: p.hash });
          showJob();
        }, { className: "primary" }),
      );
    } catch (err) {
      info.textContent = err.message;
      if (kind === "runtime" && upstreamEngineLinks[engine]) {
        info.append(el("a", {
          className: "linkbtn", href: upstreamEngineLinks[engine], target: "_blank", rel: "noopener noreferrer",
        }, engine === "gpt-sovits" ? "開啟官方 Windows 下載頁" : "查看上游安裝說明"));
      }
      if (kind === "model") {
        const href = engine === "qwen3-tts"
          ? "https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-" + variant
          : upstreamModelLinks[engine];
        if (href) info.append(el("a", {
          className: "linkbtn", href, target: "_blank", rel: "noopener noreferrer",
        }, "開啟官方模型頁"));
      }
    }
  };
  return box("stack", box("actions", btn(label, lookup)), info);
}

async function refreshAssets(message) {
  const [state, models] = await Promise.all([api("state"), api("trained-models")]);
  engines = state.engines;
  trainedModels = models;
  render();
  notice(message);
}

function models() {
  const k = draft.engine;
  const engine = engines[k];
  const root = engine.path;
  const path = el("input", { type: "text", id: "path", placeholder: root });
  const versions = el("div", { className: "stack" });
  const runtime = card(
    "引擎",
    hint(engine.engineInstalled ? "已找到引擎程式" : "未安裝引擎程式"),
    downloadControl(k, "runtime", "下載引擎"),
    versions,
  );
  api("installed-packs/" + k)
    .then((packs) => {
      if (!packs.length) return;
      versions.replaceChildren(
        el("h3", {}, "已安裝引擎版本"),
        ...packs.map((pack) => {
          const label = pack.version || pack.id;
          return box("stack",
            hint(label + (pack.intact ? (pack.active ? " · 使用中" : "") : " · 缺少引擎檔案")),
            box("actions",
              ...(!pack.active && pack.intact ? [btn("選用引擎版本", async () => {
                await api("installed-packs/" + k, "POST", { id: pack.id });
                await refreshAssets("已切換引擎版本。");
              })] : []),
              ...(pack.managed ? [btn("刪除引擎", async () => {
                if (!window.confirm("刪除引擎 " + label + "？模型、錄音及紀錄會保留。")) return;
                await api("installed-packs/" + k + "/" + pack.id, "DELETE");
                await refreshAssets("已刪除引擎，錄音、模型及紀錄已保留。");
              })] : []),
            ),
          );
        }),
      );
    })
    .catch((err) => { versions.textContent = err.message; });

  const official = card("官方模型");
  const installed = api("installed-models/" + k);
  for (const variant of engine.variants) {
    const files = el("div", { className: "stack" });
    official.append(box("stack",
      el("h3", {}, variantName(variant)),
      hint(engine.availableVariants?.includes(variant) ? "已找到模型檔案" : "未安裝此模型"),
      downloadControl(k, "model", "下載模型", variant),
      files,
    ));
    installed.then((items) => {
        files.replaceChildren(...items.filter((item) => item.variant === variant).map((item) =>
          box("stack",
            hint([item.version || item.id, item.provider, item.source === "bundle" ? "原整合包" : ""]
              .filter(Boolean).join(" · ")),
            box("actions", btn("刪除模型", async () => {
              if (!window.confirm("刪除 " + variantName(variant) + " 模型權重？錄音及生成紀錄會保留。")) return;
              await api("installed-models/" + k + "/" + encodeURIComponent(variant) + "/" + item.id
                + "?source=" + item.source, "DELETE");
              await refreshAssets("已刪除模型權重，錄音及紀錄已保留。");
            })),
            el("details", {}, el("summary", {}, "模型檔案位置"), el("p", { className: "path" }, item.path)),
          ),
        ));
      })
      .catch((err) => { files.textContent = err.message; });
  }

  const manifest = el("input", {
    type: "text", id: "manifest", placeholder: "D:\\Packs\\" + k + "-runtime.json",
  });
  const localPack = card(
    "離線匯入",
    hint("支援獨立引擎包、模型包及舊整合包。把清單及分卷放在同一資料夾。"),
    field("JSON 清單完整路徑", manifest),
    btn("校驗並安裝", async () => {
      if (!manifest.value.trim()) throw Error("請輸入 JSON 清單完整路徑。");
      currentJob = await api("import-pack/" + k, "POST", { path: manifest.value.trim() });
      showJob();
    }),
    el("details", {}, el("summary", {}, "下載舊整合包"), downloadControl(k, "", "下載整合包")),
  );
  const folder = card(
    "連接現有引擎",
    el("p", { className: "path" }, root),
    field("引擎資料夾", path),
    box("actions",
      btn("驗證並連接", async () => {
        if (!path.value.trim()) throw Error("請輸入引擎資料夾路徑。");
        await api("engine", "POST", { engine: k, path: path.value.trim() });
        await refreshAssets("已連接引擎。");
      }),
      btn("重新偵測", () => refreshAssets("已重新偵測引擎及模型。")),
      ...(engine.external ? [btn("解除連接", async () => {
        await api("engine", "POST", { engine: k, path: "" });
        await refreshAssets("已解除連接，原本資料夾仍可使用。");
      })] : []),
    ),
  );
  const trained = [];
  if (k === "gpt-sovits") {
    const v5Path = el("input", {type:"text", id:"gpt-v5-runtime", placeholder:engine.gptV5Path || "D:\\Engines\\GPT-SoVITS-V5"});
    trained.push(card("連接 GPT-SoVITS V5",
      hint(engine.gptV5Installed ? "已連接獨立 V5 引擎。唔會取代 v2Pro。" : "V5 Turbo 同 V5 Dev 用獨立引擎。連接後可下載或使用已校驗權重。"),
      ...(engine.gptV5Path?[el("p",{className:"path"},engine.gptV5Path)]:[]),
      field("V5 引擎資料夾",v5Path),box("actions",
        btn("驗證並連接 V5",async()=>{
          if(!v5Path.value.trim())throw Error("請輸入 V5 引擎資料夾。");
          await api("engine","POST",{engine:k,variant:"v5",path:v5Path.value.trim()});
          await refreshAssets("已連接 GPT-SoVITS V5。");
        }),
        ...(engine.gptV5Path?[btn("解除 V5 連接",async()=>{
          await api("engine","POST",{engine:k,variant:"v5",path:""});
          await refreshAssets("已解除 V5 連接；原始資料夾同 v2Pro 都保留。");
        })]:[]))));
  }
  if (k === "gpt-sovits" && gptVariant(d(),engine) === "v2Pro") {
    trained.push(card(
      "微調模型",
      btn("訓練角色模型", () => setTab("training")),
      btn("匯入微調模型 ZIP", () => showModelImport()),
      ...(trainedModels.length ? trainedModels.map((model) => box("stack", hint(model.name),
        box("actions",
        el("a",{className:"linkbtn",href:"/api/trained-models/"+model.id+"/download",download:""},"下載 ZIP"),
        btn("改名",async()=>{
          const name = window.prompt("微調模型名稱",model.name);
          if (name?.trim() && name.trim()!==model.name) {
            await api("trained-models/"+model.id,"PATCH",{name:name.trim()});
            await refreshAssets("微調模型已改名。");
          }
        }),
        btn("刪除微調模型", async () => {
          if (!window.confirm("刪除「" + model.name + "」的生成權重與訓練檢查點？專案、素材及紀錄會保留。")) return;
          await api("trained-models/" + model.id, "DELETE");
          await refreshAssets("已刪除微調權重，專案及素材已保留。");
        })),
      )) : [hint("完成微調後，模型會在這裏出現。")]),
      btn("重新偵測微調模型", () => refreshAssets("已重新偵測微調模型。")),
    ));
  }
  $("work").replaceChildren(box("grid", box("stack", runtime, folder, localPack),
    box("stack", official, ...trained)));
}

async function storageSettings() {
  const mark = renderID;
  const about=card("關於 TimbreDeck",hint(studioBuild ?
    "版本 "+studioBuild.version+" · 建置 "+studioBuild.id : "啟動程式未回報建置資料。"));
  const choice=card("工作室偏好",
    trainingToggle("減少介面動效及視差",!!draft.reduceMotion,v=>{
      draft.reduceMotion=v;
      document.body.dataset.motion=v?"reduce":"system";
      if(v)$("work").getAnimations().forEach(a=>a.cancel());
      scheduleDepth();changed();
    }),
    field("預設辨識模型",asrModelPicker(saveASRChoice)),
    btn("重新偵測模型",async()=>{asrOptions=await api("asr/models");render()}));
  $("work").replaceChildren(choice,about,hint("正在讀取錄音…"));
  try {
    const [unused, outputs] = await Promise.all([api("unused-recordings"), api("history")]);
    if (mark !== renderID) return;
    const refresh = async (message) => {
      if (panel() === "storage") {
        await storageSettings();
        notice(message);
      }
    };
    const reference = card(
      "未使用的參考錄音",
      hint("草稿、角色、訓練及歷史用到的參考錄音會保留。"),
      ...(unused.length ? unused.map((item) => box("stack",
        el("audio", { controls: true, preload: "none", src: "/api/reference/" + item.id }),
        box("actions", hint((item.bytes / 1e6).toFixed(2) + " MB"),
          btn("刪除參考錄音", async () => {
            if (!window.confirm("永久刪除這段未使用的參考錄音？")) return;
            await api("unused-recordings/" + item.id, "DELETE");
            await refresh("已刪除參考錄音。");
          })),
      )) : [hint("沒有可清理的參考錄音。")]),
    );
    const files = outputs.filter((output) => output.audioAvailable);
    const generated = card(
      "已生成的錄音",
      hint("刪除 WAV 後，歷史仍保留台詞、模型和合成設定。"),
      ...(files.length ? files.map((item) => box("stack",
        hint(item.input.text),
        el("audio", { controls: true, preload: "none", src: "/api/audio/" + item.id }),
        box("actions", btn("刪除生成 WAV", async () => {
          if (!window.confirm("永久刪除這段 WAV？合成紀錄會保留。")) return;
          await api("audio/" + item.id, "DELETE");
          historySelection.delete(item.id);
          await refresh("已刪除 WAV，合成紀錄已保留。");
        })),
      )) : [hint("沒有已生成的錄音。")]),
    );
    $("work").replaceChildren(choice,about,box("grid", reference, generated));
  } catch (err) {
    if (mark === renderID) $("work").replaceChildren(hint(err.message));
  }
}
function syncSynthesisControls() {
  const buttons = document.querySelectorAll(".run");
  if (!buttons.length) return;
  const engine = engines[draft.engine], mode = selectedMode(engine, d().mode);
  const busy = ["running", "cancelling"].includes(currentJob?.status);
  const asr = mode.transcript ? speakerASR(draft.engine, d()) : null;
  const waiting = asr?.pending && !asr.text;
  buttons.forEach((button) => { button.disabled = busy || !canRun(engine, mode, draft.engine==="gpt-sovits"?gptVariant(d(),engine):mode.variant) || waiting; });
}
const jobIsland={key:"",dismissed:"",resident:null};
function showJob() {
  syncSynthesisControls();
  cleanStatus();
  datasetControls();
  syncTrainingControls();
  const capturing=!!mic;
  const j=capturing && currentJob?.kind==="sts-live"?null:currentJob;
  const busy=jobActive(j),host=$("job");
  const key=j ? [j.id,j.status,panel(),j.total||0,capturing].join(":") : capturing?"mic":"idle";
  const compact=j?.id===jobIsland.dismissed && !busy;
  if(key!==jobIsland.key) {
    jobIsland.key=key;
    const dot=el("span",{className:"activity-dot","aria-hidden":"true"});
    if(j)dot.dataset.status=j.status;
    const status=el("span",{className:"activity-status",role:"status",id:"job-status"});
    const progress=el("progress",{id:"job-progress","aria-label":"作業進度",hidden:!busy});
    const parts=el("span",{className:"activity-count",id:"job-count"});
    const buttons=[];
    if(busy) buttons.push(btn("取消作業",async()=>{
      currentJob=await api("cancel/"+j.id,"POST");showJob();
    },{disabled:j.status==="cancelling"}));
    if(["synthesis","sts"].includes(j?.kind) && j.status==="done" && panel()!=="history")
      buttons.push(btn("試聽及匯出",()=>setTab("history")));
    if(j?.kind==="clean" && j.status==="done" && !cleanOpen)
      buttons.push(btn("查看清理結果",()=>setTab("clean")));
    if(["dataset","training","auto-training"].includes(j?.kind) && ["done","failed"].includes(j.status) && panel()!=="training")
      buttons.push(btn("查看訓練結果",()=>setTab("training")));
    if(capturing)buttons.push(btn("停止收音",()=>stopMic()));
    else if(j && !busy)buttons.push(btn("收起",()=>{jobIsland.dismissed=j.id;showJob()}));
    host.replaceChildren(dot,status,progress,parts,...buttons);
  }
  host.hidden=!!compact || !j && !jobIsland.resident?.id && !mic;
  const states={running:"進行中",cancelling:"正在取消",cancelled:"已取消",done:"已完成",failed:"失敗"};
  const labels={install:"安裝",synthesis:"語音合成",transcription:"素材轉錄",clean:"音檔清理",
    dataset:"建立訓練資料集",training:"微調訓練","auto-training":"一鍵生成及訓練","tts-warm":"常駐預載",sts:"STS 語音轉換","sts-live":"即時收音","sts-warm":"STS 預熱"};
  const resident=jobIsland.resident;
  const message=[
    j ? [labels[j.kind]||j.kind,states[j.status]||j.status,j.detail,j.warning].filter(Boolean).join(" · ") : "",
    resident?.id ? "常駐："+residentName(resident) : "",
    mic ? "咪高峰收音中" : "",
  ].filter(Boolean).join("　");
  if($("job-status").textContent!==message)$("job-status").textContent=message;
  const progress=$("job-progress");
  if(busy && j.total>0){
    progress.max=j.total;progress.value=j.completed||0;
    $("job-count").textContent=(j.completed||0)+"/"+j.total+" 段";
  }else{
    progress.removeAttribute("value");
    $("job-count").textContent="";
  }
}
async function poll() {
  if(studioExited) return;
  if (studioExitPending) { setTimeout(poll,1200); return; }
  try {
    const [j,resident]=await Promise.all([api("job"),api("tts/resident").catch(()=>null)]);
    if(studioExited) return;
    if(resident)jobIsland.resident=resident;
    const changed=JSON.stringify(j)!==JSON.stringify(currentJob);
    if(changed){
      currentJob=j;
      if(j?.status==="done" && j.kind==="install"){
        engines=(await api("state")).engines;
        if(["models","synth"].includes(panel()))render();
      }
      if(j?.status==="done" && j.kind==="clean" && cleanOpen)loadCleanHistory();
      trainingJobChanged(j).catch(notice);
      if(j?.status==="done" && ["synthesis","sts"].includes(j.kind) && panel()==="history") history(j.id);
      onSTSJob(j);
    }
    showJob();
  }catch(e){if(!studioExitPending && !studioExited) notice("與本機工作室嘅連線中斷，請確認啟動程式仍在執行。")}
  if(!studioExited)setTimeout(poll,1200);
}
let arenaOpen="";
let asrOptions=[{id:"",name:"自動（推薦）",status:"ready",note:"有可用模型時會按語言選擇。"}];
let asrChoice="";
function saveASRChoice(model){asrChoice=model;draft.asrModel=model;persist()}
function avatarIcon(voice){
  return voice.avatar?el("img",{className:"avatar",src:"/api/avatars/"+voice.avatar,alt:""}):el("span",{className:"avatar avatar-initial","aria-hidden":"true"},Array.from(voice.name||"?").slice(0,1).join(""));
}
function asrModelPicker(commit,current=asrChoice,label="辨識模型"){
  const input=el("select"),desc=hint("");
  for(const m of asrOptions)input.append(el("option",{value:m.id,disabled:m.status!=="ready",title:m.note},m.name+(m.status==="ready"?"":m.status==="missing"?"（未安裝）":"（待整合）")));
  input.value=current;
  input.setAttribute("aria-label",label);
  if(input.selectedIndex<0)input.value="";
  const describe=()=>{desc.textContent=asrOptions.find(m=>m.id===input.value)?.note||"";input.title=desc.textContent};
  input.onchange=()=>{describe();commit(input.value)};
  const guide=el("details",{},el("summary",{},"比較模型"),...asrOptions.filter(m=>m.id).map(m=>el("p",{title:m.note},el("strong",{},m.name+"："),m.note)));
  describe();return box("model-choice",input,desc,guide);
}
const sts={model:"cosyvoice3"};
const stsNames={cosyvoice3:"CosyVoice 3","x-vc":"X-VC","seed-vc-tiny":"Seed-VC Tiny",meanvc2:"MeanVC 2",tvtsyn1:"TVTSyn1"};
function stsStatus(message){
  sts.message=message;
  if($("sts-audio-status"))$("sts-audio-status").textContent=message;
}
function drawSTS(){
  const slots=[["source","來源錄音 · 1–25 秒"],["reference","目標音色參考 · 2–20 秒"]];
  if(!sts.models&&!sts.catalogPending){
    sts.catalogPending=true;
    api("sts/models").then(rows=>{sts.models=rows;if(arenaOpen==="sts")drawSTS()})
      .catch(err=>{stsStatus("未能讀取 STS 模型："+err.message)})
      .finally(()=>{sts.catalogPending=false});
  }
  const model=sts.models?.find(x=>x.id===sts.model);
  const ready=model?.ready??(sts.model==="cosyvoice3"&&engines.cosyvoice3?.ready);
  const micReady=model?.mic??sts.model==="cosyvoice3";
  const selection=select(Object.fromEntries((sts.models||Object.entries(stsNames).map(([id,name])=>({id,name})))
    .map(x=>[x.id,x.name+(x.ready===false?" · 未就緒":"")])),sts.model,value=>{
      if(sts.running||sts.loading||sts.warming||mic)return;
      stopSTSPreview();sts.model=value;sts.message="";sts.warmMessage="";sts.job=null;sts.result=null;drawSTS();
    });
  selection.id="sts-model";
  selection.disabled=!!(mic||sts.running||sts.loading||sts.warming);
  const isSeed=sts.model==="seed-vc-tiny";
  const seedMode=()=>field("Seed-VC 錄音模式",select({"whole":"整段轉換","realtime":"原生即時分幀（300 ms）"},
    sts.seedStreaming?"realtime":"whole",value=>{
      sts.seedStreaming=value==="realtime";drawSTS();
    },"sts-seed-mode"));
  const inputs=slots.flatMap(([key,name])=>[
    field(name,el("input",{id:"sts-"+key,type:"file",accept:mediaAccept,disabled:!!(sts.running||sts.loading),onchange:async(e)=>{
      const f=e.target.files[0];if(!f)return;
      sts.loading=true;drawSTS();
      try{const data=await api("reference","POST",f);sts[key]={...data,name:f.name};sts.result=null;sts.job=null;stsStatus("")}
      catch(err){stsStatus(err.message)}finally{sts.loading=false;if(arenaOpen==="sts")drawSTS()}
    }})),
    ...(sts[key]?[hint(sts[key].name+" · "+sts[key].seconds.toFixed(2)+" 秒"),
      el("audio",{controls:true,preload:"none",src:"/api/reference/"+sts[key].id})]:[])
  ]);
  $("work").replaceChildren(box("grid",
    card("匯入兩段錄音",...inputs,hint("不轉字，也不從文字重新朗讀。每次只轉換一段已匯入的來源錄音。")),
    card("轉換與試聽",field("STS 模型",selection),...(isSeed&&micReady&&!mic&&!sts.running?[seedMode()]:[]),
      hint((model?.summary||"")+(ready?(isSeed&&!sts.seedStreaming
        ?" 完成整段轉換後可試聽及下載 WAV。":" 首段音訊可試聽；完成後可下載完整 WAV。")
        :sts.model==="cosyvoice3"?" 請先在「模型管理」連接引擎及模型。":" 此模型未完成安裝及驗證。")),
      box("actions",btn("開始啟動 STS 模型",warmSTSMic,{disabled:!ready||!!mic||!!sts.warming||!!sts.running})),
      btn("停止 STS 模型",unloadSTSMic,{disabled:!!sts.warming||!!sts.running}),
      hint(sts.warmMessage||"手動啟動會預熱並保持常駐；可隨時停止。切換常駐模型前會詢問。"),
      el("h3",{},"TimbreDeck STS"),
      hint(micReady?(isSeed?"Seed-VC 每 300 ms 處理一段，跨段保留上下文。建議先預熱。":"收音與播放可同時進行。")+" 要給其他程式使用，請把輸出選為已安裝的虛擬音訊線。":"此模型只開放錄音轉換；咪高峰尚未驗證。"),
      btn(mic?"停止咪高峰":"啟動咪高峰",mic?()=>stopMic():openMic,{disabled:!mic&&(!ready||!micReady||!sts.reference||!!sts.warming||!!sts.running)}),
      ...(micReady?[stsDevices()]:[]),
      el("p",{id:"sts-mic",role:"status"},sts.micMessage||""),
      box("actions",btn("開始轉換及試聽",startSTSPreview,{className:"primary",disabled:!!(mic||sts.warming||sts.loading||sts.running||!ready||!sts.source||!sts.reference)}),
        ...(sts.audio?[btn("停止試聽",()=>{stopSTSPreview();stsStatus("已停止試聽，轉換作業可以繼續。")})]:[])),
      el("p",{className:"hint",id:"sts-audio-status",role:"status"},sts.message||""),
      ...(sts.job&&sts.result==="done"?[el("audio",{controls:true,preload:"none",src:"/api/audio/"+sts.job}),
        el("a",{className:"linkbtn",href:"/api/audio/"+sts.job+"?download=1"},"下載完整 WAV")]:[]))));
}
function stopSTSPreview(){
  sts.abort?.abort();
  sts.audio?.close().catch(()=>{});
  sts.audio=null;
  sts.abort=null;
}
async function startSTSPreview(){
  if(sts.running)return;
  const replace=await confirmSTSResident();
  if(replace===null)return;
  stopSTSPreview();
  const ctx=new AudioContext(),abort=new AbortController();
  sts.audio=ctx;sts.abort=abort;sts.running=true;sts.result=null;sts.job=null;
  stsStatus("準備模型及錄音，未收到音訊。");
  drawSTS();
  try{
    await ctx.resume();
    const begin=performance.now();
    currentJob=await api("sts","POST",{source:sts.source.id,reference:sts.reference.id,model:sts.model,
      streaming:sts.model==="seed-vc-tiny"&&!!sts.seedStreaming,replace});
    const ack=currentJob,key=sts.job=ack.id;
    showJob();
    waitForJob(ack,{terminal:true}).then(onSTSJob).catch(notice);
    if(abort.signal.aborted)return;
    const response=await fetch("/api/sts/"+key+"/stream",{signal:abort.signal});
    if(!response.ok||response.headers.get("X-Audio-Format")!=="pcm-s16le-mono")throw Error("未收到即時音訊；請查看作業狀態。");
    const rate=Number(response.headers.get("X-Sample-Rate")),reader=response.body.getReader();
    if(rate!==24000)throw Error("不支援的試聽取樣率。");
    let next=0,extra=null,played=false;
    while(true){
      const {done,value}=await reader.read();if(done)break;
      let bytes=value;
      if(extra!==null){bytes=new Uint8Array(value.length+1);bytes[0]=extra;bytes.set(value,1);extra=null}
      if(bytes.length%2)extra=bytes[bytes.length-1];
      const n=bytes.length>>1;if(!n)continue;
      const audio=ctx.createBuffer(1,n,rate),samples=audio.getChannelData(0),dv=new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength);
      for(let i=0;i<n;i++)samples[i]=dv.getInt16(i*2,true)/32768;
      const node=ctx.createBufferSource();node.buffer=audio;node.connect(ctx.destination);
      if(next<ctx.currentTime+.015)next=ctx.currentTime+.04;
      node.start(next);next+=audio.duration;
      if(!played){played=true;stsStatus("開始串流試聽 · 首段 "+((performance.now()-begin)/1000).toFixed(2)+" 秒")}
    }
    if(extra!==null)throw Error("串流資料未完整。");
  } catch(err){
    if(!abort.signal.aborted)stsStatus(err.message);
    if(!sts.job)sts.running=false;
  } finally{
    if(sts.audio===ctx && !sts.job){stopSTSPreview();if(arenaOpen==="sts")drawSTS()}
  }
}

function onSTSJob(j) {
  if(j?.kind!=="sts" || j.id!==sts.job || jobActive(j) || sts.result===j.status)return;
  sts.running=false;sts.result=j.status;
  if(arenaOpen==="sts")drawSTS();
}
