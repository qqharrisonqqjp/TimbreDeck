"""Stdlib integration tests for the agent CLI against a loopback fake API."""

import contextlib
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import wave
import zipfile


_tool = Path(__file__).resolve().parents[1] / "tools" / "td_agent.py"
_spec = importlib.util.spec_from_file_location("td_agent", _tool)
agent = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(agent)

INSTANCE = "a" * 32
JOB = "b" * 32
PERSONAL_DRAFT = "private content never requested or returned"


def wav(seed=1):
    f = io.BytesIO()
    with wave.open(f, "wb") as out:
        out.setparams((1, 2, 8000, 0, "NONE", "not compressed"))
        out.writeframes(bytes([seed, 0]) * 800)
    return f.getvalue()


def zip_fixture():
    f = io.BytesIO()
    with zipfile.ZipFile(f, "w") as out:
        out.writestr("review/fixture.txt", "a local synthetic fixture")
    return f.getvalue()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, value, status=200, media="application/json"):
        content = value if isinstance(value, bytes) else json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Content-Type", media)
        self.end_headers()
        self.wfile.write(content)

    def do_GET(self):
        self.handle_api()

    def do_POST(self):
        self.handle_api()

    def handle_api(self):
        app = self.server
        size = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(size) if size else b""
        app.calls.append((self.command, self.path, self.headers.get(agent.INSTANCE_HEADER), body))
        if self.path == "/api/agent/info":
            if app.redirect:
                self.send_response(302)
                self.send_header("Location", "http://example.com/upload")
                self.end_headers()
                return
            modes = {"clone": {"id": "clone", "reference": True}}
            engines = {}
            for engine in agent.ENGINE_IDS:
                clone = dict(modes["clone"], transcript=engine in ("gpt-sovits", "qwen3-tts", "cosyvoice3"))
                engines[engine] = {"ready": True, "modes": [clone], "variants": ["fixture"],
                                   "path": PERSONAL_DRAFT, "speed": True}
            engines["indextts2"]["modes"].append({"id": "emotion-vector", "reference": True, "emotion": "vector"})
            engines["qwen3-tts"]["modes"].append({"id": "preset", "speaker": True, "languages": True, "variant": "fixture"})
            engines["qwen3-tts"].update(speakers=["Fixture Speaker"], languageOptions=["auto"])
            self.reply({"apiVersion": 1, "product": "TimbreDeck", "instance": INSTANCE,
                        "pid": os.getpid(), "engines": engines, "draft": PERSONAL_DRAFT, **app.features})
        elif self.headers.get(agent.INSTANCE_HEADER) != INSTANCE:
            self.reply({"error": "Instance changed"}, 409)
        elif self.path == "/api/job":
            self.reply(app.job)
        elif self.path == "/api/tts/resident":
            self.reply(app.resident)
        elif self.path == "/api/tts/stop":
            if json.loads(body)["id"] != app.resident.get("id"):
                self.reply({"error": "Resident changed"}, 409)
            else:
                app.resident = {"busy": False}
                self.reply(app.resident)
        elif self.path == "/api/profiles":
            self.reply([{"id": JOB, "name": "Fixture", "language": "ja", "reference": PERSONAL_DRAFT,
                         "transcript": PERSONAL_DRAFT, "localPath": PERSONAL_DRAFT}])
        elif self.path == "/api/history/" + JOB:
            self.reply({"id": JOB, "model": "v5dev", "seconds": 3.2, "sampleRate": 48000,
                        "verifiedCuda": "cuda:0", "timing": {"resident": True, "generationSeconds": .6},
                        "input": {"text": PERSONAL_DRAFT}})
        elif self.path == "/api/reference":
            self.reply({"id": hashlib.sha256(body).hexdigest(), "seconds": 4.1}, 201)
        elif self.path == "/api/asr":
            self.reply({"text": "This is a test.", "provider": "faster-whisper", "language": "en",
                        "requestedLanguage": json.loads(body)["language"], "cached": False})
        elif self.path in ("/api/synthesize", "/api/clean", "/api/datasets/auto"):
            app.job = {"id": JOB, "engine": "gpt-sovits", "kind": self.path.split("/")[-1],
                       "status": "done", "detail": PERSONAL_DRAFT}
            self.reply({**app.job, "status": "running"}, 202)
        elif self.path in ("/api/audio/" + JOB, "/api/clean/audio/" + JOB):
            self.reply(wav(9) if not app.bad_media else b"invalid artifact bytes",
                       media="audio/wav")
        elif self.path == "/api/datasets/auto/" + JOB + "/download":
            self.reply(zip_fixture(), media="application/zip")
        else:
            self.reply({"error": "unexpected request"}, 404)


class CLITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.output = self.root / "exports"
        self.fixture = self.root / "fixture-聲.wav"
        self.fixture.write_bytes(wav())
        self.app = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.app.calls, self.app.job = [], None
        self.app.redirect, self.app.bad_media = False, False
        self.app.features, self.app.resident = {}, {"busy": False}
        self.thread = threading.Thread(target=self.app.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()
        self.addCleanup(self.app.server_close)
        self.addCleanup(self.thread.join, 3)
        self.addCleanup(self.app.shutdown)
        self.url = f"http://127.0.0.1:{self.app.server_address[1]}"
        self.env = patch.dict(os.environ, {"TD_STUDIO_DATA": str(self.root)})
        self.env.start()
        self.addCleanup(self.env.stop)

    def invoke(self, *args, discovery=False):
        command = ["--timeout", "5"]
        if not discovery:
            command.extend(["--url", self.url])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = agent.main(command + list(args))
        result = json.loads(out.getvalue())
        return code, result

    def session(self, instance=INSTANCE):
        path = self.root / "cache"
        path.mkdir(exist_ok=True)
        (path / "agent-session.json").write_text(json.dumps({"url": self.url, "pid": os.getpid(),
                                                             "instance": instance}), encoding="utf-8")

    def test_discovery_closed_and_identity_mismatch(self):
        code, result = self.invoke("status", discovery=True)
        self.assertEqual(code, 1)
        self.assertEqual(result["error"]["code"], "studio_offline")
        self.assertIn("Start-TimbreDeck-AI", result["error"]["details"]["launchCommand"])
        self.assertFalse((self.root / "cache").exists())
        self.assertFalse(self.app.calls)
        self.session("different")
        code, result = self.invoke("asr", "--reference", str(self.fixture), discovery=True)
        self.assertEqual((code, result["error"]["code"]), (1, "instance_mismatch"))
        self.assertEqual([call[1] for call in self.app.calls], ["/api/agent/info"])
        self.session()
        code, result = self.invoke("status", discovery=True)
        self.assertEqual((code, result["status"]), (0, "idle"))
        self.assertEqual(self.app.calls[-1][2], INSTANCE)

    def test_latest_cleaning_option_and_dataset_controls(self):
        self.app.features.update(actions=["dataset-options"], cleanModes=list(agent.CLEAN_MODES),
                                 readyCleanModes=["mossformer", "lavasr-denoise"])
        code, result = self.invoke("capabilities")
        self.assertEqual(code, 0)
        self.assertEqual(result["cleaningModes"],list(agent.CLEAN_MODES))
        self.assertEqual(result["readyCleaningModes"],["mossformer","lavasr-denoise"])
        code, result = self.invoke("clean","--reference",str(self.fixture),"--mode","lavasr","--output-dir",str(self.output))
        self.assertEqual((code,result["error"]["code"]),(1,"model_unavailable"))
        self.assertFalse(any(c[1]=="/api/reference" for c in self.app.calls))
        code, result = self.invoke("clean","--reference",str(self.fixture),"--mode","mossformer","--output-dir",str(self.output))
        self.assertEqual(code,0,result)
        code, result = self.invoke("dataset","--reference",str(self.fixture),"--language","ja",
            "--clean-mode","lavasr-denoise","--no-split","--no-transcribe","--skip-unrecognized","--output-dir",str(self.output))
        self.assertEqual(code,0,result)
        data = next(json.loads(c[3]) for c in self.app.calls if c[1]=="/api/datasets/auto")
        self.assertEqual([data[k] for k in ("cleanMode","split","transcribe","skipUnrecognized")],
                         ["lavasr-denoise",False,False,True])
        self.assertNotIn("training",data)

    def test_resident_stop_requires_current_id_and_never_cancels_jobs(self):
        self.app.features["actions"]=["resident-control"]
        self.app.resident={"busy":False,"id":JOB,"kind":"tts","engine":"gpt-sovits","variant":"v2Pro"}
        code, data = self.invoke("resident-status")
        self.assertEqual((code,data["resident"]["id"]),(0,JOB))
        code, data = self.invoke("stop-model","--resident-id",INSTANCE)
        self.assertEqual((code,data["error"]["code"]),(1,"resident_changed"))
        self.app.job={"id":INSTANCE,"status":"running"}
        code, data = self.invoke("stop-model","--resident-id",JOB)
        self.assertEqual((code,data["error"]["code"]),(1,"busy"))
        self.assertFalse(any(c[0]=="POST" for c in self.app.calls))
        self.app.job=None
        code, data = self.invoke("stop-model","--resident-id",JOB)
        self.assertEqual((code,data["stopped"]),(0,JOB))
        self.assertFalse(any("/cancel/" in c[1] for c in self.app.calls))

    def test_profile_and_output_metadata_are_filtered(self):
        self.app.features["actions"]=["voice-metadata","output-metadata"]
        code, data = self.invoke("voices")
        self.assertEqual((code,data["voices"]), (0,[{"id":JOB,"name":"Fixture","language":"ja"}]))
        code, data = self.invoke("synthesize","--engine","gpt-sovits","--text","Fixture",
            "--reference",str(self.fixture),"--transcript","Fixture","--output-dir",str(self.output))
        self.assertEqual(code,0,data)
        self.assertEqual(data["output"]["sampleRate"],48000)
        self.assertEqual(data["output"]["model"],"v5dev")
        self.assertNotIn(PERSONAL_DRAFT,json.dumps(data))

    def test_capabilities_strip_draft_paths_and_details(self):
        self.app.job = {"id": JOB, "status": "running", "kind": "clean", "detail": PERSONAL_DRAFT}
        code, result = self.invoke("capabilities")
        self.assertEqual((code, result["status"]), (0, "busy"))
        self.assertNotIn("detail", result["job"])
        self.assertEqual(sorted(result["engines"]), sorted(agent.ENGINE_IDS))
        self.assertNotIn(PERSONAL_DRAFT, repr(result))
        self.assertEqual([call[1] for call in self.app.calls], ["/api/agent/info", "/api/job"])

    def test_opt_in_starts_only_fixed_session_before_actions(self):
        exe = self.root / "TimbreDeck-AI.exe"
        exe.write_bytes(b"fixture never executed")

        def launch(argv, **kwargs):
            self.assertEqual(argv, [str(exe), "ensure-running"])
            self.assertIs(kwargs["shell"], False)
            self.assertLessEqual(kwargs["timeout"], 20)
            self.session()
            return SimpleNamespace(returncode=0)

        with patch.dict(os.environ, {"TD_AGENT_AUTOSTART": "1"}), patch.object(agent, "DEFAULT_DATA", self.root), \
                patch.object(agent, "AUTOSTART_EXE", exe), patch.object(agent.subprocess, "run", side_effect=launch) as execute:
            code, result = self.invoke("status", discovery=True)
            self.assertEqual((code, result.get("status")), (0, "idle"))
            self.assertEqual(execute.call_count, 1)
            self.session("mismatch")
            code, result = self.invoke("status", discovery=True)
            self.assertEqual((code, result["error"]["code"]), (1, "instance_mismatch"))
            self.assertEqual(execute.call_count, 1)
            code, result = self.invoke("status")
            self.assertEqual((code, result.get("status")), (0, "idle"))
            self.assertEqual(execute.call_count, 1)

    def test_opt_in_does_not_start_daily_service_for_data_override(self):
        with patch.dict(os.environ, {"TD_AGENT_AUTOSTART": "1"}), patch.object(agent.subprocess, "run") as execute:
            code, result = self.invoke("status", discovery=True)
            self.assertEqual((code, result["error"]["code"]), (1, "studio_offline"))
            execute.assert_not_called()

    def test_redirect_is_rejected_without_upload(self):
        self.app.redirect = True
        code, result = self.invoke("asr", "--reference", str(self.fixture))
        self.assertEqual((code, result["error"]["code"]), (1, "redirect_blocked"))
        self.assertEqual([call[1] for call in self.app.calls], ["/api/agent/info"])

    def test_busy_and_invalid_reference_do_not_upload(self):
        self.app.job = {"id": JOB, "status": "running", "kind": "training"}
        code, result = self.invoke("asr", "--reference", str(self.fixture))
        self.assertEqual((code, result["error"]["code"]), (1, "busy"))
        self.assertEqual(result["error"]["details"]["job"]["id"], JOB)
        self.app.job = None
        broken = self.root / "bad.wav"
        broken.write_bytes(b"not.wav")
        code, result = self.invoke("clean", "--reference", str(self.fixture), "--reference", str(broken))
        self.assertEqual((code, result["error"]["code"]), (1, "invalid_wav"))
        self.assertFalse(any(call[0] == "POST" for call in self.app.calls))

    def test_asr_upload_only_on_local_pinned_api(self):
        self.session()
        with patch.dict(os.environ, {"HTTP_PROXY": "http://example.com:12345", "NO_PROXY": ""}):
            code, result = self.invoke("asr", "--reference", str(self.fixture), "--language", "en", discovery=True)
        self.assertEqual((code, result["text"], result["provider"]), (0, "This is a test.", "faster-whisper"))
        self.assertEqual(result["reference"], hashlib.sha256(wav()).hexdigest())
        calls = self.app.calls
        self.assertEqual([c[1] for c in calls], ["/api/agent/info", "/api/job", "/api/reference", "/api/asr"])
        self.assertEqual({c[2] for c in calls[1:]}, {INSTANCE})
        self.assertEqual(calls[2][3], wav())

    def test_synthesis_uses_asr_and_exports_checksummed_wav(self):
        code, result = self.invoke("synthesize", "--engine", "gpt-sovits", "--text", "new speech",
                                   "--reference", str(self.fixture), "--output-dir", str(self.output))
        self.assertEqual(code, 0, result)
        item = result["artifacts"][0]
        self.assertEqual((item["kind"], item["type"], item["sha256"], item["job"]),
                         ("synthesis", "audio/wav", hashlib.sha256(wav(9)).hexdigest(), JOB))
        self.assertEqual(Path(item["path"]).read_bytes(), wav(9))
        self.assertEqual(result["recognition"]["provider"], "faster-whisper")
        payload = next(json.loads(c[3]) for c in self.app.calls if c[1] == "/api/synthesize")
        self.assertEqual((payload["transcript"], payload["refLang"], payload["text"]), ("This is a test.", "en", "new speech"))
        self.assertFalse(any(c[1] in ("/api/draft", "/api/state") or c[1].startswith("/api/cancel") for c in self.app.calls))
        self.assertEqual(self.fixture.read_bytes(), wav())

    def test_all_five_clone_contracts_and_optional_modes(self):
        for engine in agent.ENGINE_IDS:
            code, result = self.invoke("synthesize", "--engine", engine, "--text", "clone fixture",
                                       "--reference", str(self.fixture), "--transcript", "known words",
                                       "--output-dir", str(self.output / engine))
            self.assertEqual(code, 0, result)
        for engine, mode, options in (
            ("indextts2", "emotion-vector", ("--reference", str(self.fixture), "--emotion-weights") + ("0.2",) * 8),
            ("qwen3-tts", "preset", ("--speaker", "Fixture Speaker", "--variant", "fixture")),
        ):
            code, result = self.invoke("synthesize", "--engine", engine, "--mode", mode, "--text", "mode fixture",
                                       "--output-dir", str(self.output / mode), *options)
            self.assertEqual(code, 0, result)
        jobs = [json.loads(call[3]) for call in self.app.calls if call[1] == "/api/synthesize"]
        self.assertEqual([job["engine"] for job in jobs[:5]], list(agent.ENGINE_IDS))
        self.assertEqual(jobs[-2]["emotionWeights"], [0.2] * 8)
        self.assertEqual(jobs[-1]["speaker"], "Fixture Speaker")
        self.assertEqual(jobs[-1]["reference"], "")
        self.assertFalse(any(call[1] == "/api/asr" for call in self.app.calls))

    def test_clean_preserves_order_and_never_overwrites(self):
        second = self.root / "second.wav"
        second.write_bytes(wav(2))
        args = ("clean", "--reference", str(second), "--reference", str(self.fixture),
                "--reference", str(second), "--gap-ms", "33", "--mode", "both", "--output-dir", str(self.output))
        code, result = self.invoke(*args)
        self.assertEqual(code, 0, result)
        item = result["artifacts"][0]
        self.assertEqual((item["type"], item["kind"]), ("audio/wav", "clean"))
        data = next(json.loads(c[3]) for c in self.app.calls if c[1] == "/api/clean")
        first_id, second_id = (hashlib.sha256(wav(n)).hexdigest() for n in (1, 2))
        self.assertEqual(data, {"references": [second_id, first_id, second_id], "gapMs": 33, "mode": "both"})
        self.assertEqual(sum(c[1] == "/api/reference" for c in self.app.calls), 2)
        path = Path(item["path"])
        path.write_bytes(b"keep existing")
        code, result = self.invoke(*args)
        self.assertEqual((code, result["error"]["code"]), (1, "output_exists"))
        self.assertEqual(path.read_bytes(), b"keep existing")
        self.assertFalse(list(self.output.glob("*.part")))

    def test_dataset_exports_zip_without_training(self):
        code, result = self.invoke("dataset", "--reference", str(self.fixture), "--language", "yue",
                                   "--name", "explicit training input", "--denoise", "--output-dir", str(self.output))
        self.assertEqual(code, 0, result)
        item = result["artifacts"][0]
        self.assertEqual(item["type"], "application/zip")
        with zipfile.ZipFile(item["path"]) as archive:
            self.assertEqual(archive.namelist(), ["review/fixture.txt"])
        self.assertFalse(any(c[1].startswith("/api/training") for c in self.app.calls))
        data = next(json.loads(c[3]) for c in self.app.calls if c[1] == "/api/datasets/auto")
        self.assertEqual(data, {"references": [hashlib.sha256(wav()).hexdigest()], "language": "yue",
                                "speaker": "explicit training input", "denoise": True})

    def test_truncated_media_does_not_publish_any_file(self):
        self.app.bad_media = True
        code, result = self.invoke("clean", "--reference", str(self.fixture), "--output-dir", str(self.output))
        self.assertEqual((code, result["error"]["code"]), (1, "invalid_artifact"))
        self.assertEqual(list(self.output.iterdir()), [])


class ValidationTests(unittest.TestCase):
    def test_network_addresses_and_paths_are_not_supported(self):
        for url in ("https://127.0.0.1:80", "http://localhost:552", "http://1.2.3.4:55",
                    "http://127.0.0.1", "http://127.0.0.1:9/p", "http://bad@127.0.0.1:55",
                    "http://127.0.0.1:55/a?secret", "http://[::1%a]:1234", "http://127.0.0.1:0/"):
            with self.subTest(url=url), self.assertRaises(agent.AgentError):
                agent.loopback_endpoint(url)
        self.assertEqual(agent.loopback_endpoint("http://127.0.0.1:2532/"), ("127.0.0.1", 2532, "http://127.0.0.1:2532"))
        self.assertEqual(agent.loopback_endpoint("http://[::1]:2532/")[0], "::1")
        with self.assertRaises(agent.AgentError):
            agent.explicit_path("//untrusted-network/f.wav")

    def test_symlink_input_and_output_ancestor_fail(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)
            real = path / "outside"
            real.mkdir()
            (real / "fixture.wav").write_bytes(wav())
            link = path / "by-link"
            try:
                link.symlink_to(real, target_is_directory=True)
            except OSError as exc:
                self.skipTest(f"not permitted to create a symlink: {exc}")
            with self.assertRaisesRegex(agent.AgentError, "contains a link"):
                agent.input_wavs([link / "fixture.wav"])
            with self.assertRaisesRegex(agent.AgentError, "contains a link"):
                agent.safe_output_directory(link / "new")
            self.assertFalse((real / "new").exists())

    def test_windows_reparse_attributes_are_never_accepted(self):
        fixture = SimpleNamespace(st_mode=0o040000, st_file_attributes=0x400)
        self.assertTrue(agent.is_link(fixture))


if __name__ == "__main__":
    unittest.main()
