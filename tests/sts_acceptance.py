"""Actual Windows STS on generated fixtures. No user speech and no other workspace writes."""
from pathlib import Path
import array, hashlib, io, json, os, re, shutil, subprocess, time, urllib.request, wave
from concurrent.futures import ThreadPoolExecutor

base=Path(r"D:/TimbreDeck");out=base/"validation-sts";out.mkdir(exist_ok=True)
work=out/("studio-"+str(time.time_ns()));(work/"config").mkdir(parents=True)
shutil.copy2(base/"acceptance-80ebf58/more/release/StudioData/config/engines.json",work/"config/engines.json")
source=out/"source-synthetic.wav";speaker=out/"voice-synthetic.wav"
for f,voice,words in [
    (source,"awb","May the Force be with you. I can follow this track with a short pause. The stars look brighter in the sky tonight. Listen to the last sentence and compare the voice."),
    (speaker,"slt","This local reference voice is a synthetic test fixture.")]:
    subprocess.run(["ffmpeg","-v","error","-nostdin","-y","-f","lavfi","-i",f"flite=voice={voice}:text={words}","-ar","32000","-ac","1","-c:a","pcm_s16le",str(f)],check=True)
exe=base/"source/Start-STS-test.exe"
p=subprocess.Popen([exe,"-no-browser","-data",work],stdout=subprocess.PIPE,encoding="utf8",env=dict(os.environ,PYTHONDONTWRITEBYTECODE="1",OMP_NUM_THREADS="4"))
try:
    url=p.stdout.readline().strip().split()[-1];assert re.fullmatch(r"http://127\.0\.0\.1:\d+",url)
    def request(route,data=None,timeout=240):
        body=data if isinstance(data,(bytes,type(None))) else json.dumps(data).encode()
        r=urllib.request.Request(url+"/api/"+route,data=body,headers={"X-TimbreDeck":"1","Content-Type":"application/octet-stream" if isinstance(data,bytes) else "application/json"})
        return urllib.request.build_opener(urllib.request.ProxyHandler({})).open(r,timeout=timeout)
    def api(route,data=None):
        with request(route,data) as r:return json.load(r) if r.headers.get_content_type()=="application/json" else r.read()
    a,b=[api("reference",x.read_bytes())["id"] for x in (source,speaker)]
    for case in ("cold","warm"):
        start=time.perf_counter();j=api("sts",dict(source=a,reference=b));key=j["id"]
        def collect():
            with request("sts/"+key+"/stream") as r:
                assert r.headers["X-Sample-Rate"]=="24000"
                first=r.read(2);elapsed=time.perf_counter()-start
                assert r.headers["X-Audio-Format"]=="pcm-s16le-mono" and first
                snap=api("job")
                return first+r.read(),elapsed,snap["status"]
        with ThreadPoolExecutor(max_workers=1) as ex:
            f=ex.submit(collect);current=api("job")
            for i in range(600):
                if current["status"] not in ("running","cancelling"):break
                time.sleep(.35);current=api("job")
            if current["status"]!="done":
                print((work/"logs"/(key+".log")).read_text("utf8",errors="replace")[-5500:],flush=True)
                raise AssertionError(current)
            pcm,latency,state=f.result(timeout=30)
        total=time.perf_counter()-start
        wav=api("audio/"+key+"?download=1")
        with wave.open(io.BytesIO(wav)) as f:
            expected=f.readframes(f.getnframes())
            assert pcm==expected and f.getframerate()==24000
        samples=array.array("h",pcm);assert max(samples)>100 and min(samples)<-100
        diagnostics=json.loads((work/"outputs"/key/"execution.json").read_text("utf8"))
        assert diagnostics["waveform_devices"]==["cuda:0"]
        assert all(diagnostics["module_devices"][m]==["cuda:0"] for m in ("flow","hift"))
        assert diagnostics["mode"]=="voice-convert" and not diagnostics["stream"]["asr"] and not diagnostics["stream"]["text_generation"]
        assert diagnostics["stream"]["warm"]==(case=="warm"),diagnostics
        assert len(diagnostics["stream"]["chunks"])>=2 and state=="running",diagnostics
        assert all(api("reference/"+key)==src.read_bytes() for key,src in ((a,source),(b,speaker)))
        saved=out/("sts-"+case+".wav");saved.write_bytes(wav)
        result=dict(id=j["id"],exe_sha=hashlib.sha256(exe.read_bytes()).hexdigest(),first_audio_http_s=round(latency,3),total_http_s=round(total,3),
                    audio_s=len(pcm)/48000,active_when_first_audio=state,exact_pcm_match=True,file=str(saved),
                    diagnostic=diagnostics,originals_preserved=True,isolated_data=str(work))
        (out/("real-sts-"+case+".json")).write_text(json.dumps(result,indent=2),"utf8");print(json.dumps(result,ensure_ascii=False),flush=True)

finally:p.terminate();p.wait(timeout=12)
