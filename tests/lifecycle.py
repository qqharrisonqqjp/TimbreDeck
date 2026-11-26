"""Verify separate installation, removal, and data preservation through the launcher."""
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import wave

from smoke import file, fixtures


REPO = Path(__file__).resolve().parents[1]


def main():
    binary = Path(sys.argv[1]).resolve()
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        source = root / "fixture"
        fixtures(source)
        fixture_runtimes = source / "runtimes"
        for engine in fixture_runtimes.iterdir():
            interpreter = engine / "runtime/python.exe"
            if os.name != "nt":
                interpreter.unlink()
                file(interpreter, "#!/bin/sh\nexec " + shlex.quote(sys.executable) + ' "$@"\n')
        gpt = fixture_runtimes / "gpt-sovits"
        file(gpt / "sv.py", "sv_path = ''")
        file(gpt / "fast_langdetect.py", """
from types import SimpleNamespace
def LangDetectConfig(**kwargs): return kwargs
def LangDetector(config): return config
infer = SimpleNamespace()
""")
        tts = gpt / "GPT_SoVITS/TTS_infer_pack/TTS.py"
        tts.write_text(tts.read_text().replace(
            'assert config["custom"]["t2s_weights_path"].endswith("learned.ckpt")',
            'assert config["custom"]["t2s_weights_path"].endswith("s1v3.ckpt")',
        ).replace(
            'assert config["custom"]["vits_weights_path"].endswith("learned.pth")',
            'assert config["custom"]["vits_weights_path"].endswith("s2Gv2Pro.pth")',
        ))
        index = fixture_runtimes / "indextts2"
        file(index / "huggingface_hub/constants.py", "")
        file(index / "transformers/utils/hub.py", "")
        index_worker = index / "indextts/infer_v2.py"
        index_worker.write_text(index_worker.read_text().replace(
            "assert kwargs == dict(use_cuda_kernel=False, use_deepspeed=False)",
            "assert kwargs['model_dir'].endswith('checkpoints') and kwargs['cfg_path'].endswith('config.yaml')",
        ))
        studio = root / "聲台測試"
        runtime = subprocess.Popen([str(binary), "-no-browser", "-data", str(studio)],
                                   stdout=subprocess.PIPE, encoding="utf-8")
        try:
            url = runtime.stdout.readline().split()[-1]
            exercise(root, fixture_runtimes, studio, url)
        finally:
            runtime.terminate()
            runtime.wait(timeout=15)
    print("Five split packs synthesized with fixtures; reinstall, legacy migration and explicit deletion preserved user data.")


def exercise(root, fixture_runtimes, studio, url):
    def api(path, data=None, method=None, code=None):
        body = data if isinstance(data, bytes) or data is None else json.dumps(data).encode()
        request = urllib.request.Request(url + "/api/" + path, data=body, method=method,
                                         headers={"X-TimbreDeck": "1"})
        try:
            with urllib.request.urlopen(request, timeout=10) as res:
                assert code is None, f"{path} unexpectedly succeeded"
                return json.load(res) if res.headers.get_content_type() == "application/json" else res.read()
        except urllib.error.HTTPError as error:
            assert code == error.code, error.read()
            return None

    def wait(job):
        until = time.monotonic() + 20
        while time.monotonic() < until:
            state = api("job")
            assert state["id"] == job["id"]
            if state["status"] != "running":
                if state["status"] != "done":
                    log = studio / "logs" / (state["id"] + ".log")
                    raise AssertionError((state, log.read_text(errors="replace") if log.exists() else ""))
                return job["id"]
            time.sleep(.04)
        raise AssertionError("operation timed out")

    def build_pack(engine, kind, variant=""):
        folder = root / "packs" / engine / (kind + variant)
        command = [sys.executable, str(REPO / "tools/make_pack.py"), engine, "fixture",
                   str(fixture_runtimes / engine), str(folder), "--kind", kind]
        if variant:
            command.extend(["--variant", variant])
        subprocess.run(command, check=True)
        return next(folder.glob("*.json"))

    def install(engine, pack):
        wait(api("import-pack/" + engine, {"path": str(pack)}))

    def wave_bytes(sample):
        f = io.BytesIO()
        with wave.open(f, "wb") as wav:
            wav.setparams((1, 2, 16000, 0, "NONE", "NONE"))
            wav.writeframes(sample * 48000)
        return f.getvalue()

    recorded = wave_bytes(b"\x01\x00")
    ref = api("reference", recorded)["id"]
    voice = api("profiles", dict(name="保留的角色", reference=ref, transcript="參考。", language="yue"))
    project = api("training", dict(name="保留的訓練", clips=[dict(reference=ref, sourceName="sample.wav",
                  startMs=0, endMs=3000, language="yue", transcript="參考。", use="train")]))
    api("draft", dict(engine="gpt-sovits", text="留下台詞", drafts={}, reference=ref), method="PUT")
    cases = [
        ("gpt-sovits", "v2Pro", dict(speed=1, language="yue")),
        ("cosyvoice3", "Fun-CosyVoice3-0.5B", dict(speed=1.25)),
        ("voxcpm2", "VoxCPM2", dict(instruction="清朗")),
        ("indextts2", "IndexTTS-2", {}),
        ("qwen3-tts", "Base", dict(language="zh")),
    ]
    outputs = {}
    for engine, variant, args in cases:
        group = api("state")["engines"][engine]
        assert not group["engineInstalled"]
        model = build_pack(engine, "model", variant)
        installer = build_pack(engine, "runtime")
        install(engine, model)
        group = api("state")["engines"][engine]
        assert not group["engineInstalled"] and group["availableVariants"] == [variant] and not group["ready"], group
        install(engine, installer)
        assert api("state")["engines"][engine]["ready"]
        payload = dict(engine=engine, mode="clone", text="第一句。", voiceId=voice["id"], **args)
        outputs[engine] = wait(api("synthesize", payload))
        row = next(x for x in api("history") if x["id"] == outputs[engine])
        assert row["runtimeVersion"] == row["packVersion"] == "fixture"
        if engine == "qwen3-tts":
            keep = api("installed-models/" + engine)[0]
            pack = api("installed-packs/" + engine)[0]
            api("installed-packs/" + engine + "/" + pack["id"], method="DELETE")
            assert api("installed-models/" + engine) == [keep]
            install(engine, installer)
            api(f"installed-models/{engine}/{variant}/{keep['id']}?source=model", method="DELETE")
            group = api("state")["engines"][engine]
            assert group["engineInstalled"] and not group["ready"]
            assert api("audio/" + outputs[engine])
            install(engine, model)
            assert api("state")["engines"][engine]["ready"]

    legacy = build_pack("gpt-sovits", "bundle")
    install("gpt-sovits", legacy)
    active = next(row for row in api("installed-packs/gpt-sovits") if row["active"])
    bundled = next(row for row in api("installed-models/gpt-sovits") if row["source"] == "bundle")
    file(Path(bundled["path"]) / "GPT_weights_v2Pro/personal.ckpt", "保留個人模型")
    api("installed-packs/gpt-sovits/" + active["id"], method="DELETE")
    retained = api("installed-models/gpt-sovits")
    assert any(row["source"] == "model" and row["id"].startswith(bundled["id"]) for row in retained), retained
    assert (studio / "preserved/gpt-sovits" / active["id"] / "GPT_weights_v2Pro/personal.ckpt").is_file()

    path = studio / "training" / project["id"] / "run"
    file(path / "s1-weights/derived.ckpt", "fixture")
    file(path / "s2-weights/derived.pth", "fixture")
    file(path / "data/2-name2text.txt", "保留特徵\n")
    file(path / "model.json", json.dumps({
        "engine": "gpt-sovits", "model": "v2Pro",
        "source_revision": "d7c2210da8c013e81a94bfc7b811a477c99fd506",
        "gpt": "s1-weights/derived.ckpt", "sovits": "s2-weights/derived.pth",
    }))
    assert api("trained-models")
    api("trained-models/" + project["id"], method="DELETE")
    assert not api("trained-models") and not (path / "s1-weights").exists()
    assert (path / "data/2-name2text.txt").is_file()
    assert api("profiles") == [voice] and api("training") == [project]
    assert api("state")["draft"]["text"] == "留下台詞"
    assert api("reference/" + ref) == recorded and len(api("history")) == 5
    assert len(list((studio / "config/removed-assets").glob("*.json"))) == 4

    unused = api("reference", wave_bytes(b"\x02\x00"))["id"]
    assert [clip["id"] for clip in api("unused-recordings")] == [unused]
    api("unused-recordings/" + ref, method="DELETE", code=409)
    api("unused-recordings/" + unused, method="DELETE")
    api("reference/" + unused, code=404)
    removed = outputs["qwen3-tts"]
    api("audio/" + removed, method="DELETE")
    api("audio/" + removed, code=404)
    assert not next(row for row in api("history") if row["id"] == removed)["audioAvailable"]
    assert api("reference/" + ref) == recorded


if __name__ == "__main__":
    main()
