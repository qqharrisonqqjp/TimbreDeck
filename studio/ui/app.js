"use strict";
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
const cleanWorkspace = ["去去除雜聲", "批量清理本機 WAV，按指定次序合併及匯出。"];
const workspaces = { ...catalog, clean: cleanWorkspace };
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
  voices = [],
  editingVoice = null,
  trainingProject = null,
  trainedModels = [],
  historySelection = new Set(),
  currentJob,
  timer,
  writes = Promise.resolve(),
  revision = 0,
  renderID = 0;
function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, val] of Object.entries(props)) {
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
async function api(path, method = "GET", data) {
  const blob = data instanceof Blob;
  const opts = { method, headers: { "X-TimbreDeck": "1" } };
  if (data !== undefined) {
    opts.headers["Content-Type"] = blob
      ? "application/octet-stream"
      : "application/json";
    opts.body = blob ? data : JSON.stringify(data);
  }
  const res = await fetch("/api/" + path, opts);
  const body = await res.json();
  if (!res.ok) throw Error(body.error || "HTTP " + res.status);
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
function canRun(engine, mode) {
  return engine.ready && (!mode.variant || engine.availableVariants?.includes(mode.variant));
}
function panel() {
  if (cleanOpen) return "clean";
  const v = d().tab;
  if (v === "training" && draft.engine === "gpt-sovits") return v;
  return ["models", "history", "voices", "storage"].includes(v)
    ? v
    : integrated(draft.engine)
      ? "synth"
      : "models";
}
function persist() {
  clearTimeout(timer);
  const rev = ++revision;
  const copy = structuredClone(draft);
  writes = writes
    .catch(() => {})
    .then(() => api("draft", "PUT", copy))
    .then(() => {
      if (rev === revision) $("saved").textContent = "草稿已儲存至本機";
    })
    .catch((e) => {
      notice(e);
      $("saved").textContent = "草稿儲存失敗";
    });
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
  if (k === "clean") {
    cleanOpen = true;
  } else {
    cleanOpen = false;
    if (draft.engine !== k) {
      draft.engine = k;
      persist();
    }
  }
  render();
}
function setTab(k) {
  cleanOpen = false;
  d().tab = k;
  persist();
  render();
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
  return selectedVoice()?.modelProject || "";
}
function modelOptions(base, selected) {
  const models = { "": base, ...Object.fromEntries(trainedModels.map((model) => [model.id, model.name])) };
  if (selected && !models[selected]) models[selected] = "找不到已選模型";
  return models;
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
  renderID++;
  const k = cleanOpen ? "clean" : draft.engine;
  document.body.dataset.engine = k;
  $("title").textContent = workspaces[k][0];
  $("subtitle").textContent = workspaces[k][1];
  $("state").textContent = cleanOpen ? "本機工具" : !integrated(k)
    ? "待接通"
    : engines[k].ready
      ? "已找到引擎檔案"
      : engines[k].engineInstalled ? "未安裝模型" : "未安裝引擎";
  $("notice").textContent = "";
  $("switcher").replaceChildren(
    ...Object.keys(workspaces).map((id) =>
      btn(workspaces[id][0], () => setEngine(id), {
        "aria-pressed": String(id === k),
      }),
    ),
  );
  $("compact").value = k;
  if (cleanOpen) {
    $("tabs").hidden = true;
    $("tabs").replaceChildren();
    cleaning();
    showJob();
    return;
  }
  $("tabs").hidden = false;
  const tabs = {
    ...(integrated(k) ? { synth: "語音合成" } : {}),
    models: "模型管理",
    voices: "聲音庫",
    ...(k === "gpt-sovits" ? { training: "訓練素材" } : {}),
    history: "輸出歷史",
    storage: "設定",
  };
  $("tabs").replaceChildren(
    ...Object.entries(tabs).map(([id, label]) =>
      btn(label, () => setTab(id), {
        "aria-current": panel() === id ? "page" : "false",
      }),
    ),
  );
  if (panel() === "synth") synthesis();
  else if (panel() === "models") models();
  else if (panel() === "voices") voiceLibrary();
  else if (panel() === "training") training();
  else if (panel() === "storage") storageSettings();
  else history();
  showJob();
}
const cleanModes = {
  denoise: "基礎去噪",
  uvr: "UVR 人聲分離",
  both: "先用 UVR5 分離人聲，再做語音降噪",
};
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
    ? "部分舊錄音未能列出；仍可匯入 WAV，或使用目前可見的錄音。" : "";
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
    id: "clean-upload", type: "file", accept: ".wav,audio/wav", multiple: true,
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
  const mode = select(cleanModes, cleanMode, (v) => { cleanMode = v; }, "clean-mode");
  const gap = el("input", {
    type: "number", id: "clean-gap", min: "0", max: String(cleanMaxGap), step: "1", value: cleanGap,
    "aria-describedby": "clean-length",
    oninput: (e) => { cleanGap = e.target.value; cleanStatus(); },
  });
  $("work").replaceChildren(
    box("grid",
      card("加入錄音",
        field("匯入 WAV（可選多個）", upload),
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
        field("清理模式", mode),
        field("段與段之間的間隔（毫秒）", gap),
        hint("最多 24 段；合併後連間隔最長 180 秒。"),
        el("p", { id: "clean-length", className: "hint", role: "status" }),
        btn("開始清理", startClean, { id: "clean-run", className: "primary" }),
      ),
    ),
    card("清理次序",
      el("p", { id: "clean-queue-empty", className: "hint" }, "先匯入 WAV 或加入已存錄音。順序會用於合併。"),
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
    const result = await api("asr", "POST", { reference, language });
    if (ticket !== ctx.ticket) return;
    const text = typeof result.text === "string" ? result.text.trim().slice(0, 10000) : "";
    if (!text) throw Error("未辨識出清楚對白。");
    const label = [typeof result.provider === "string" && result.provider,
      typeof result.language === "string" && (lang[result.language] || result.language)].filter(Boolean).join(" · ");
    if (ctx.manual) {
      ctx.suggestion = text === ctx.text ? null : { text };
      ctx.message = "已保留手動編輯" + (ctx.suggestion ? "；可另行採用辨識結果" : "") + (label ? "（" + label + "）" : "") + "。";
    } else {
      ctx.text = text;
      ctx.commit?.(ctx);
      ctx.message = "逐字稿已自動填入，可先校對" + (label ? "（" + label + "）" : "") + "。";
    }
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

const datasetMax = 24, datasetSeconds = 180;
const datasetLanguages = Object.fromEntries(Object.entries(referenceLanguages).filter(([key]) => key !== "auto"));
const datasetState = { clips: [], language: "yue", speaker: "", denoise: false, importing: false, submitting: false };
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
async function submitDataset() {
  const button = $("dataset-run");
  if (!button || button.disabled) return;
  datasetState.submitting = true;
  drawDatasetFiles();
  try {
    currentJob = await api("datasets/auto", "POST", {
      references: datasetState.clips.map(({ id }) => id),
      language: datasetState.language,
      speaker: datasetState.speaker.trim() || "speaker-1",
      denoise: datasetState.denoise,
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
      hint(item.speaker + " · " + item.count + " 段"),
      el("a", { className: "linkbtn", href: "/api/datasets/auto/" + item.id + "/download", download: "" }, "下載資料集"),
    )) : [hint("完成的資料集會在這裏顯示。")]));
  } catch (e) {
    if (live()) element.replaceChildren(hint("未能讀取資料集：" + e.message));
  }
}
function autoDatasetPanel() {
  const checkbox = el("input", {
    type: "checkbox", id: "dataset-denoise", checked: datasetState.denoise,
    onchange: (e) => { datasetState.denoise = e.target.checked; },
  });
  const section = card("一鍵建立訓練資料集",
    hint("只會使用你在此明確選取的 WAV，不會加入目前參考錄音、聲音庫或舊作業。建立後仍需查閱與校對，不會自動開始訓練。"),
    field("選取訓練 WAV（可選多個）", el("input", {
      type: "file", accept: ".wav,audio/wav", multiple: true, id: "dataset-files-input",
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
    el("label", { className: "dataset-option" }, checkbox, "切片前先做基礎去噪"),
    el("p", { id: "dataset-status", className: "asr-status hint", role: "status" }),
    btn("建立資料集", submitDataset, { id: "dataset-run", className: "primary", disabled: true }),
    el("h3", {}, "已建立的資料集"),
    el("div", { id: "dataset-history", "aria-live": "polite" }, hint("正在讀取資料集…")),
  );
  section.classList.add("dataset-auto");
  return section;
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
    accept: ".wav,audio/wav",
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
  const right = [box("row", field("工作模式", modeCtrl), ...(engine.speed ? [field("語速", speed)] : []))];
  if (k === "gpt-sovits") {
    const selected = selectedTrainedModel(settings);
    right.push(
      field("GPT-SoVITS 模型", select(modelOptions("v2Pro 基礎模型", selected), selected, (id) => {
        settings.modelVoice = draft.voiceId || "";
        update("modelProject", id);
        render();
      }, "trained-model")),
      btn("重新偵測微調模型", async () => {
        trainedModels = await api("trained-models");
        render();
      }),
    );
  }
  if (mode.reference) {
    if (voices.length)
      right.push(field("角色", select(
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
    right.push(field("參考錄音（WAV）", el("span", { className: "filebtn" }, ref)));
    if (engine.minReference)
      right.push(hint(`使用 ${engine.minReference} 至 ${engine.maxReference} 秒的單人錄音。`));
    else if (engine.maxReference)
      right.push(hint(`使用最多 ${engine.maxReference} 秒的清晰單人錄音。`));
    if (draft.reference)
      right.push(
        el("p", { className: "file-name" }, draft.referenceName || "已匯入錄音"),
        el("audio", {
          controls: true,
          preload: "metadata",
          src: "/api/reference/" + draft.reference,
        }),
      );
  }
  if (mode.reference)
    right.push(asrPanel("reference-asr", speakerASR(k, settings), {
      textID: "transcript", languageID: "refLang",
      textLabel: "參考逐字稿",
      hint: mode.transcript ? "" : "此模式只用參考音訊；逐字稿只供查看與校對。",
    }));
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
  const left = card("朗讀台詞", field("要讀出的文字", script), controls);
  if (!canRun(engine, mode))
    left.append(
      box(
        "actions",
        hint(mode.variant ? "合成前請先安裝 " + mode.variant + " 模型。" : "合成前請先連接引擎。"),
        btn("前往模型管理", () => setTab("models")),
      ),
    );
  $("work").replaceChildren(box("grid", left, card(mode.variant || engine.variants.join(" · "), ...right)));
}
function emotionFields(kind, settings, update) {
  const fields = [];
  if (kind === "audio") {
    const input = el("input", {
      type: "file", accept: ".wav,audio/wav", id: "emotion-reference",
      onchange: async (event) => {
        const file = event.target.files[0];
        if (!file) return;
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
    fields.push(field("情緒參考錄音（WAV，最多 15 秒）", input));
    if (settings.emotionReference) fields.push(
      hint(settings.emotionName || "已匯入情緒錄音"),
      el("audio", { controls: true, preload: "none", src: "/api/reference/" + settings.emotionReference }),
    );
    fields.push(asrPanel("emotion-asr", emotionASR(settings), {
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
  if (mode.variant) payload.variant = mode.variant;
  if (k === "gpt-sovits") payload.modelProject = selectedTrainedModel(s);
  if (mode.speaker) payload.speaker = s.speaker || engine.speakers[0];
  if (mode.emotion) payload.emotionStrength = s.emotionStrength ?? 1;
  if (mode.emotion === "audio") payload.emotionReference = s.emotionReference || "";
  if (mode.emotion === "vector") payload.emotionWeights = s.emotionWeights || Array(8).fill(0);
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
      el("h3", {}, voice.name),
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
    type: "file", accept: ".wav,audio/wav",
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
    field("參考錄音（WAV）", el("span", { className: "filebtn" }, upload)),
    ...(voice.reference ? [el("audio", {
      controls: true, preload: "metadata", src: "/api/reference/" + voice.reference,
    })] : []),
    asrPanel("voice", profileASR(voice)),
    field("GPT-SoVITS 模型", select(
      modelOptions("v2Pro 基礎模型", voice.modelProject), voice.modelProject || "",
      (id) => { voice.modelProject = id; }, "voice-trained-model",
    )),
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
async function training() {
  const mark = renderID;
  const manual = box("training-manual", hint("正在讀取訓練專案…"));
  $("work").replaceChildren(autoDatasetPanel(), manual);
  drawDatasetFiles();
  loadDatasetHistory(mark);
  try {
    const projects = await api("training");
    if (mark !== renderID) return;
    const projectsCard = card(
      "訓練專案",
      ...projects.map((project) => btn(project.name, () => {
        trainingProject = structuredClone(project);
        render();
      }, { "aria-pressed": String(project.id === trainingProject?.id) })),
      btn("新增專案", () => {
        trainingProject = { name: "", clips: [] };
        render();
      }, { className: "primary" }),
    );
    if (!trainingProject) {
      manual.replaceChildren(projectsCard);
      return;
    }
    const project = trainingProject;
    const asrModel = el("input", {
      type: "text", placeholder: "使用 GPT-SoVITS 內的 faster-whisper-large-v3",
    });
    const editor = card(
      "片段與逐字稿",
      field("專案名稱", el("input", {
        type: "text", value: project.name, maxLength: 80,
        oninput: (event) => { project.name = event.target.value; },
      })),
      field("加入 PCM16 WAV", el("input", {
        type: "file", accept: ".wav,audio/wav", multiple: true,
        onchange: async (event) => {
          try {
            for (const file of event.target.files) {
              const ref = await api("reference", "POST", file);
              project.clips.push({
                reference: ref.id, sourceName: file.name, startMs: 0, endMs: Math.floor(ref.seconds * 1000),
                language: "yue", transcript: "", use: "pending",
              });
            }
          } catch (err) {
            notice(err);
          }
          if (trainingProject === project && panel() === "training") training();
        },
      })),
      ...project.clips.map((clip, index) => trainingClipFields(project, clip, index)),
      box("actions",
        btn("儲存專案", async () => {
          const saved = await api(project.id ? "training/" + project.id : "training",
            project.id ? "PUT" : "POST", project);
          if (trainingProject !== project || panel() !== "training") return;
          trainingProject = saved;
          await training();
          notice("訓練專案已儲存。");
        }, { className: "primary" }),
        ...(project.id && project.clips.some((clip) => clip.use === "train") ? [
          el("a", { className: "linkbtn", href: "/api/training/" + project.id + "/dataset", download: "" },
            "匯出已儲存的資料集"),
        ] : []),
      ),
    );
    if (project.id && project.clips.some((clip) => clip.use === "pending" && !clip.transcript)) {
      editor.append(
        field("本機轉錄模型（可選）", asrModel),
        btn("轉錄已儲存的空白片段", async () => {
          currentJob = await api("training/" + project.id + "/transcribe", "POST", {
            revision: project.revision,
            modelPath: asrModel.value.trim(),
          });
          showJob();
        }),
      );
    }
    manual.replaceChildren(box("grid", projectsCard, editor));
  } catch (err) {
    if (mark === renderID) notice(err);
  }
}
function trainingClipFields(project, clip, index) {
  const millis = (key) => el("input", {
    type: "number", step: 1, min: 0, value: clip[key],
    oninput: (event) => { clip[key] = event.target.valueAsNumber; },
  });
  const options = { ...referenceLanguages };
  delete options.auto;
  return card(
    clip.sourceName || "參考錄音",
    box("row", field("開始毫秒", millis("startMs")), field("結束毫秒", millis("endMs"))),
    box("row",
      field("語言", select(options, clip.language, (value) => { clip.language = value; }, "clip-lang-" + index)),
      field("用途", select(
        { pending: "待校對", train: "採用作訓練", holdout: "保留作試聽", skip: "剔除" },
        clip.use, (value) => { clip.use = value; }, "clip-use-" + index,
      )),
    ),
    field("逐字稿", area("clip-text-" + index, clip.transcript, "請輸入該片段的實際對白",
      (value) => { clip.transcript = value; })),
    ...(project.id && clip.id ? [
      hint("上次儲存的片段"),
      el("audio", {
        controls: true, preload: "none",
        src: "/api/training/" + project.id + "/clip/" + clip.id + "?revision=" + project.revision,
      }),
    ] : []),
    box("actions",
      ...(clip.use === "pending" && !clip.transcript ? [
        btn("按停頓自動切片", async () => {
          const split = await api("training/split", "POST", clip);
          if (trainingProject !== project) return;
          const position = project.clips.indexOf(clip);
          if (position < 0) return;
          if (project.clips.length - 1 + split.length > 300) throw Error("每個專案最多 300 個片段。");
          project.clips.splice(position, 1, ...split);
          if (panel() === "training") render();
        }),
      ] : []),
      btn("複製片段範圍", () => {
        const copy = { ...clip, transcript: "", use: "pending" };
        delete copy.id;
        project.clips.splice(index + 1, 0, copy);
        render();
      }),
      btn("移除片段", () => { project.clips.splice(index, 1); render(); }),
    ),
  );
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
      el("h3", {}, variant),
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
              if (!window.confirm("刪除 " + variant + " 模型權重？錄音及生成紀錄會保留。")) return;
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
    trained.push(card(
      "微調模型",
      btn("訓練角色模型", () => setTab("training")),
      ...(trainedModels.length ? trainedModels.map((model) => box("stack", hint(model.name),
        box("actions", btn("刪除微調模型", async () => {
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
  $("work").replaceChildren(hint("正在讀取錄音…"));
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
    $("work").replaceChildren(box("grid", reference, generated));
  } catch (err) {
    if (mark === renderID) $("work").replaceChildren(hint(err.message));
  }
}
async function history() {
  const mark = renderID;
  $("work").replaceChildren(hint("正在讀取輸出…"));
  try {
    const files = await api("history");
    if (mark !== renderID) return;
    const content = files.map((x) =>
      card(
        catalog[x.input.engine][0] + (x.input.preview ? " · 試讀" : " · 全文"),
        el(
          "p",
          { className: "meta" },
          new Date(x.created).toLocaleString("zh-HK") +
            "　" +
            x.seconds.toFixed(1) +
            " 秒" + (x.sampleRate ? "　" + x.sampleRate + " Hz" : ""),
        ),
        hint([x.voiceName, x.model || x.input.variant, x.packVersion].filter(Boolean).join(" · ") || "預覽版記錄"),
        hint(x.verifiedCuda ? "已核對波形及主模型：" + x.verifiedCuda : "未有完整 CUDA 作業證據"),
        el("p", { className: "history-text" }, x.input.text),
        el("details", {},
          el("summary", {}, "合成設定"),
          hint([
            engines[x.input.engine]?.modes?.find((mode) => mode.id === x.input.mode)?.label,
            lang[x.input.language],
            x.input.speaker,
            x.input.speed && x.input.speed + " ×",
            x.input.emotionStrength != null && "情緒強度 " + x.input.emotionStrength,
          ].filter(Boolean).join(" · ")),
          ...(x.input.transcript ? [hint("逐字稿　" + x.input.transcript)] : []),
          ...(x.input.instruction ? [hint("指示　" + x.input.instruction)] : []),
        ),
        x.audioAvailable ? box(
          "history-actions",
          el("audio", {
            controls: true,
            preload: "none",
            src: "/api/audio/" + x.id,
          }),
          el(
            "a",
            {
              className: "linkbtn",
              href: "/api/audio/" + x.id + "?download=1",
              download: "",
            },
            "匯出 WAV",
          ),
          btn(historySelection.has(x.id) ? "取消比較" : "加入比較", () => {
            if (historySelection.has(x.id)) historySelection.delete(x.id);
            else {
              if (historySelection.size >= 2) throw Error("請先取消其中一筆比較。");
              historySelection.add(x.id);
            }
            history();
          }),
          btn("存為角色錄音", async () => {
            const sample = await fetch("/api/audio/" + x.id);
            if (!sample.ok) throw Error("找不到這段輸出音檔。");
            const ref = await api("reference", "POST", await sample.blob());
            editingVoice = {
              name: x.voiceName || "",
              reference: ref.id,
              language: referenceLanguages[x.input.language] ? x.input.language : "auto",
              transcript: x.input.text,
            };
            setTab("voices");
          }),
        ) : hint("錄音已刪除"),
      ),
    );
    $("work").replaceChildren(
      ...(historySelection.size ? [
        box("grid", ...content.filter((_, i) => historySelection.has(files[i].id))),
      ] : []),
      ...(content.length
        ? content.filter((_, i) => !historySelection.has(files[i].id))
        : [card("輸出歷史", hint("完成合成後，可在這裏試聽和匯出音檔。"))]),
    );
  } catch (e) {
    if (mark === renderID) notice(e);
  }
}
function syncSynthesisControls() {
  const buttons = document.querySelectorAll(".run");
  if (!buttons.length) return;
  const engine = engines[draft.engine], mode = selectedMode(engine, d().mode);
  const busy = ["running", "cancelling"].includes(currentJob?.status);
  const asr = mode.transcript ? speakerASR(draft.engine, d()) : null;
  const waiting = asr?.pending && !asr.text;
  buttons.forEach((button) => { button.disabled = busy || !canRun(engine, mode) || waiting; });
}
function showJob() {
  const j = currentJob,
    busy = j && ["running", "cancelling"].includes(j.status);
  syncSynthesisControls();
  cleanStatus();
  datasetControls();
  const jobPanel = $("job");
  if (!j) {
    jobPanel.replaceChildren();
    return;
  }
  const states = {
    running: "進行中",
    cancelling: "正在取消",
    cancelled: "已取消",
    done: "已完成",
    failed: "失敗",
  };
  const nodes = [
    (j.kind === "clean" ? cleanWorkspace[0] : catalog[j.engine]?.[0] || "工作室") +
      " · " +
      ({ install: "安裝", synthesis: "語音合成", transcription: "素材轉錄", clean: "音檔清理", dataset: "建立訓練資料集" }[j.kind] || j.kind) +
      " · " +
      states[j.status],
  ];
  if (j.detail && j.status !== "cancelled") nodes.push("　" + j.detail);
  if (busy)
    nodes.push(
      btn(
        "取消作業",
        async () => {
          currentJob = await api("cancel/" + j.id, "POST");
          showJob();
        },
        { disabled: j.status === "cancelling" },
      ),
    );
  if (j.kind === "synthesis" && j.status === "done" && d().tab !== "history")
    nodes.push(btn("試聽及匯出", () => setTab("history")));
  if (j.kind === "clean" && j.status === "done" && !cleanOpen)
    nodes.push(btn("查看清理結果", () => setEngine("clean")));
  if (j.kind === "dataset" && j.status === "done" && panel() !== "training")
    nodes.push(btn("查看資料集", () => {
      draft.engine = "gpt-sovits";
      setTab("training");
    }));
  if (j.kind === "transcription" && j.status === "done" && !cleanOpen && d().tab === "training")
    nodes.push("　重新選取專案以查看建議逐字稿。");
  jobPanel.replaceChildren(...nodes);
}
async function poll() {
  try {
    const j = await api("job");
    if (JSON.stringify(j) !== JSON.stringify(currentJob)) {
      currentJob = j;
      if (j?.status === "done" && j.kind === "install") {
        engines = (await api("state")).engines;
        render();
      } else {
        showJob();
        if (j?.status === "done" && j.kind === "clean" && cleanOpen) loadCleanHistory();
        if (j?.status === "done" && j.kind === "dataset" && panel() === "training") loadDatasetHistory();
        if (j?.status === "done" && panel() === "history") history();
      }
    }
  } catch (e) {
    notice("與本機啟動器的連線中斷，請確認 Start.exe 仍在執行。");
  }
  setTimeout(poll, 1200);
}
(async () => {
  try {
    const [state, profiles, models] = await Promise.all([api("state"), api("profiles"), api("trained-models")]);
    draft = state.draft;
    engines = state.engines;
    voices = profiles;
    trainedModels = models;
    if (draft.voiceId && !selectedVoice()) delete draft.voiceId;
    draft.drafts ||= {};
    if (!catalog[draft.engine]) draft.engine = "gpt-sovits";
    for (const [k, v] of Object.entries(workspaces))
      $("compact").add(new Option(v[0], k));
    $("compact").onchange = (e) => setEngine(e.target.value);
    document.addEventListener("visibilitychange", () => {
      if (document.hidden && $("saved").textContent === "正在儲存草稿…")
        persist();
    });
    render();
    poll();
  } catch (e) {
    notice(e);
  }
})();
