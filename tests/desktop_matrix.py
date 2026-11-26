"""Local host verification: real ASR catalog, MP4 import and GPU split synthesis."""
from pathlib import Path
from array import array
import hashlib, io, json, os, shutil, subprocess, sys, time, urllib.request, wave

root = Path(__file__).resolve().parents[2]
base = root / "acceptance-80ebf58"
out = root / "validation-next"
out.mkdir(exist_ok=True)
work = out / f"studio-{time.time_ns()}"
(work / "config").mkdir(parents=True)
shutil.copyfile(base / "more/release/StudioData/config/engines.json", work / "config/engines.json")
wav = root / "asr-update/verification/1.wav"
media = out / "fixture.m4a"
subprocess.run(["ffmpeg", "-y", "-v", "error", "-nostdin", "-i", wav, media], check=True)
env = dict(os.environ, TIMBREDECK_ASR_ASSETS=str(root / "asr-update/assets"),
           TIMBREDECK_CLEAN_ASSETS=str(root / "noise-update/assets"), PYTHONUTF8="1",
           HF_HUB_OFFLINE="1", TRANSFORMERS_OFFLINE="1", PYTHONDONTWRITEBYTECODE="1", OMP_NUM_THREADS="4")
p = subprocess.Popen([root / "source/Start.exe", "-no-browser", "-data", work], env=env,
                     stdout=subprocess.PIPE, encoding="utf-8")
report = {"tests": [],"exe_sha":hashlib.sha256((root/"source/Start.exe").read_bytes()).hexdigest(),"isolated_data":str(work)}
def put():
    (out / ("additional-asr.json" if "--additional" in sys.argv else "desktop-matrix.json")).write_text(json.dumps(report, ensure_ascii=False, indent=2), "utf8")
try:
    url = p.stdout.readline().strip().split()[-1]
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def api(x, q=None):
        body = q if isinstance(q, (bytes, type(None))) else json.dumps(q).encode()
        req = urllib.request.Request(url + "/api/" + x, data=body,
            headers={"X-TimbreDeck":"1","Content-Type":"application/octet-stream" if isinstance(q, bytes) else "application/json"})
        with client.open(req, timeout=240) as res:
            return json.load(res) if res.headers.get_content_type()=="application/json" else res.read()
    reference=api("reference",media.read_bytes())
    assert hashlib.sha256(media.read_bytes()).hexdigest()==reference["sourceSha256"]
    assert 3<=reference["seconds"]<=10 and api("reference/"+reference["id"])[:4]==b"RIFF"
    report["import"]=reference
    models=["sensevoice-onnx","faster-whisper-small","faster-whisper-large-v3-turbo","faster-whisper-large-v3",
            "qwen3-asr-0.6b","qwen3-asr-1.7b","fun-asr-nano","fun-asr-mlt-nano"]
    options={x["id"]:x["status"] for x in api("asr/models")}
    for name in (name for name in models if options[name]=="ready" and ("--all" in sys.argv or "--additional" in sys.argv and name in models[4:])):
        start=time.monotonic()
        r=api("asr",{"reference":reference["id"],"language":"en","model":name})
        assert r["model"]==name and not r.get("warning") and "station" in r["text"].lower(),r
        r["elapsed_seconds"]=round(time.monotonic()-start,2)
        report["tests"].append(r);print(name,r["elapsed_seconds"],r["text"],flush=True);put()
    if "--additional" in sys.argv:
        # Independent local Cantonese synthetic sample, not the user's reference.
        sample=root/"asr-update/worker-tests/synthetic-cantonese.wav"
        yue=api("reference",sample.read_bytes())["id"]
        report["yue"]={}
        for name in ("qwen3-asr-0.6b","sensevoice-onnx"):
            start=time.monotonic();ans=api("asr",{"reference":yue,"language":"yue","model":name})
            assert ans["model"]==name and ans["language"]=="yue" and ans["text"],ans
            ans["elapsed_seconds"]=round(time.monotonic()-start,2)
            report["yue"][name]=ans;print("yue",name,ans["text"],flush=True);put()
        sys.exit(0)
    if "--parts" in sys.argv:
        q={"engine":"gpt-sovits","mode":"clone","reference":reference["id"],"refLang":"en","language":"en",
            "text":"Tonight the doors are open. Please wait by the gate.",
            "transcript":"The train will leave the station at eight o'clock tonight.","speed":1,"splitAt":31,"splitSeparate":True}
        job=api("synthesize",q)["id"];deadline=time.monotonic()+480
        while time.monotonic()<deadline:
            j=api("job");assert j["id"]==job
            if j["status"]!="running":break
            time.sleep(.5)
        assert j["status"]=="done",j
        from zipfile import ZipFile
        data=api("audio/"+job+"/parts");(out/"split-separate.zip").write_bytes(data)
        with ZipFile(io.BytesIO(data)) as z:
            assert not z.testzip() and z.namelist()==["part-001.wav","part-002.wav"]
            for name in z.namelist():
                with wave.open(io.BytesIO(z.read(name))) as f:assert f.getnframes()>0 and f.getframerate()==32000
        row=next(x for x in api("history") if x["id"]==job)
        assert row["parts"]==2 and not row["audioAvailable"] and row["verifiedCuda"].startswith("cuda:0")
        (out/"split-separate.json").write_text(json.dumps(dict(job=job,parts=2,sha256=hashlib.sha256(data).hexdigest(),engine="gpt-sovits",verified_cuda=row["verifiedCuda"]),indent=2),"utf8")
        print("parts ZIP: two real 32 kHz WAVs",job,flush=True);sys.exit(0)
    qa_text="Tonight the doors are open. Please wait by the gate."
    job=api("synthesize",{"engine":"gpt-sovits","mode":"clone","reference":reference["id"],"refLang":"en","language":"en","text":qa_text,
                          "transcript":"The train will leave the station at eight o'clock tonight.","speed":1,"splitAt":31,"splitGapMs":375})["id"]
    deadline=time.monotonic()+480
    while time.monotonic()<deadline:
        j=api("job");assert j["id"]==job
        if j["status"] not in ("running","cancelling"):break
        time.sleep(.5)
    assert j["status"]=="done",j
    outputs=work/"outputs"/job;entry=json.loads((outputs/"segments.json").read_text())
    assert entry["parts"]==["Tonight the doors are open.","Please wait by the gate."] and entry["gapMs"]==375
    assert all(e["waveform_devices"]==["cuda:0"] for e in entry["execution"])
    data=api("audio/"+job+"?download=1")
    with wave.open(io.BytesIO(data)) as f:
        rate=f.getframerate();frames=f.getnframes();values=array("h",f.readframes(frames))
    assert (b"\0\0"*int(rate*.375)) in values.tobytes()
    assert any(x["id"]==job and x.get("verifiedCuda","").startswith("cuda:0") for x in api("history"))
    (out/"split-demo.wav").write_bytes(data)
    report["split"]=dict(job=job,parts=entry["parts"],seconds=frames/rate,gap_ms=375,sha256=hashlib.sha256(data).hexdigest(),cuda="cuda:0")
    print("split",report["split"],flush=True);put()
finally:
    p.terminate();p.wait(timeout=10)
