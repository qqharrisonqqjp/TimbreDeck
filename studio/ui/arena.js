"use strict";
const arena = {tts:{lang:"en",configs:[],rows:[]}, asr:{lang:"en",choose:new Set(),rows:[]}};
let arenaSequence = 0;

function arenaRefresh(kind) { if (arenaOpen===kind) drawArena(); }
function arenaMedia(state, key, kind, label, optional=false) {
  return box("stack",field(label,el("input",{type:"file",accept:mediaAccept,
    disabled:!!state.busy||!!state.loading,onchange:async e=>{
      const f=e.target.files[0];
      if(!f)return;
      state.loading=true;state.error="";arenaRefresh(kind);
      try{const r=await api("reference","POST",f); state[key]=r.id;state[key+"Name"]=f.name;}
      catch(err){state.error=err.message;}
      finally{state.loading=false;arenaRefresh(kind);}
    }})),state[key] ? box("actions",hint(state[key+"Name"] || "已選錄音"),
    ...(optional ? [btn("移除檔案",()=>{state[key]="";state[key+"Name"]="";arenaRefresh(kind);},{disabled:!!state.busy})]:[])) :
    hint(optional ? "尚未選擇（部分模式不用）" : "選擇要比較的錄音。"));
}

function drawArena() {
  if (arenaOpen==="tts") drawTTSArena();
  else drawASRArena();
}

function drawASRArena() {
  const s=arena.asr;
  const flags=asrOptions.filter(x=>x.id&&x.status==="ready");
  const rows=s.rows.map(r=>box("arena-result",el("h3",{},r.name),
    r.error ? hint(r.error) : el("p",{},r.text||"沒有辨識到對白"),
    ...(!r.error?[hint(displaySeconds(r.elapsed,1)+(r.model?" · "+r.model:""))]:[]),
    ...(r.warning?[hint(r.warning)]:[])));
  $("work").replaceChildren(box("grid",card("ASR 比較設定",
    arenaMedia(s,"ref","asr","共同錄音"),
    el("fieldset",{className:"training-fields",disabled:!!s.busy},
      field("錄音語言",select(referenceLanguages,s.lang,v=>{s.lang=v;},"arena-language")),
      box("arena-models",...flags.map(({id,name,note})=>el("label",{title:note,className:"dataset-option"},
        el("input",{type:"checkbox",checked:s.choose.has(id),onchange:e=>{
          if(e.target.checked)s.choose.add(id);else s.choose.delete(id);drawASRArena();
        }}),name))),
      btn("開始比較",()=>runASRArena(flags),{className:"primary",disabled:!!(s.busy||s.loading||!s.ref||s.choose.size<2)})),
    hint(s.error || (s.busy?"按次序辨識。":"請選至少兩款已安裝的 ASR 模型。"))),
    card("比較結果",...rows)));
}

async function runASRArena(flags) {
  const s=arena.asr, models=flags.filter(m=>s.choose.has(m.id));
  if(s.busy||!s.ref||models.length<2)return;
  if ((await api("tts/resident")).busy) throw Error("已有作業進行中，未開始比較。");
  s.busy=true;s.rows=[];s.error="";arenaRefresh("asr");
  try {
    for (const model of models) {
      const before=performance.now(),row={name:model.name};
      try {
        const result=await api("asr","POST",{reference:s.ref,language:s.lang,model:model.id});
        Object.assign(row,{text:result.text,model:result.model||result.provider,
          warning:(result.warning||"")+(result.cached?"（快取）":"")});
      } catch(e){row.error=e.message;}
      row.elapsed=(performance.now()-before)/1000;
      s.rows.push(row);arenaRefresh("asr");
    }
  } finally {s.busy=false;arenaRefresh("asr");}
}

function arenaEngineList() {
  return Object.keys(engines).filter(k=>engines[k].ready && engines[k].modes?.length);
}
function arenaMode(engine, id) {
  const data=engines[engine];
  return data?.modes.find(m=>m.id===id && canRun(data,m)) ||
    data?.modes.find(m=>m.id===(engine==="qwen3-tts"?"simple-clone":"clone") && canRun(data,m)) ||
    data?.modes.find(m=>canRun(data,m));
}
function addTTSConfig(s) {
  if(s.configs.length>=12)return;
  const options=arenaEngineList();
  const engine=options.find(k=>k===draft.engine)||options[0];
  if(!engine) throw Error("請先連接至少一款 TTS 引擎。");
  s.configs.push({id:++arenaSequence,engine});
  drawTTSArena();
}
function arenaConfig(s,c,index,engineList) {
  const selectEngine = select(Object.fromEntries([...new Set([...engineList,c.engine])].map(k=>[k,engineName(k)])),
    c.engine,k=>{
      Object.assign(c,{engine:k,mode:"",variant:"",language:"",speaker:"",speed:1,
        instruction:"",modelProject:"",emotionReference:"",emotionWeights:undefined,emotionStrength:undefined});
      drawTTSArena();
    },"arena-engine-"+c.id);
  const title = el("h3",{},"組合 "+(index+1));
  const actions = box("actions",
    btn("複製設定",()=>{
      s.configs.splice(index+1,0,{...structuredClone(c),id:++arenaSequence});drawTTSArena();
    },{disabled:s.configs.length>=12}),
    btn("移除組合",()=>{s.configs.splice(index,1);drawTTSArena();}));
  const mode=arenaMode(c.engine,c.mode);
  const data=engines[c.engine];
  if (!mode || !data?.ready) return box("arena-config",title,field("引擎",selectEngine),hint("這款引擎目前未就緒。"),actions);
  const variants=mode.variant?[mode.variant]:data.availableVariants || data.variants;
  const variant=variants.includes(c.variant)?c.variant:variants[0];
  const out=data.languageOptions?.includes(c.language)?c.language :
    data.languageOptions?.includes("en")?"en":data.languageOptions?.[0];
  const controls=[
    box("row",field("引擎",selectEngine),field("模式",select(Object.fromEntries(data.modes.filter(m=>canRun(data,m)).map(m=>[m.id,m.label])),
      mode.id,v=>{c.mode=v;c.variant="";drawTTSArena();},"arena-mode-"+c.id)),
      field("基礎模型",select(Object.fromEntries(variants.map(v=>[v,variantName(v)])),variant,v=>{c.variant=v;drawTTSArena();}))),
  ];
  if (c.engine==="gpt-sovits" && variant==="v2Pro") controls.push(field("微調模型",select(
    modelOptions("只用 v2Pro 基礎模型",c.modelProject),c.modelProject||"",v=>{c.modelProject=v;})));
  if (mode.languages) controls.push(field("輸出台詞語言",select(
    Object.fromEntries(data.languageOptions.map(v=>[v,lang[v]||v])),out,v=>{c.language=v;})));
  if (data.speed) controls.push(field("語速",el("input",{type:"number",min:0.5,max:2,step:0.05,value:c.speed??1,
    oninput:e=>{c.speed=e.target.valueAsNumber;}})));
  if (mode.speaker) controls.push(field("預設聲線",select(Object.fromEntries(data.speakers.map(v=>[v,v])),
    c.speaker||data.speakers[0],v=>{c.speaker=v;})));
  if (mode.instruction || mode.style) controls.push(field("講法指令",area(undefined,c.instruction||"",
    mode.instruction?"此模式需要講法指令":"可選", v=>{c.instruction=v;})));
  if (mode.emotion) controls.push(...emotionFields(mode.emotion,c,(key,value)=>{c[key]=value;}, {
    id:"arena-emotion-"+c.id,
    upload:async file=>{
      s.loading=true;drawTTSArena();
      try {const saved=await api("reference","POST",file);c.emotionReference=saved.id;c.emotionName=file.name;}
      catch(e){s.error=e.message;}
      finally{s.loading=false;arenaRefresh("tts");}
    },
  }));
  return box("arena-config",box("training-clip-heading",title,actions),...controls);
}

function drawTTSArena() {
  const s=arena.tts, available=arenaEngineList();
  const disabled=!!(s.busy||s.loading);
  const rows=s.rows.map(r=>box("arena-result",el("h3",{},r.name),
    ...(r.error?[hint(r.error)]:[
      el("audio",{controls:true,preload:"none",src:"/api/audio/"+r.job}),
      el("a",{className:"linkbtn",href:"/api/audio/"+r.job+"?download=1",download:""},"下載 WAV"),
      outputDetails(r.record || {input:r.input}),
    ])));
  $("work").replaceChildren(box("arena-grid",
    box("stack",card("共同條件",el("fieldset",{disabled,className:"training-fields"},
      field("共同台詞",area("arena-text",s.text||"","所有組合會朗讀同一句",v=>{s.text=v;})),
      arenaMedia(s,"ref","tts","共同參考錄音",true),
      field("錄音語言",select(referenceLanguages,s.lang,v=>{s.lang=v;},"arena-language")),
      field("參考逐字稿（留空可自動辨識）",area("arena-transcript",s.transcript||"","參考錄音中的實際台詞",v=>{s.transcript=v;})),
      asrModelPicker(v=>{s.asrModel=v;},s.asrModel??asrChoice,"擂臺辨識模型"))),
      card("模型組合",
        el("fieldset",{disabled,className:"training-fields"},
          ...(s.configs.length?s.configs.map((c,i)=>arenaConfig(s,c,i,available)):[hint("按「添加模型」建立第一個組合。可以重複添加同一個模型，再調不同設定。")]),
          btn("添加模型",()=>addTTSConfig(s),{disabled:!available.length||s.configs.length>=12})),
        box("actions",btn("開始比較",runTTSArena,{className:"primary",disabled:disabled||s.configs.length<2}),
          hint(s.error || (s.busy?"順序生成 · "+s.rows.length+" / "+s.configs.length:"最少兩個組合；每次只執行一個。"))),
        ...(s.busy?[el("progress",{max:s.configs.length,value:s.rows.length,"aria-label":"TTS 擂臺進度"})]:[]))),
    card("比較結果",...(rows.length?rows:[hint("完成後會在此列出各組合的音檔、實測耗時同設定。")]))));
}

function ttsArenaPayload(s,c,index) {
  const data=engines[c.engine],mode=arenaMode(c.engine,c.mode);
  const name="組合 "+(index+1)+" · "+engineName(c.engine)+(mode ? " · "+mode.label : "");
  const item = {name};
  if (!mode||!data.ready) {item.error="這款引擎或模型未就緒。";return item;}
  const variant=mode.variant || (data.availableVariants?.includes(c.variant)?c.variant:data.availableVariants?.[0] || data.variants[0]);
  const input=item.input={engine:c.engine,mode:mode.id,variant,text:s.text.trim(),preview:false,
    speed:data.speed?(c.speed??1):1};
  if (mode.reference) {
    if (!s.ref) item.error="此模式需共同參考錄音。";
    input.reference=s.ref;
  }
  if (mode.transcript) {item.transcript=true;input.transcript=s.transcript?.trim() || "";}
  if (mode.languages) input.language=data.languageOptions.includes(c.language)?c.language:
    data.languageOptions.includes("en")?"en":data.languageOptions[0];
  if (c.engine==="gpt-sovits") {
    input.refLang=s.lang;
    if (variant==="v2Pro") input.modelProject=c.modelProject||"";
    input.useBaseModel=!input.modelProject;
  }
  if (mode.instruction || mode.style) {
    input.instruction=c.instruction?.trim()||"";
    if (mode.instruction && !input.instruction) item.error="此模式要填講法指令。";
  }
  if (mode.speaker) input.speaker=c.speaker||data.speakers[0];
  if (mode.emotion) input.emotionStrength=c.emotionStrength??1;
  if (mode.emotion==="audio") input.emotionReference=c.emotionReference||"";
  if (mode.emotion==="vector") input.emotionWeights=c.emotionWeights||Array(8).fill(0);
  return item;
}

async function runTTSArena() {
  const s=arena.tts;
  if(s.busy||s.configs.length<2)return;
  if(!s.text?.trim()) throw Error("請先填寫共同台詞。");
  const plan=s.configs.map((c,i)=>ttsArenaPayload(s,c,i));
  s.busy=true;s.rows=[];s.error="";arenaRefresh("tts");
  try {
    const resident=await api("tts/resident");
    if (resident.busy) throw Error("已有作業進行中，未開始擂臺。");
    const asr=plan.some(r=>!r.error && r.transcript && !r.input.transcript);
    if (resident.id && (asr || plan.some(r=>!r.error && !sameTTS(resident,r.input)))) {
      if (!window.confirm("目前常駐："+residentName(resident)+"。\n先停止常駐模型，再逐組比較？")) return;
      await api(resident.kind==="tts" ? "tts/stop" : "sts/stop","POST",{id:resident.id});
    }
    let transcript="", errASR="";
    if (asr) {
      try {
        const result=await api("asr","POST",{reference:s.ref,language:s.lang,model:s.asrModel??asrChoice});
        transcript=result.text?.trim()||"";
        if(!transcript)errASR="辨識未有台詞，請手動填寫共同逐字稿。";
      } catch(e) {errASR="共同逐字稿辨識失敗："+e.message;}
    }
    for(const row of plan) {
      if (!row.error) {
        try {
          if (row.transcript && !row.input.transcript) {
            if (errASR) throw Error(errASR);
            row.input.transcript=transcript;
          }
          const ack=await api("synthesize","POST",row.input);
          currentJob=ack;
          showJob();
          await waitForJob(ack);
          row.job=ack.id;
          try {row.record=await api("history/"+ack.id);} catch {}
        } catch(e) {row.error=e.message;}
      }
      s.rows.push(row);arenaRefresh("tts");
      if (row.error==="已取消") break;
    }
  } catch(e) {s.error=e.message;}
  finally{s.busy=false;arenaRefresh("tts");}
}
