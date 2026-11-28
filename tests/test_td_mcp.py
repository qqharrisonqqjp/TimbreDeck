"""Exercise the local MCP server over real newline-framed stdio."""

import base64
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import wave


REPO = Path(__file__).resolve().parents[1]
FIXTURE = r'''
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys
from zipfile import ZipFile

parser = argparse.ArgumentParser()
parser.add_argument("--url")
parser.add_argument("--timeout", type=int, required=True)
subs = parser.add_subparsers(dest="action", required=True)
subs.add_parser("capabilities")
subs.add_parser("status")
subs.add_parser("voices")
subs.add_parser("resident-status")
warm = subs.add_parser("warm-model")
warm.add_argument("--engine", required=True)
warm.add_argument("--kind", required=True)
warm.add_argument("--variant")
stop = subs.add_parser("stop-model")
stop.add_argument("--resident-id", required=True)
asr = subs.add_parser("asr")
asr.add_argument("--reference", type=Path, required=True)
asr.add_argument("--language", default="auto")
synth = subs.add_parser("synthesize")
synth.add_argument("--engine", required=True)
synth.add_argument("--text", required=True)
synth.add_argument("--preview", action="store_true")
synth.add_argument("--output-dir", type=Path, required=True)
for action in ("clean", "dataset"):
    sub = subs.add_parser(action)
    sub.add_argument("--reference", action="append", type=Path, required=True)
    sub.add_argument("--output-dir", type=Path, required=True)
    if action == "clean":
        sub.add_argument("--mode", required=True)
        sub.add_argument("--gap-ms", type=int)
    else:
        sub.add_argument("--language", required=True)
        sub.add_argument("--speaker")
        sub.add_argument("--denoise", action="store_true")
        sub.add_argument("--clean-mode")
        sub.add_argument("--no-split", action="store_true")
        sub.add_argument("--no-transcribe", action="store_true")
        sub.add_argument("--skip-unrecognized", action="store_true")
args = parser.parse_args()
assert args.url in (None, "http://127.0.0.1:49125") and args.timeout == 5
result = {"ok": True, "action": args.action}
if args.action in ("synthesize", "clean", "dataset"):
    if args.action == "synthesize" and args.text == "oversized":
        print("x" * 100000)
        raise SystemExit()
    if args.action == "synthesize" and args.text == "fail":
        print(json.dumps({"ok": False, "error": {"code": "fixture", "message": "Expected failure"}}))
        raise SystemExit(1)
    if args.action == "dataset":
        result.update(language=args.language, speaker=args.speaker, denoise=args.denoise,
                      cleanMode=args.clean_mode,noSplit=args.no_split,noTranscribe=args.no_transcribe,
                      skipUnrecognized=args.skip_unrecognized,
                      filenames=[p.name for p in args.reference])
        path = args.output_dir / "匯出 archive.zip"
        mime = "application/zip"
        with ZipFile(path, "w") as z:
            z.write(args.reference[0], "full.wav")
    else:
        path = args.output_dir / "本地 result.wav"
        mime = "audio/wav"
        if args.action == "clean":
            shutil.copyfile(args.reference[0], path)
            result["gapMs"] = args.gap_ms
            result["mode"] = args.mode
        else:
            if args.text == "outside":
                path = args.output_dir.parent / "outside.wav"
            elif args.text in ("large", "stale"):
                path = args.output_dir / (args.text + ".wav")
            if args.text == "large":
                with path.open("wb") as f:
                    f.seek(8 * 1024 * 1024)
                    f.write(b"x")
            else:
                path.write_bytes(b"RIFF".ljust(44))
            result.update(text=args.text, preview=args.preview)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    result["artifacts"] = [{"path": str(path), "type": mime, "bytes": path.stat().st_size, "sha256": digest}]
    if args.action == "synthesize" and args.text == "stale":
        path.write_bytes(b"changed".ljust(44))
elif args.action == "asr":
    result.update(text="本機逐字稿", provider="fixture", language=args.language,
                  filename=args.reference.name)
elif args.action == "status":
    result["explicitUrl"] = args.url
elif args.action == "warm-model":
    result.update(engine=args.engine, kind=args.kind, variant=args.variant)
elif args.action == "stop-model":
    result["residentId"] = args.resident_id
print("fixture diagnostic", file=sys.stderr)
print(json.dumps(result, ensure_ascii=False))
'''


def rpc(method, ident=None, params=None):
    result = {"jsonrpc": "2.0", "method": method}
    if ident is not None:
        result["id"] = ident
    if params is not None:
        result["params"] = params
    return result


def init(version):
    return [
        rpc("initialize", 1, {"protocolVersion": version, "capabilities": {},
                              "clientInfo": {"name": "unittest", "version": "1"}}),
        rpc("notifications/initialized"),
    ]


def call(tool, ident, **kwargs):
    return rpc("tools/call", ident, {"name": "timbredeck_" + tool, "arguments": kwargs})


class TestMCP(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve() / "本機測試"
        self.root.mkdir()
        self.input = self.root / "in files"
        self.output = self.root / "out files"
        self.input.mkdir()
        self.output.mkdir()
        for path in [self.input / "一.wav", self.input / "second.wav", self.root / "outside.wav"]:
            with wave.open(str(path), "wb") as audio:
                audio.setparams((1, 2, 16000, 0, "NONE", "NONE"))
                audio.writeframes(b"\x00\x10" * 200)
        server = self.root / "td_mcp.py"
        shutil.copyfile(REPO / "tools/td_mcp.py", server)
        (self.root / "td_agent.py").write_text(FIXTURE, encoding="utf-8")
        self.command = [sys.executable, "-B", "-X", "utf8", str(server), "--url", "http://127.0.0.1:49125",
                        "--input-root", str(self.input), "--output-dir", str(self.output), "--timeout", "5"]

    def wire(self, *messages):
        data = b""
        for message in messages:
            data += (message if isinstance(message, bytes) else json.dumps(message).encode("utf-8")) + b"\n"
        result = subprocess.run(self.command, input=data, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8", "replace"))
        self.assertEqual(result.stderr, b"")
        responses = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertTrue(all(r["jsonrpc"] == "2.0" for r in responses))
        return {r["id"]: r for r in responses}

    def test_initialize_list_and_actions(self):
        result = self.wire(
            *init("2025-11-25"), rpc("notifications/ignored"), rpc("tools/list", 2), rpc("ping", 3),
            call("capabilities", 4), call("status", 5), call("asr", 6, reference="一.wav", language="yue"),
            call("synthesize", 7, engine="gpt-sovits", text="--language=en", preview=True),
            call("clean", 8, references=["一.wav", "second.wav"], mode="mossformer", gap_ms=0),
            call("dataset", 9, references=["second.wav", "一.wav"], language="en", speaker="Demo",
                 clean_mode="lavasr-denoise",no_split=True,no_transcribe=True,skip_unrecognized=True),
            call("warm_model", 10, engine="gpt-sovits", kind="tts", variant="v5turbo"),
            call("stop_model", 11, resident_id="a"*32), call("resident_status",12),call("voices",13),
        )
        self.assertEqual(set(result), set(range(1, 14)))
        self.assertEqual(result[1]["result"]["protocolVersion"], "2025-11-25")
        tools = result[2]["result"]["tools"]
        self.assertEqual({t["name"] for t in tools}, {"timbredeck_" + name for name in
                         ("capabilities", "status", "asr", "synthesize", "clean", "dataset", "sts",
                          "warm_model","stop_model","resident_status","voices")})
        self.assertTrue(all(t["inputSchema"]["additionalProperties"] is False for t in tools))
        self.assertEqual(result[3]["result"], {})
        self.assertEqual(result[6]["result"]["structuredContent"]["text"], "本機逐字稿")
        self.assertEqual(result[7]["result"]["structuredContent"]["text"], "--language=en")
        self.assertTrue(result[7]["result"]["structuredContent"]["preview"])
        self.assertEqual(result[8]["result"]["structuredContent"]["gapMs"], 0)
        self.assertEqual(result[8]["result"]["structuredContent"]["mode"], "mossformer")
        dataset=result[9]["result"]["structuredContent"]
        self.assertEqual([dataset[k] for k in ("cleanMode","noSplit","noTranscribe","skipUnrecognized")],
                         ["lavasr-denoise",True,True,True])
        self.assertEqual(result[10]["result"]["structuredContent"]["variant"],"v5turbo")
        self.assertEqual(result[11]["result"]["structuredContent"]["residentId"],"a"*32)
        self.assertEqual(result[12]["result"]["structuredContent"]["action"],"resident-status")
        self.assertEqual(result[9]["result"]["structuredContent"]["filenames"], ["second.wav", "一.wav"])
        for ident, suffix in ((7, "wav"), (8, "wav"), (9, "zip")):
            value = result[ident]["result"]
            self.assertFalse(value["isError"])
            artifact = value["structuredContent"]["artifacts"][0]
            self.assertTrue(Path(artifact["path"]).is_relative_to(self.output))
            self.assertEqual(artifact["uri"], Path(artifact["path"]).as_uri())
            self.assertEqual(value["content"][1]["type"], "resource_link")
            self.assertTrue(value["content"][1]["uri"].endswith("." + suffix))
            self.assertNotIn("data", value["content"][1])

    def test_allowlist_paths_and_bounded_error_output(self):
        result = self.wire(
            rpc("tools/list", 0), *init("2025-06-18"),
            call("run", 2, command="cmd.exe"),
            call("asr", 3, reference="../outside.wav"),
            call("asr", 4, reference=str(self.root / "outside.wav")),
            call("asr", 5, reference="一.wav", url="https://example.org"),
            call("clean", 6, references=["一.wav"], mode="both", output_dir=str(self.root)),
            call("clean", 7, references=["一.wav"], mode="both", gap_ms=True),
            call("synthesize", 8, engine="gpt-sovits", text="outside"),
            call("synthesize", 9, engine="gpt-sovits", text="oversized"),
            call("synthesize", 10, engine="gpt-sovits", text="fail"),
            rpc("resources/read", 11, {"uri": (self.root / "outside.wav").as_uri()}),
            b"{" + b" " * (132 * 1024), rpc("ping", 12),
        )
        self.assertEqual(result[0]["error"]["code"], -32002)
        self.assertEqual(result[2]["error"]["code"], -32602)
        for ident in range(3, 11):
            value = result[ident]["result"]
            self.assertTrue(value["isError"])
            self.assertEqual(len(value["content"]), 1)
        self.assertIn("output", result[9]["result"]["structuredContent"]["error"]["message"])
        self.assertEqual(result[10]["result"]["structuredContent"]["error"]["code"], "fixture")
        self.assertEqual(result[11]["error"]["code"], -32002)
        self.assertEqual(result[None]["error"]["code"], -32700)
        self.assertEqual(result[12]["result"], {})

    def test_older_clients_receive_local_file_metadata_as_text(self):
        result = self.wire(*init("2024-11-05"),
                           call("clean", 2, references=["一.wav"], mode="denoise", gap_ms=50))
        self.assertEqual(result[1]["result"]["protocolVersion"], "2024-11-05")
        value = result[2]["result"]
        self.assertNotIn("structuredContent", value)
        self.assertEqual(len(value["content"]), 1)
        self.assertEqual(json.loads(value["content"][0]["text"])["artifacts"][0]["type"], "audio/wav")

    def test_explicit_local_configuration(self):
        command = self.command.copy()
        command[command.index("--url") + 1] = "https://example.org"
        result = subprocess.run(command, input=b"", capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, b"")
        self.assertIn(b"expected http://127.0.0.1:<port>", result.stderr)
        del self.command[self.command.index("--url"):self.command.index("--url") + 2]
        result = self.wire(*init("2025-11-25"), call("status", 2))
        self.assertIsNone(result[2]["result"]["structuredContent"]["explicitUrl"])

    def test_read_registered_artifacts_and_recheck_hash(self):
        wav = (self.output / "本地 result.wav").as_uri()
        archive = (self.output / "匯出 archive.zip").as_uri()
        stale = (self.output / "stale.wav").as_uri()
        large = (self.output / "large.wav").as_uri()
        hidden = self.output / "unregistered.wav"
        hidden.write_bytes(b"private")
        result = self.wire(
            *init("2025-11-25"), rpc("resources/list", 2),
            call("clean", 3, references=["一.wav"], mode="denoise", gap_ms=100),
            call("dataset", 4, references=["一.wav"], language="en"),
            rpc("resources/list", 5), rpc("resources/templates/list", 6),
            rpc("resources/read", 7, {"uri": wav}), rpc("resources/read", 8, {"uri": archive}),
            rpc("resources/read", 9, {"uri": hidden.as_uri()}),
            call("synthesize", 10, engine="gpt-sovits", text="stale"),
            rpc("resources/read", 11, {"uri": stale}),
            call("synthesize", 12, engine="gpt-sovits", text="large"),
            rpc("resources/read", 13, {"uri": large}),
        )
        self.assertTrue("resources" in result[1]["result"]["capabilities"])
        self.assertEqual(result[2]["result"]["resources"], [])
        self.assertEqual([r["uri"] for r in result[5]["result"]["resources"]], [wav, archive])
        self.assertEqual(result[6]["result"]["resourceTemplates"], [])
        for ident, uri, name, media in [(7, wav, "本地 result.wav", "audio/wav"),
                                        (8, archive, "匯出 archive.zip", "application/zip")]:
            content = result[ident]["result"]["contents"][0]
            self.assertEqual(content["uri"], uri)
            self.assertEqual(content["mimeType"], media)
            self.assertEqual(base64.b64decode(content["blob"], validate=True), (self.output / name).read_bytes())
        self.assertEqual(result[9]["error"]["code"], -32002)
        self.assertEqual(result[11]["error"]["code"], -32002)
        self.assertIn("8 MiB", result[13]["error"]["message"])
        next_session = self.wire(*init("2025-11-25"), rpc("resources/read", 2, {"uri": wav}))
        self.assertEqual(next_session[2]["error"]["code"], -32002)


if __name__ == "__main__":
    unittest.main()
