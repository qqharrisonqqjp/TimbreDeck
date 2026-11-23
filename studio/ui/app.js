"use strict";
const $ = (id) => document.getElementById(id);
const catalog = {
  "gpt-sovits": ["GPT-SoVITS", "以參考錄音及逐字稿複製聲線。", "v2Pro"],
  cosyvoice3: [
    "CosyVoice 3",
    "以同一段錄音進行聲音複製、跨語言朗讀或指令朗讀。",
    "Fun-CosyVoice3-0.5B",
  ],
  voxcpm2: ["VoxCPM2", "規劃支援錄音複製及文字聲線設計。", "VoxCPM2"],
  indextts2: ["IndexTTS-2", "規劃支援獨立的角色音色與情緒示範。", "IndexTTS-2"],
  "qwen3-tts": [
    "Qwen3-TTS",
    "規劃支援錄音複製、預設聲線及文字聲線設計。",
    "Base · CustomVoice · VoiceDesign",
  ],
};
const integrated = (k) => ["gpt-sovits", "cosyvoice3"].includes(k);
const lang = {
  yue: "粵語（可夾英語）",
  zh: "普通話",
  en: "英語",
  ja: "日語",
  ko: "韓語",
  auto: "自動辨識",
};
let draft,
  engines,
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
      onclick: () => Promise.resolve(fn()).catch(notice),
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
function panel() {
  const v = d().tab;
  return ["models", "history"].includes(v)
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
      : "未安裝引擎";
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
    history: "輸出歷史",
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
  else history();
  showJob();
}
function synthesis() {
  const k = draft.engine,
    settings = d(),
    gpt = k === "gpt-sovits";
  const mode = gpt ? "clone" : settings.mode || "clone";
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
  const modes = gpt
    ? { clone: "錄音複製" }
    : { clone: "錄音複製", instruct: "指令朗讀", cross: "跨語言朗讀" };
  const modeCtrl = select(
    modes,
    mode,
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
        draft.reference = res.id;
        draft.referenceName = file.name;
        persist();
        render();
      } catch (err) {
        notice(err);
      }
    },
  });
  const right = [
    box("row", field("工作模式", modeCtrl), field("語速", speed)),
    field("參考錄音（WAV）", el("span", { className: "filebtn" }, ref)),
  ];
  right.push(
    hint(gpt ? "使用 3 至 10 秒的單人錄音。" : "選用清晰的單人錄音。"),
  );
  if (draft.reference)
    right.push(
      el("p", { className: "file-name" }, draft.referenceName || "已匯入錄音"),
      el("audio", {
        controls: true,
        preload: "metadata",
        src: "/api/reference/" + draft.reference,
      }),
    );
  if (mode === "clone")
    right.push(
      field(
        "參考錄音原本說了甚麼？",
        area(
          "transcript",
          settings.transcript || "",
          "填寫錄音中的實際對白",
          (v) => update("transcript", v),
        ),
      ),
    );
  if (mode === "instruct")
    right.push(
      field(
        "講法指示",
        area(
          "instruction",
          settings.instruction || "",
          "例如：請用廣東話，節奏稍慢，像跟朋友聊天。",
          (v) => update("instruction", v),
        ),
      ),
    );
  if (gpt)
    right.push(
      box(
        "row",
        field(
          "台詞語言",
          select(
            lang,
            settings.language || "yue",
            (v) => update("language", v),
            "language",
          ),
        ),
        field(
          "錄音語言",
          select(
            lang,
            settings.refLang || "yue",
            (v) => update("refLang", v),
            "refLang",
          ),
        ),
      ),
    );
  const controls = box(
    "actions",
    btn("試讀第一句", () => submit(true), { className: "run" }),
    btn("生成全文", () => submit(false), { className: "primary run" }),
  );
  const left = card("朗讀台詞", field("要讀出的文字", script), controls);
  if (!engines[k].ready)
    left.append(
      box(
        "actions",
        hint("合成前請先連接引擎。"),
        btn("前往模型管理", () => setTab("models")),
      ),
    );
  $("work").replaceChildren(box("grid", left, card(catalog[k][2], ...right)));
}
async function submit(preview) {
  const k = draft.engine,
    s = d();
  const payload = {
    engine: k,
    mode: k === "gpt-sovits" ? "clone" : s.mode || "clone",
    text: draft.text || "",
    transcript: s.transcript || "",
    instruction: s.instruction || "",
    reference: draft.reference || "",
    language: s.language || "yue",
    refLang: s.refLang || "yue",
    speed: s.speed || 1,
    preview,
  };
  persist();
  currentJob = await api("synthesize", "POST", payload);
  showJob();
}
function models() {
  const k = draft.engine;
  const root = engines[k].path;
  const about = card("模型", el("h3", {}, catalog[k][2]));
  if (!integrated(k)) {
    about.append(hint("本預覽版尚未接通這款引擎。"));
    $("work").replaceChildren(about);
    return;
  }
  const path = el("input", { type: "text", id: "path", placeholder: root });
  const info = el("div", { className: "stack" });
  async function lookup() {
    info.textContent = "正在查詢版本…";
    try {
      const p = await api("pack/" + k);
      const gb = (n) => (n / 1e9).toFixed(2) + " GB";
      info.replaceChildren(
        hint(
          p.version +
            "　下載 " +
            gb(p.bytes) +
            "　安裝後 " +
            gb(p.expanded) +
            "　可用空間 " +
            gb(p.free),
        ),
        btn(
          "下載並安裝",
          async () => {
            currentJob = await api("install/" + k, "POST", { hash: p.hash });
            showJob();
          },
          { className: "primary" },
        ),
      );
    } catch (e) {
      info.textContent = e.message;
    }
  }
  about.append(
    hint("從本專案的 GitHub 發佈頁查詢引擎包。"),
    box("actions", btn("檢查可下載版本", lookup)),
    info,
  );
  const folder = card(
    "連接現有引擎",
    hint(
      "輸入含有 runtime 與引擎程式的完整資料夾路徑。放在預設位置的引擎可直接重新偵測。",
    ),
    hint("目前檢查的位置"),
    el("p", { className: "path" }, root),
    field("引擎資料夾", path),
    box(
      "actions",
      btn("驗證並連接", async () => {
        if (!path.value.trim()) throw Error("請輸入引擎資料夾路徑。");
        engines[k] = await api("engine", "POST", {
          engine: k,
          path: path.value.trim(),
        });
        render();
        notice("已找到必要檔案，可前往語音合成。");
      }),
      btn("重新偵測", async () => {
        engines = (await api("state")).engines;
        render();
        notice(
          engines[k].ready
            ? "已找到必要檔案。"
            : "缺少：" + (engines[k].missing || []).join("、"),
        );
      }),
    ),
  );
  if (engines[k].ready)
    folder.append(
      box(
        "actions",
        btn("前往語音合成", () => setTab("synth"), { className: "primary" }),
      ),
    );
  $("work").replaceChildren(box("grid", folder, about));
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
            " 秒",
        ),
        el("p", { className: "history-text" }, x.input.text),
        box(
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
        ),
      ),
    );
    $("work").replaceChildren(
      ...(content.length
        ? content
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
    b.disabled = !!busy || !engines[draft.engine]?.ready;
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
      (j.kind === "install" ? "安裝" : "語音合成") +
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
    const state = await api("state");
    draft = state.draft;
    engines = state.engines;
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
