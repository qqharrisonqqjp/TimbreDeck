"""Compare input and converted transcripts with one offline ASR per language."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import sys
import unicodedata


def distance(a, b):
    row = list(range(len(b) + 1))
    for i, c in enumerate(a, 1):
        nxt = [i]
        for j, d in enumerate(b, 1):
            nxt.append(min(nxt[-1] + 1, row[j] + 1, row[j - 1] + (c != d)))
        row = nxt
    return row[-1]


def units(text, lang):
    text = unicodedata.normalize("NFKC", text).lower()
    return re.findall(r"[a-z0-9]+(?:'[a-z]+)?", text) if lang == "en" else [
        c for c in text if c.isalnum()]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("asr_assets", type=Path)
    p.add_argument("benchmarks", type=Path)
    a = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    spec = importlib.util.spec_from_file_location("studio_asr", root / "studio/asr_worker.py")
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    sys.path.insert(0, str(a.asr_assets / "asr-python"))
    rows = {}
    models = {p.parent.name: json.loads(p.read_text("utf-8"))
              for p in a.benchmarks.glob("*/benchmark.json")}
    for lang, index in (("en", 1), ("yue", 3)):
        paths = {name: m[index]["output"] for name, m in models.items() if len(m) > index}
        wav = next(m[index]["input"] for m in models.values() if len(m) > index)
        recognize = worker.sensevoice(a.asr_assets, lang)
        anchor, _ = recognize(worker.decode_wav(wav))
        target = units(anchor, lang)
        outputs = {}
        for name, path in paths.items():
            text, _ = recognize(worker.decode_wav(path))
            u = units(text, lang)
            outputs[name] = dict(transcript=text, wav=path, distance=distance(target, u),
                                 rate=distance(target, u) / len(target) if target else None)
        rows[lang] = dict(provider="SenseVoice-Small int8 CPU", input=wav,
                          input_transcript=anchor, units=len(target), outputs=outputs)
    path = a.benchmarks / "transcript-check.json"
    path.write_text(json.dumps(rows, ensure_ascii=False, indent=2) + "\n", "utf-8")
    print(path)


if __name__ == "__main__":
    main()
