"""Real round-trip of the GPT-SoVITS studio workflow using synthetic English fixtures."""

import argparse
from contextlib import contextmanager
from http.client import HTTPConnection
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time
import wave
import zipfile


TEXTS = [
    "The river flows quietly through the valley.",
    "Please bring the blue notebook with you.",
    "Our friends will join us for a late dinner.",
    "Yesterday the sky was clear across the city.",
    "I left the small lamp beside the window.",
    "Three children found a coin on the beach.",
]


@contextmanager
def studio(exe, config, workspace):
    (workspace / "config").mkdir(parents=True, exist_ok=False)
    shutil.copyfile(config, workspace / "config" / "engines.json")
    with (workspace / "server-stderr.log").open("wb") as log:
        proc = subprocess.Popen([exe, "-no-browser", "-data", workspace], stdout=subprocess.PIPE,
                                stderr=log, text=True, encoding="utf-8")
        try:
            line = proc.stdout.readline()
            port = int(re.search(r"127\.0\.0\.1:(\d+)", line)[1])

            def api(path, payload=None, expect=200, method=None):
                conn = HTTPConnection("127.0.0.1", port, timeout=200)
                try:
                    binary = isinstance(payload, bytes)
                    body = payload if binary or payload is None else json.dumps(payload).encode()
                    conn.request(method or ("GET" if body is None else "POST"), "/api/" + path, body, {
                        "X-TimbreDeck": "1", "Content-Type": "application/octet-stream" if binary else "application/json",
                    })
                    resp = conn.getresponse()
                    data = resp.read()
                    assert resp.status == expect, (path, resp.status, data[:500])
                    return json.loads(data) if resp.headers.get_content_type() == "application/json" else data
                finally:
                    conn.close()

            def finish(job, expected="done", timeout=1200):
                last = None
                deadline = time.monotonic() + timeout
                while time.monotonic() < deadline:
                    state = api("job")
                    assert state["id"] == job["id"], state
                    detail = (state["status"], state.get("detail", ""))
                    if detail != last:
                        print(job["kind"], job["id"], *detail, flush=True)
                        last = detail
                    if state["status"] not in {"running", "cancelling"}:
                        assert state["status"] == expected, state
                        return state
                    time.sleep(0.75)
                raise TimeoutError(job)

            yield api, finish
        finally:
            proc.terminate()
            proc.wait(timeout=15)


def read_wave(b):
    with wave.open(io.BytesIO(b), "rb") as wav:
        return wav.getnframes(), wav.getframerate(), bool(any(wav.readframes(wav.getnframes())))


def make_inputs(out):
    out.mkdir(parents=True, exist_ok=True)
    split = out / "two-sentences.wav"
    paths = []
    for i, sentence in enumerate(TEXTS):
        path = out / f"line-{i + 1}.wav"
        if not path.exists():
            subprocess.run(["ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i",
                            f"flite=text={sentence}:voice=slt", "-ac", "1", "-ar", "16000",
                            "-c:a", "pcm_s16le", "-af", "apad=pad_dur=0.25", path], check=True)
        paths.append(path)
    if not split.exists():
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", paths[0], "-i", paths[1],
                        "-filter_complex", "[0:a]apad=pad_dur=0.65[a];[a][1:a]concat=n=2:v=0:a=1",
                        "-c:a", "pcm_s16le", split], check=True)
    return paths, split


def run(args):
    paths, combined = make_inputs(args.out / "fixtures")
    with studio(args.exe, args.config, args.out / args.scenario) as (api, finish):
        ref = api("reference", combined.read_bytes(), 201)
        clip = dict(reference=ref["id"], startMs=0, endMs=int(ref["seconds"] * 1000),
                    language="en", use="pending", sourceName=combined.name)
        spans = api("training/split", clip)
        assert len(spans) >= 2, spans
        if args.scenario == "baseline":
            body = api("training/preview", spans[0], 404)
            assert b"404 page not found" in body, body
            print("baseline HTTP 404: draft preview endpoint absent", flush=True)
            return
        head = spans[0]
        preview = api("training/preview", head)
        frames, rate, audible = read_wave(preview)
        assert audible and abs(1000 * frames / rate - (head["endMs"] - head["startMs"])) < 1, (frames, rate, head)
        edited = dict(head, endMs=head["endMs"] - 100)
        assert read_wave(api("training/preview", edited))[0] < frames
        print("manual split, unsaved player and boundary edits: PASS", flush=True)
        refs = [api("reference", f.read_bytes(), 201)["id"] for f in paths]
        if args.scenario == "denoise":
            ack = api("datasets/auto", dict(references=[refs[0]], sourceNames=[paths[0].name], language="en",
                speaker="Denoise QA", denoise=True, split=False, transcribe=False), 202)
            finish(ack)
            row = next(x for x in api("datasets/auto") if x["id"] == ack["id"])
            assert row["denoise"] and row["pending"] == 1 and not api("training-runs"), row
            payload = api(f"datasets/auto/{ack['id']}/download")
            with zipfile.ZipFile(io.BytesIO(payload)) as files:
                clean = files.read("full.wav")
                assert read_wave(clean)[2] and clean != paths[0].read_bytes()
                assert len(files.read("pending.list").splitlines()) == 1
            print("optional real denoiser applied; untranscribed sample preserved for review; no training: PASS", flush=True)
            return
        if args.scenario == "manual":
            project = api("training", dict(name="Synthetic manual", clips=spans), 201)
            model = next(m["id"] for m in api("asr/models") if "large-v3" in m["id"] and m["status"] == "ready")
            finish(api(f"training/{project['id']}/transcribe", {
                "revision": project["revision"], "asrModel": model, "clips": [project["clips"][0]["id"]],
            }, 202))
            updated = next(p for p in api("training") if p["id"] == project["id"])
            assert updated["clips"][0]["transcript"] and not updated["clips"][1]["transcript"], updated["clips"]
            updated["clips"][0].update(use="train", transcript=TEXTS[0])
            updated["clips"][1].update(use="skip", transcript="Excluded test audio")
            saved = api(f"training/{project['id']}", updated, method="PUT")
            reviewed = api(f"training/{project['id']}/dataset")
            with zipfile.ZipFile(io.BytesIO(reviewed)) as archive:
                data = archive.read("dataset.list").decode()
                assert TEXTS[0] in data and "Excluded test audio" not in data
            imported = api("training/import?name=Review%20roundtrip", reviewed, 201)
            assert len(imported["clips"]) == 1 and imported["clips"][0]["transcript"] == TEXTS[0]
            none = api("datasets/auto", dict(references=refs[:2], sourceNames=["First.wav", "Second.wav"],
                speaker="Draft dataset only", language="en", denoise=False, split=False, transcribe=False), 202)
            finish(none)
            assert not api("training-runs")
            project = api(f"datasets/auto/{none['id']}/project", {}, 201)
            assert [c["sourceName"] for c in project["clips"]] == ["First.wav", "Second.wav"], project
            assert all(c["use"] == "pending" and not c["transcript"] for c in project["clips"])
            assert saved["revision"] > updated["revision"]
            print("per-clip ASR/edit/exclude, dataset-only, ZIP import with source grouping: PASS", flush=True)
            return
        if args.scenario == "manual-train":
            zip_source=(args.out/"synthetic-dataset.zip").read_bytes()
            p=api("training/import?name=Reviewed%20QA",zip_source,201)
            p["clips"][0].update(transcript=TEXTS[0],use="train")
            p["clips"][-1].update(transcript="Excluded sentence from manual QA",use="skip")
            saved=api("training/"+p["id"],p,method="PUT")
            j=api(f"training/{p['id']}/train",{
                "revision":saved["revision"],"options":{"epochs":1,"batch":2,"name":"Reviewed QA v2Pro"},
            },202)
            finish(j)
            record=next(x for x in api("training-runs") if x["id"]==j["id"])
            assert record["status"]=="done" and record["dataset"] and record["modelId"],record
            data=api(f"training-runs/{j['id']}/dataset")
            with zipfile.ZipFile(io.BytesIO(data)) as archive:
                manifest=archive.read("dataset.list").decode()
                assert "Excluded sentence from manual QA" not in manifest and TEXTS[0] in manifest
                assert len(manifest.splitlines())==len(p["clips"])-1
            assert any(x["id"]==record["modelId"] and x["name"]=="Reviewed QA v2Pro" for x in api("trained-models"))
            (args.out/"manual-training-proof.json").write_text(json.dumps(record,indent=2),"utf-8")
            print("real manual training from edited ZIP: corrected transcript retained, excluded clip absent, paired model registered PASS")
            return

        if args.scenario=="roundtrip":
            model_zip=(args.out/"synthetic-v2Pro.zip").read_bytes()
            first = api("trained-models/import?name=Original%20QA%20copy",model_zip,201)
            record={"modelId":first["id"]}
        else:
            choices = api("asr/models")
            default = next((m["id"] for m in choices
                if m["id"]=="faster-whisper-small" and m["status"]=="ready"),"")
            ack = api("datasets/auto", dict(references=refs, sourceNames=[p.name for p in paths], speaker="Synthetic Studio QA",
                      denoise=False, split=True, transcribe=True, language="en", asrModel=default,
                      skipUnrecognized=False, training=dict(name="Synthetic v2Pro", epochs=1, batch=2)), 202)
            finish(ack)
            record = next(r for r in api("training-runs") if r["id"] == ack["id"])
            assert record["dataset"] and record["modelId"] and record["status"] == "done", record
            model_zip = api(f"trained-models/{record['modelId']}/download")
            dataset_zip = api(f"training-runs/{ack['id']}/dataset")
            (args.out / "synthetic-v2Pro.zip").write_bytes(model_zip)
            (args.out / "synthetic-dataset.zip").write_bytes(dataset_zip)
        pack = zipfile.ZipFile(io.BytesIO(model_zip))
        assert set(pack.namelist()) == {"weights/gpt.ckpt", "weights/sovits.pth", "model.json"}
        imported = api("trained-models/import?name=Imported%20Synthetic", model_zip, 201)
        key = imported["id"]
        assert key != record["modelId"]
        assert api(f"trained-models/{key}", {"name": "Renamed QA model"}, method="PATCH")["name"] == "Renamed QA model"
        profile = api("profiles", dict(name="Synthetic only", reference=refs[0], transcript=TEXTS[0], language="en",
                                      modelProject=key, modelProjects=[record["modelId"], key]), 201)
        assert len(profile["modelProjects"]) == 2
        qa = dict(engine="gpt-sovits", variant="v2Pro", mode="clone", language="en", refLang="en",
                  voiceId=profile["id"], text="Listen to this short test. Here comes another short test.",
                  speed=1.0, splitAt=26, splitGapMs=420)
        split_job = api("synthesize", qa, 202)
        finish(split_job)
        output = api(f"audio/{split_job['id']}")
        assert read_wave(output)[2]
        (args.out / "synthetic-split.wav").write_bytes(output)
        root = args.out / args.scenario
        meta = json.loads((root / "outputs" / split_job["id"] / "segments.json").read_text("utf-8"))
        execution = meta["execution"]
        assert len(execution) >= 2 and execution[0]["timings"]["reused"] is False
        assert all(x["timings"]["reused"] is True and "cuda:0" in x["waveform_devices"] for x in execution[1:])
        runs = (root / "logs" / (split_job["id"] + ".log")).read_text().splitlines()
        workers = [line for line in runs if line.startswith("Resident worker:")]
        assert len(workers) == len(execution) and len(set(workers)) == 1, workers
        assert runs.count(workers[0].replace("Resident worker:","Split worker released after the job:"))==1
        history = next(x for x in api("history") if x["id"] == split_job["id"])
        assert history["fineTuneName"] == "Renamed QA model" and history["timing"]["coldStartSeconds"] > 0
        assert history["verifiedCuda"] and "generationSeconds" in history["timing"], history
        assert api("tts/resident") == {"busy": False}
        pinned = api("tts/warm",{"engine":"gpt-sovits","variant":"v2Pro","modelProject":key},202)
        finish(pinned)
        slot = api("tts/resident")
        assert slot["modelProject"] == key and not slot["busy"]
        warm = api("synthesize",dict(qa,splitSeparate=True),202)
        finish(warm)
        zip_data = api(f"audio/{warm['id']}/parts")
        with zipfile.ZipFile(io.BytesIO(zip_data)) as parts:
            files = [name for name in parts.namelist() if name.endswith(".wav")]
            assert len(files) >= 2 and all(read_wave(parts.read(name))[2] for name in files)
        (args.out / "synthetic-split-parts.zip").write_bytes(zip_data)
        warm_record = next(x for x in api("history") if x["id"] == warm["id"])
        assert warm_record["timing"]["resident"] and "coldStartSeconds" not in warm_record["timing"], warm_record
        assert warm_record["partsAvailable"] and not warm_record["audioAvailable"]
        assert api("tts/resident") == slot
        assert api("tts/stop",{"id":slot["id"]}) == {"busy": False}
        print("pinned split ZIP: model retained, cold start not applicable, explicit stop PASS", flush=True)
        print("real training, exported/imported GPT + SoVITS, multi-model role and split CUDA reuse: PASS",
              json.dumps(history["timing"]), flush=True)
        args.out.joinpath("training-proof.json").write_text(json.dumps({
            "training": record, "profile": profile["id"], "reimported": key, "history": history,
            "segments": execution, "worker_logs": workers, "pinnedHistory": warm_record, "explicitStop": True,
        }, indent=2, ensure_ascii=False), "utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=("baseline", "manual", "real", "roundtrip", "denoise", "manual-train"))
    for field in ("exe", "config", "out"):
        parser.add_argument(field, type=Path)
    run(parser.parse_args())
