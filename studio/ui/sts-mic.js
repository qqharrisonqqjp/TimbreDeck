"use strict";
let mic=null;
let devices=[],micDevice="",outDevice="";
const micFrames=25600;
window.addEventListener("pagehide",()=>{if(mic)stopMic()});
async function warmSTSMic(){
  if(sts.warming||mic)return;
  const replace=await confirmSTSResident();
  if(replace===null)return;
  sts.warming=true;sts.warmMessage="正在載入模型…";drawSTS();
  try{
    currentJob=await api("sts/warm?model="+encodeURIComponent(sts.model),"POST",{replace});showJob();
    await waitForJob(currentJob,{timeout:4*60*1000});
    sts.warmMessage="模型已啟動及預熱。";
  }catch(e){sts.warmMessage=e.message}
  finally{sts.warming=false;if(arenaOpen==="sts")drawSTS()}
}
async function unloadSTSMic(){
  if(mic)stopMic();
  for(let i=0;i<20;i++){
    const j=await api("job");
    if(j?.kind!=="sts-live"||!["running","cancelling"].includes(j.status))break;
    await new Promise(r=>setTimeout(r,100));
  }
  const state=await api("tts/resident");
  if(state.kind==="sts")await api("sts/stop","POST",{id:state.id});
  sts.warmMessage="STS 模型已卸載。";if(arenaOpen==="sts")drawSTS();
}
function stsDevices(){
  const names=(kind,label)=>({"":label,...Object.fromEntries(devices.filter(x=>x.kind===kind&&x.deviceId&&x.deviceId!=="default"&&x.deviceId!=="communications").map(x=>[x.deviceId,x.label||kind]))});
  const input=select(names("audioinput","系統預設咪高峰"),micDevice,x=>{micDevice=x});
  input.disabled=!!mic;
  const output=select(names("audiooutput","系統預設播放裝置"),outDevice,async x=>{
    if(mic)try{await mic.ctx.setSinkId(x)}catch(e){micText("切換播放裝置失敗："+e.message);drawSTS();return}
    outDevice=x;
  });
  output.disabled=!!mic&&typeof mic.ctx?.setSinkId!=="function";
  return box("row",field("咪高峰（停止後切換）",input),field("變聲輸出裝置",output),
    btn("重新偵測裝置",async()=>{devices=await navigator.mediaDevices.enumerateDevices();if(arenaOpen==="sts")drawSTS()}));
}
function micText(text){sts.micMessage=text;if($("sts-mic"))$("sts-mic").textContent=text}
function stopMic(text="咪高峰已停止。"){
  const x=mic;mic=null;
  if(x){x.abort.abort();x.media?.getTracks().forEach(t=>t.stop());x.ctx?.close().catch(()=>{});}
  micText(text);if(arenaOpen==="sts")drawSTS();
  document.body.dataset.recording="false";
  scheduleDepth();
  showJob();
}
async function openMic(){
  if(mic||sts.warming||!sts.reference)return;
  const model=sts.model;
  const replace=await confirmSTSResident();
  if(replace===null || arenaOpen!=="sts" || sts.model!==model || mic)return;
  if(replace){
    sts.warming=true;drawSTS();
    try {
      currentJob=await api("sts/warm?model="+encodeURIComponent(model),"POST",{replace});
      showJob();await waitForJob(currentJob,{timeout:4*60*1000});
    } finally {
      sts.warming=false;
      if(arenaOpen==="sts")drawSTS();
    }
    if(arenaOpen!=="sts" || sts.model!==model || mic)return;
  }
  $("mini-audio").pause();
  stopSTSPreview();
  const seed=sts.model==="seed-vc-tiny",frames=seed?4800:micFrames;
  const x={abort:new AbortController(),model:sts.model,ref:sts.reference.id,q:[],offset:0,samples:new Float32Array(frames),next:0,count:0,
    session:seed?crypto.randomUUID().replaceAll("-",""):""};
  mic=x;micText("正在開啟咪高峰…");drawSTS();
  document.body.dataset.recording="true";
  scheduleDepth();
  showJob();
  try{
    if(seed){
      const worker=await api("sts/worker");
      if(mic!==x)return;
      if(!worker.pinned||worker.model!==x.model)throw Error("請先按「開始啟動 STS 模型」完成 Seed-VC 預熱。");
    }
    x.ctx=new AudioContext({sampleRate:16000});await x.ctx.resume();
    if(x.ctx.sampleRate!==16000)throw Error("瀏覽器未能輸出 16 kHz 收音。");
    const stream=await navigator.mediaDevices.getUserMedia({audio:{channelCount:1,echoCancellation:true,noiseSuppression:true,...(micDevice?{deviceId:{exact:micDevice}}:{})}});
    if(mic!==x){stream.getTracks().forEach(t=>t.stop());return}
    x.media=stream;
    devices=await navigator.mediaDevices.enumerateDevices();
    if(outDevice)await x.ctx.setSinkId(outDevice);if(mic!==x)return;
    x.input=x.ctx.createMediaStreamSource(stream);
    x.node=x.ctx.createScriptProcessor(seed?512:4096,1,1);const silent=x.ctx.createGain();silent.gain.value=0;
    x.input.connect(x.node);x.node.connect(silent).connect(x.ctx.destination);
    x.node.onaudioprocess=e=>{
      if(mic!==x)return;
      for(const sample of e.inputBuffer.getChannelData(0)){
        x.samples[x.offset++]=sample;
        if(x.offset!==frames)continue;
        const values=x.samples;x.samples=new Float32Array(frames);x.offset=0;
        if(!seed&&!values.some(v=>Math.abs(v)>.004))continue;
        if(x.q.length>=2){stopMic("轉換追唔上收音。請先預載模型，或放慢說話。");return}
        x.q.push(values);pumpMic(x);
      }
    };
    micText("咪高峰收音中。");
    if(arenaOpen==="sts")drawSTS();
  }catch(e){if(mic===x)stopMic(e.message)}
}
function liveWav(samples){
  const b=new ArrayBuffer(44+samples.length*2),d=new DataView(b);
  const tag=(p,s)=>[...s].forEach((c,i)=>d.setUint8(p+i,c.charCodeAt(0)));
  tag(0,"RIFF");d.setUint32(4,b.byteLength-8,true);tag(8,"WAVEfmt ");d.setUint32(16,16,true);
  d.setUint16(20,1,true);d.setUint16(22,1,true);d.setUint32(24,16000,true);d.setUint32(28,32000,true);
  d.setUint16(32,2,true);d.setUint16(34,16,true);tag(36,"data");d.setUint32(40,samples.length*2,true);
  samples.forEach((v,i)=>d.setInt16(44+i*2,Math.max(-32768,Math.min(32767,Math.round(v*32768))),true));return b;
}
async function pumpMic(x){
  if(x.sending)return;x.sending=true;
  try{
    while(mic===x&&x.q.length){
      const session=x.session?"&session="+x.session+"&seq="+x.count:"";
      const response=await fetch("/api/sts/live?reference="+x.ref+"&model="+encodeURIComponent(x.model)+session,{method:"POST",headers:{"X-TimbreDeck":"1","Content-Type":"application/octet-stream"},body:liveWav(x.q.shift()),signal:x.abort.signal});
      if(!response.ok)throw Error((await response.json()).error||"咪高峰轉換失敗。");
      if(response.headers.get("X-Audio-Format")!=="pcm-s16le-mono")throw Error("即時輸出格式不正確。");
      const reader=response.body.getReader();let extra=null;
      while(mic===x){
        const {done,value}=await reader.read();if(done)break;
        let bytes=value;
        if(extra!==null){bytes=new Uint8Array(value.length+1);bytes[0]=extra;bytes.set(value,1)}
        extra=bytes.length%2?bytes[bytes.length-1]:null;
        if(bytes.length<2)continue;
        const pcm=new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength);
        const audio=x.ctx.createBuffer(1,pcm.byteLength>>1,24000),samples=audio.getChannelData(0);
        for(let i=0;i<samples.length;i++)samples[i]=pcm.getInt16(i*2,true)/32768;
        const node=x.ctx.createBufferSource();node.buffer=audio;node.connect(x.ctx.destination);
        const next=Math.max(x.next,x.ctx.currentTime+.03);node.start(next);x.next=next+audio.duration;
      }
      if(mic!==x)return;
      micText("收音及播放進行中 · 已轉換 "+(++x.count)+" 段。");
    }
  }catch(e){if(mic===x)stopMic(e.message)}
  finally{x.sending=false}
}
