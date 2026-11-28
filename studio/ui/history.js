"use strict";
const historyState = {kind:"all",engine:"",ticket:0,query:"",root:null,rows:new Map(),nodes:new Map()};

function engineName(key) { return (Object.hasOwn(catalog,key) && catalog[key]?.[0]) || (Object.hasOwn(stsNames,key) && stsNames[key]) || key || "未知引擎"; }
function historySTS(record) {
  return !!record.input?.sourceSpeech || record.input?.mode === "voice-convert" ||
    (Object.hasOwn(stsNames,record.input?.engine) && record.input.engine!=="cosyvoice3");
}
function displaySeconds(value, decimals=2) {
  return Number.isFinite(value) && value >= 0 ? value.toFixed(decimals)+" 秒" : "未記錄";
}
function outputDetails(record) {
  const i = record.input || {};
  const t = record.timing || {};
  const mode = engines[i.engine]?.modes?.find(m=>m.id===i.mode)?.label ||
    (i.mode==="voice-convert"?"聲音轉換":i.mode) || "未記錄";
  const labels = [
    ["冷啟動", t.resident ? "不適用（模型已常駐）" : displaySeconds(t.coldStartSeconds)],
    ["生成時間",displaySeconds(t.generationSeconds)],
    ["總耗時",displaySeconds(t.totalSeconds)],
    ["模型",variantName(record.model || i.variant) || "未記錄"],
    ["微調模型",record.fineTuneName || (i.modelProject ? "舊紀錄 · "+i.modelProject.slice(0,8) : "")],
    ["版本",[record.packVersion && "模型 "+record.packVersion,record.runtimeVersion && "引擎 "+record.runtimeVersion].filter(Boolean).join(" · ")],
    ["模式",mode],
    ["輸出語言",lang[i.language] || i.language],
    ["參考錄音語言",lang[i.refLang] || i.refLang],
    ["語速",Number.isFinite(i.speed) && i.speed>0 ? i.speed+" ×" : ""],
    ["聲線",i.speaker || record.voiceName],
    ["情緒強度",i.emotionStrength],
    ["情緒比例",Array.isArray(i.emotionWeights)?i.emotionWeights.join(" / "):""],
    ["情緒參考",i.emotionReference && "已使用獨立情緒錄音"],
    ["切分",i.splitAt>0 ? [i.splitAt+" 字", i.splitSeparate ? "獨立 WAV ZIP" : (i.splitGapMs ?? 0)+" ms 間隔 · 合併 WAV",
      record.parts ? record.parts+" 段" : ""].filter(Boolean).join(" · ") : ""],
    ["流式轉換",i.stsStreaming && "已啟用"],
    ["逐字稿",i.transcript],["指令",i.instruction],
  ];
  return el("details",{className:"output-details"},el("summary",{},"耗時、模型及設定"),
    el("dl",{},...labels.filter(([,v])=>v !== undefined && v !== null && v !== "" && v !== false)
      .flatMap(([label,value])=>[el("dt",{},label),el("dd",{},String(value))])),
    hint(record.verifiedCuda ? "已核對波形及主模型："+record.verifiedCuda : "未有完整 CUDA 作業證據"));
}

function outputCard(x, redraw) {
  const input = x.input || {};
  const date = new Date(x.created);
  const kind = input.preview ? "試讀" : historySTS(x) ? "STS" : "全文";
  const title = engineName(input.engine)+" · "+kind;
  const validKey = /^[0-9a-f]{32}$/.test(x.id || "");
  const wavURL = "/api/audio/"+x.id;
  const items = [];
  if (validKey && x.audioAvailable) {
    items.push(el("audio",{controls:true,preload:"none",src:wavURL}),
      btn("放到作業島試聽",()=>playInDock(x)),
      el("a",{className:"linkbtn",href:wavURL+"?download=1",download:""},"匯出 WAV"),
      btn(historySelection.has(x.id) ? "取消比較" : "加入比較",()=>{
        if (historySelection.has(x.id)) historySelection.delete(x.id);
        else {
          if (historySelection.size>=2) throw Error("請先取消其中一筆比較。");
          historySelection.add(x.id);
        }
        redraw();
      },{className:"history-compare"}),
      btn("存為角色錄音",async()=>{
        const sample=await fetch(wavURL);
        if(!sample.ok) throw Error("找不到這段輸出音檔。");
        const ref=await api("reference","POST",await sample.blob());
        editingVoice={name:x.voiceName||"",reference:ref.id,
          language:referenceLanguages[input.language]?input.language:"auto",transcript:input.text||""};
        setTab("voices");
      }));
  }
  if (validKey && x.partsAvailable) {
    items.push(el("a",{className:"linkbtn",href:wavURL+"/parts",download:""},
      x.parts>0 ? "下載 "+x.parts+" 段 WAV（ZIP）":"下載分段 WAV（ZIP）"));
  }
  return card(title,el("p",{className:"meta"},
    [(Number.isNaN(date.getTime())?"日期未記錄":date.toLocaleString("zh-HK")),
      displaySeconds(x.seconds,1),x.sampleRate ? x.sampleRate+" Hz":""].filter(Boolean).join("　")),
    ...((x.voiceName || x.fineTuneName) ? [hint([x.voiceName,x.fineTuneName].filter(Boolean).join(" · "))] : []),
    ...(input.text ? [el("p",{className:"history-text"},input.text)] : []),
    outputDetails(x),
    items.length ? box("history-actions",...items) : hint("音檔不在此工作區，保留歷史紀錄。"));
}

function mountHistory() {
  const s=historyState;
  if(s.root?.isConnected)return;
  s.rows.clear();s.nodes.clear();
  s.list=box("history-list");
  s.counter=el("p",{className:"hint",role:"status"},"正在讀取輸出…");
  s.engines=select({"":"所有引擎"},s.engine,v=>{s.engine=v;paintHistory();});
  const search=el("input",{type:"search",placeholder:"搜尋台詞、角色或型號",value:s.query,
    oninput:e=>{s.query=e.target.value;paintHistory();}});
  s.root=box("stack",
    card("搜尋及比較",box("row",field("搜尋",search),
      field("類別",select({all:"全部",tts:"語音合成",sts:"STS",zip:"分段 ZIP"},s.kind,v=>{s.kind=v;paintHistory();})),
      field("引擎",s.engines)),s.counter),
    s.list);
  $("work").replaceChildren(s.root);
}
function paintHistory() {
  const s=historyState;
  if(!s.root?.isConnected)return;
  let visible=0,compared=0;
  const q=s.query.trim().toLocaleLowerCase();
  const list=[...s.rows.values()].sort((a,b)=>String(b.created).localeCompare(String(a.created)));
  list.forEach((x,index)=>{
    const keep=historySelection.has(x.id);
    const input=x.input||{};
    const show=keep || ((!s.engine||s.engine===input.engine) &&
      (s.kind==="all" || (s.kind==="sts"? historySTS(x):s.kind==="zip"? x.partsAvailable:!historySTS(x))) &&
      (!q || [input.text,x.voiceName,x.model,x.fineTuneName,engineName(input.engine)].join(" ").toLocaleLowerCase().includes(q)));
    let node=s.nodes.get(x.id);
    if(!node && show) {
      node=outputCard(x,paintHistory);
      s.nodes.set(x.id,node);
      s.list.append(node);
    }
    if(!node)return;
    node.hidden=!show;
    node.dataset.compared=String(keep);
    node.style.order=String(keep ? index-list.length:index);
    const button=node.querySelector(".history-compare");
    if(button) {
      button.textContent=keep?"取消比較":"加入比較";
      button.setAttribute("aria-pressed",String(keep));
    }
    if(show)visible++;
    if(keep)compared++;
  }
  );
  s.counter.textContent=visible+" / "+s.rows.size+" 筆紀錄 · "+compared+" 筆加入比較"+(visible?"":" · 未有符合條件嘅輸出");
}
function setHistoryRow(x) {
  const s=historyState;
  const prior=s.rows.get(x.id);
  if(prior && (prior.audioAvailable!==x.audioAvailable || prior.partsAvailable!==x.partsAvailable)){
    s.nodes.get(x.id)?.remove();s.nodes.delete(x.id);
  }
  s.rows.set(x.id,x);
  if(!x.audioAvailable)historySelection.delete(x.id);
}
function historyFilters() {
  const s=historyState;
  const names=[...new Set([...s.rows.values()].map(x=>x.input?.engine).filter(Boolean))];
  s.engines.replaceChildren(new Option("所有引擎",""),...names.map(k=>new Option(engineName(k),k)));
  if(s.engine&&!names.includes(s.engine))s.engine="";
  s.engines.value=s.engine;
}
async function history(newID="") {
  const s=historyState, mark=renderID;
  mountHistory();
  const valid=()=>panel()==="history" && renderID===mark && s.root.isConnected;
  if(newID){
    try {
      const row=await api("history/"+newID);
      if(valid() && row?.id===newID){setHistoryRow(row);historyFilters();paintHistory();return}
    } catch {}
    if(!valid())return;
  }
  const ticket=++s.ticket, prev=new Set(s.rows.keys());
  try {
    const files=await api("history");
    if(!valid()||ticket!==s.ticket)return;
    const incoming=new Set();
    for(const row of Array.isArray(files)?files:[]){
      if(!/^[a-f0-9]{32}$/.test(row?.id||""))continue;
      incoming.add(row.id);setHistoryRow(row);
    }
    for(const stale of prev){
      if(incoming.has(stale))continue;
      s.nodes.get(stale)?.remove();s.nodes.delete(stale);s.rows.delete(stale);
      historySelection.delete(stale);
    }
    historyFilters();paintHistory();
  } catch(e){if(valid())s.counter.replaceChildren("載入歷史失敗："+e.message,btn("重試",()=>history()))}
}
