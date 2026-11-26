"""Acceptance matrix contract tests; no model, network, real launcher, or GPU required."""
from array import array
from contextlib import contextmanager, redirect_stdout, redirect_stderr
from io import BytesIO, StringIO
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
from urllib.error import HTTPError
import wave

import acceptance_matrix as matrix


KEY = "17" * 16
OTHER_KEY = "aa" * 16
REF_ID = "55" * 32
EMO_ID = "66" * 32
CUDA_DEVICE = "cuda:0 · Unit-test GPU (not real evidence)"


def args():
    return SimpleNamespace(text=" Hello. ", transcript="   Original words. ", language="yue",
                           ref_lang="en", qwen_language="zh", timeout=10, require_cuda=True)


def wav_bytes(values=(0, 12, -99, 5), channels=1):
    out = BytesIO()
    with wave.open(out, "wb") as wav:
        wav.setnchannels(channels)
        wav.setsampwidth(2)
        wav.setframerate(48000)
        wav.writeframes(array("h", values).tobytes())
    return out.getvalue()


def evidence(engine, mode, key=KEY):
    return {
        "job": key, "engine": engine, "mode": mode, "model": f"fixture: {engine}",
        "platform": "win32", "torch_version": "2.8+cu128", "cuda_version": "12.8",
        "waveform_devices": ["cuda:0"],
        "module_devices": {m: ["cuda:0"] for m in matrix.CUDA_MODULES[engine]},
        "cuda_devices": {"cuda:0": {"name": CUDA_DEVICE.split(" · ")[1]}},
    }


def job(engine, status="running", key=KEY):
    return dict(id=key, engine=engine, kind="synthesis", status=status)


def mocked_catalog():
    state = {"engines": {}}
    for engine, modes in matrix.ENGINES.items():
        state["engines"][engine] = {"ready": True, "engineInstalled": True,
            "availableVariants": list(dict.fromkeys(modes.values())), "modes": [
                {"id": mode, "variant": variant if engine == "qwen3-tts" else "",
                 "reference": (engine, mode) not in matrix.WITHOUT_REFERENCE,
                 "transcript": (engine, mode) in matrix.TRANSCRIPT,
                 "instruction": (engine, mode) in matrix.INSTRUCTIONS,
                 "languages": engine in ("gpt-sovits", "qwen3-tts"),
                 "speaker": (engine, mode) == ("qwen3-tts", "preset"),
                 "emotion": mode.removeprefix("emotion-") if mode.startswith("emotion-") else ""}
                for mode, variant in modes.items()
            ]}
    return state


class MatrixTests(unittest.TestCase):
    def test_select_all_five_clones_and_qualified_modes(self):
        all_cases = matrix.select_cases()
        self.assertEqual(len(all_cases), 15)
        self.assertEqual(len(set(all_cases)), 15)
        self.assertEqual(matrix.select_cases(modes=["clone"]), [(e, "clone") for e in matrix.ENGINES])
        self.assertEqual(matrix.select_cases(["qwen3-tts", "voxcpm2"], ["design", "qwen3-tts:simple-clone", "design"]), [
            ("qwen3-tts", "simple-clone"), ("qwen3-tts", "design"), ("voxcpm2", "design")])
        for chosen, modes in [(["gpt-sovits"], ["design"]), (None, ["all", "clone"]),
                              (["cosyvoice3"], ["voxcpm2:design"]), (None, ["fish"]) ]:
            with self.subTest(engines=chosen, modes=modes), self.assertRaises(ValueError):
                matrix.select_cases(chosen, modes)

    def test_mode_specific_payloads(self):
        def request(e, m):
            return matrix.payload(args(), e, m, REF_ID, EMO_ID)

        self.assertEqual(request("gpt-sovits", "clone"), {
            "engine": "gpt-sovits", "mode": "clone", "variant": "v2Pro", "text": "Hello.", "speed": 1,
            "preview": False, "reference": REF_ID, "transcript": "Original words.", "language": "yue", "refLang": "en"})
        self.assertIn("transcript", request("cosyvoice3", "clone"))
        self.assertIn("instruction", request("cosyvoice3", "instruct"))
        self.assertTrue({"instruction", "transcript", "language"}.isdisjoint(request("cosyvoice3", "cross")))
        self.assertIn("transcript", request("voxcpm2", "continue"))
        self.assertNotIn("reference", request("voxcpm2", "design"))
        self.assertEqual(request("indextts2", "emotion-audio")["emotionReference"], EMO_ID)
        self.assertIn("instruction", request("indextts2", "emotion-text"))
        self.assertEqual(len(request("indextts2", "emotion-vector")["emotionWeights"]), 8)
        self.assertNotIn("emotionStrength", request("indextts2", "clone"))
        self.assertEqual(request("qwen3-tts", "clone")["language"], "zh")
        self.assertNotIn("transcript", request("qwen3-tts", "simple-clone"))
        self.assertEqual(request("qwen3-tts", "preset")["variant"], "CustomVoice")
        self.assertIn("speaker", request("qwen3-tts", "preset"))
        self.assertEqual(request("qwen3-tts", "design")["variant"], "VoiceDesign")

    def test_reject_noncanonical_loopback_and_redirect(self):
        self.assertEqual(matrix.validate_loopback("http://127.0.0.1:65535"), "http://127.0.0.1:65535")
        for url in ("https://127.0.0.1:1234", "http://localhost:1234", "http://127.0.0.2:1234",
                    "http://127.0.0.1:1234@other.test", "http://someone@127.0.0.1:1234",
                    "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:1234/"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                matrix.Loopback(url)
        self.assertIsNone(matrix.NoRedirect().redirect_request(None, None, 302, "", {}, "https://example.invalid"))
        api = matrix.Loopback("http://127.0.0.1:1234")
        api.opener = Mock()
        api.opener.open.side_effect = HTTPError(api.url, 302, "redirect", {}, BytesIO(b"moved"))
        with self.assertRaises(matrix.APIError) as cm:
            api("reference", b"WAV", expect=201)
        self.assertEqual(cm.exception.status, 302)
        api.opener.open.assert_called_once()
        req = api.opener.open.call_args.args[0]
        self.assertEqual(req.full_url, "http://127.0.0.1:1234/api/reference")
        self.assertEqual(req.get_header("Origin"), api.url)
        self.assertEqual(req.get_header("X-timbredeck"), "1")
        self.assertEqual(req.data, b"WAV")

    def test_reject_foreign_job_without_cancellation(self):
        api = Mock(return_value=job("voxcpm2", key=OTHER_KEY))
        with self.assertRaises(matrix.JobOwnershipError):
            matrix.wait_job(api, "voxcpm2", KEY, 10, interval=0)
        api.assert_called_once_with("job")

    def test_cancel_only_own_job_and_wait_for_terminal(self):
        api = Mock(side_effect=[job("voxcpm2"), job("voxcpm2", "cancelling"), job("voxcpm2", "cancelled")])
        with self.assertRaisesRegex(matrix.JobDeadline, "terminal state: cancelled"):
            matrix.wait_job(api, "voxcpm2", KEY, 0, interval=0)
        self.assertEqual([x.args[0] for x in api.call_args_list], ["job", "cancel/" + KEY, "job"])

    def test_cuda_is_bound_to_exact_job_and_each_module(self):
        for engine, mode in matrix.select_cases():
            x = evidence(engine, mode)
            self.assertEqual(matrix.verify_execution(x, engine, mode, KEY, True), CUDA_DEVICE)
            self.assertIsNone(matrix.verify_execution(x, engine, mode, KEY))
            with self.assertRaisesRegex(ValueError, "does not match"):
                matrix.verify_execution(x, engine, mode, OTHER_KEY, False)
            x["module_devices"][matrix.CUDA_MODULES[engine][0]] = ["cpu"]
            with self.assertRaisesRegex(ValueError, "does not share"):
                matrix.verify_execution(x, engine, mode, KEY, True)
        x = evidence("voxcpm2", "clone")
        x["waveform_devices"] = ["cpu"]
        with self.assertRaisesRegex(ValueError, "one CUDA waveform"):
            matrix.verify_execution(x, "voxcpm2", "clone", KEY, True)

    def test_wav_must_be_complete_nonconstant_mono_pcm(self):
        audio = wav_bytes()
        info = matrix.measure(audio)
        self.assertEqual((info["rate_hz"], info["frames"], info["channels"], info["bits"]), (48000, 4, 1, 16))
        self.assertEqual(info["sha256"], hashlib.sha256(audio).hexdigest())
        self.assertEqual(info["peak_pcm16"], 99)
        for bad in (audio[:-2], wav_bytes((0, 0, 0)), wav_bytes(channels=2)):
            with self.subTest(size=len(bad)), self.assertRaises(ValueError):
                matrix.measure(bad)

    def test_reject_unavailable_model_and_capability_drift(self):
        state = mocked_catalog()
        matrix.ready_mode(state, "qwen3-tts", "design", "VoiceDesign")
        state["engines"]["qwen3-tts"]["availableVariants"].remove("VoiceDesign")
        with self.assertRaisesRegex(ValueError, "not ready"):
            matrix.ready_mode(state, "qwen3-tts", "design", "VoiceDesign")
        state["engines"]["voxcpm2"]["modes"][1]["transcript"] = False
        with self.assertRaisesRegex(ValueError, "mode contract"):
            matrix.ready_mode(state, "voxcpm2", "continue", "VoxCPM2")

    def test_cli_matrix_all_modes_reports_and_preserves_fixtures(self):
        with tempfile.TemporaryDirectory() as raw_root:
            root = Path(raw_root)
            data, out = root / "data", root / "comparison"
            (data / "config").mkdir(parents=True)
            config = data / "config" / "engines.json"
            config.write_bytes(b"existing settings left alone")
            launcher = root / "Start.exe"
            launcher.touch()
            ref, emo = root / "ref.wav", root / "emo.wav"
            ref.write_bytes(wav_bytes((3, 4, 5, 6)))
            emo.write_bytes(wav_bytes((7, 8, 9, 10)))
            sample = wav_bytes()
            sample_info = matrix.measure(sample)
            state = mocked_catalog()
            past = []
            current = None
            history = []
            received = []

            def api(route, payload=None, expect=200, binary=False):
                nonlocal current
                if route == "state":
                    return state
                if route == "job":
                    return current
                if route == "reference":
                    self.assertEqual(expect, 201)
                    self.assertIn(payload, (ref.read_bytes(), emo.read_bytes()))
                    return {"id": hashlib.sha256(payload).hexdigest(), "seconds": 3.5}
                if route == "synthesize":
                    self.assertEqual(expect, 202)
                    received.append(payload)
                    engine, mode = payload["engine"], payload["mode"]
                    key = f"{len(past) + 1:032x}"
                    past.append(key)
                    folder = data / "outputs" / key
                    folder.mkdir(parents=True)
                    (folder / "speech.wav").write_bytes(sample)
                    x = evidence(engine, mode, key)
                    (folder / "execution.json").write_text(json.dumps(x), encoding="utf-8")
                    history.append({"id": key, "input": payload.copy(), "model": x["model"], "audioAvailable": True,
                                    "sampleRate": sample_info["rate_hz"], "seconds": sample_info["seconds"],
                                    "verifiedCuda": CUDA_DEVICE})
                    current = job(engine, "done", key)
                    return job(engine, key=key)
                if route.startswith("audio/"):
                    self.assertTrue(binary)
                    self.assertEqual(route, "audio/" + current["id"])
                    return sample
                if route == "history":
                    return history
                self.fail(f"unexpected API route: {route}")

            @contextmanager
            def fake_launch(cli_args, report_dir):
                self.assertEqual((cli_args.data, report_dir), (data, out))
                yield api

            argv = ["--launcher", str(launcher), "--data", str(data), "--output", str(out), "--reference", str(ref),
                    "--emotion-reference", str(emo), "--transcript", "Reference transcript", "--require-cuda"]
            with patch.object(matrix, "start_studio", fake_launch), redirect_stdout(StringIO()):
                self.assertEqual(matrix.main(argv), 0)
            self.assertEqual([(x["engine"], x["mode"]) for x in received], matrix.select_cases())
            report = json.loads((out / "comparison.json").read_text(encoding="utf-8"))
            self.assertEqual(len(report["results"]), 15)
            self.assertEqual(len(set(past)), 15)
            self.assertEqual(config.read_bytes(), b"existing settings left alone")
            self.assertFalse(report.get("run_error"))
            for row in report["results"]:
                self.assertEqual((row["status"], row["cuda_status"]), ("generated", "passed"))
                self.assertTrue(Path(row["execution"]).is_file())
                self.assertEqual(Path(row["wav"]).read_bytes(), sample)
            with redirect_stderr(StringIO()), self.assertRaises(SystemExit) as cm:
                matrix.main(argv)
            self.assertEqual(cm.exception.code, 2)


if __name__ == "__main__":
    unittest.main()

