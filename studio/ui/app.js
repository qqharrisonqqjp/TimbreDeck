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
  draft.engine = k;
  persist();
  render();
}
function setTab(k) {
  d().tab = k;
  persist();
  render();
}
function selectedVoice() {
  return voices.find((voice) => voice.id === draft.voiceId);
}
function useVoice(voice) {
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
  const k = draft.engine;
  document.body.dataset.engine = k;
  $("title").textContent = catalog[k][0];
  $("subtitle").textContent = catalog[k][1];
  $("state").textContent = !integrated(k)
    ? "待接通"
    : engines[k].ready
      ? "已找到引擎檔案"
      : engines[k].engineInstalled ? "未安裝模型" : "未安裝引擎";
  $("notice").textContent = "";
  $("switcher").replaceChildren(
    ...Object.keys(catalog).map((id) =>
      btn(catalog[id][0], () => setEngine(id), {
        "aria-pressed": String(id === k),
      }),
    ),
  );
  $("compact").value = k;
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
      try {
        const res = await api("reference", "POST", file);
        replaceReference(res.id, file.name);
        persist();
        render();
      } catch (err) {
        notice(err);
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
          if (voice) useVoice(voice);
          else replaceReference("", "");
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
  if (mode.transcript)
    right.push(
      field(
        "參考錄音原本說了甚麼？",
        area(
          "transcript",
          referenceTranscript(settings) || "",
          "填寫錄音中的實際對白",
          (v) => {
            settings.transcriptFor = draft.reference;
            settings.transcriptVoice = draft.voiceId || "";
            update("transcript", v);
          },
        ),
      ),
    );
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
        ...(k === "gpt-sovits" ? [field(
          "錄音語言",
          select(
            referenceLanguages,
            (settings.refLangFor === draft.reference ? settings.refLang : selectedVoice()?.language) ||
              settings.refLang || "yue",
            (v) => {
              settings.refLangFor = draft.reference;
              update("refLang", v);
            },
            "refLang",
          ),
        )] : []),
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
        try {
          const ref = await api("reference", "POST", file);
          update("emotionReference", ref.id);
          update("emotionName", file.name);
          render();
        } catch (err) { notice(err); }
      },
    });
    fields.push(field("情緒參考錄音（WAV，最多 15 秒）", input));
    if (settings.emotionReference) fields.push(
      hint(settings.emotionName || "已匯入情緒錄音"),
      el("audio", { controls: true, preload: "none", src: "/api/reference/" + settings.emotionReference }),
    );
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
    transcript: mode.transcript ? referenceTranscript(s) || "" : "",
    instruction: mode.instruction || mode.style ? s.instructions?.[mode.id] ?? s.instruction ?? "" : "",
    reference: mode.reference ? draft.reference || "" : "",
    voiceId: mode.reference ? draft.voiceId || "" : "",
    language: engine.languageOptions?.includes(s.language) ? s.language : engine.languageOptions?.[0] || "yue",
    refLang: (s.refLangFor === draft.reference ? s.refLang : selectedVoice()?.language) ||
      s.refLang || "yue",
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
        transcript: referenceTranscript(d()) || "",
        language: selectedVoice()?.language || d().refLang || "yue",
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
      try {
        const ref = await api("reference", "POST", file);
        if (ref.id !== voice.reference) voice.transcript = "";
        voice.reference = ref.id;
        if (editingVoice === voice && panel() === "voices") render();
      } catch (err) { notice(err); }
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
    field("錄音語言", select(referenceLanguages, voice.language, (value) => { voice.language = value; }, "voice-language")),
    field("參考逐字稿", area("voice-transcript", voice.transcript, "填寫錄音中的實際對白",
      (value) => { voice.transcript = value; })),
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
  $("work").replaceChildren(hint("正在讀取訓練專案…"));
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
      $("work").replaceChildren(projectsCard);
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
    $("work").replaceChildren(box("grid", projectsCard, editor));
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
function showJob() {
  const j = currentJob,
    busy = j && ["running", "cancelling"].includes(j.status);
  document.querySelectorAll(".run").forEach((b) => {
    const engine = engines[draft.engine];
    b.disabled = !!busy || !canRun(engine, selectedMode(engine, d().mode));
  });
  const panel = $("job");
  if (!j) {
    panel.replaceChildren();
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
    catalog[j.engine][0] +
      " · " +
      ({ install: "安裝", synthesis: "語音合成", transcription: "素材轉錄" }[j.kind] || j.kind) +
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
  if (j.kind === "transcription" && j.status === "done" && panel() === "training")
    nodes.push("　重新選取專案以查看建議逐字稿。");
  panel.replaceChildren(...nodes);
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
    for (const [k, v] of Object.entries(catalog))
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
