"""Publish the readiness stamp only after real Windows CUDA worker tests."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import wave

from fetch_sts_weights import MODELS


REPOS = {
    "x-vc": ("x-vc", "49df8c591eafc48b096e466d96f9839f9c0dd739", "bins/infer_utils.py"),
    "seed-vc-tiny": ("seed-vc", "51383efd921027683c89e5348211d93ff12ac2a8", "inference.py"),
    "meanvc2": ("meanvc2", "13acf84c1bf135ea5edad9c245b345289b06b33e", "runtime/run_rt.py"),
    "tvtsyn1": ("tvtsyn", "9aa67c0272620caaf43170fe98ec48438e1624e0", "inference/tvtsyn_inference/api.py"),
}


def atomic_json(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(content, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("model", choices=REPOS)
    p.add_argument("assets", type=Path)
    p.add_argument("benchmark", type=Path)
    p.add_argument("--python", type=Path, default=Path(os.sys.executable))
    a = p.parse_args()
    tested = json.loads(a.benchmark.read_text(encoding="utf-8"))
    if len(tested) < 2:
        raise ValueError("Both cold and hot benchmark runs required")
    for result in tested:
        diag = result["diagnostics"]
        if (diag["engine"] != a.model or diag["platform"] != "win32"
                or any(diag["module_devices"].get(k) != ["cuda:0"] for k in ("converter", "decoder", "speaker"))):
            raise ValueError("Benchmark is not a successful Windows CUDA run for this model")
        with wave.open(result["output"], "rb") as f:
            if f.getframerate() != 24000 or not f.getnframes():
                raise ValueError("Missing or invalid benchmark WAV")
    repo, revision, entry = REPOS[a.model]
    relative = Path("upstream") / repo
    local = a.assets / relative
    actual = subprocess.check_output(["git", "-C", str(local), "rev-parse", "HEAD"], text=True).strip()
    diff = subprocess.check_output(["git", "-C", str(local), "status", "--porcelain", "--untracked-files=no"], text=True)
    if actual != revision or diff:
        raise ValueError("Clone does not match the pinned, unmodified upstream revision")
    files = [relative / entry]
    for group, (_, members) in MODELS[a.model].items():
        files.extend(Path("weights") / group.replace("/", "--") / f for f in members)
    if a.model == "meanvc2":
        files.extend(Path("weights/meanvc2-extra") / name for name in
                     ("wavlm_large_finetune.pth", "wavlm_large_cfg.pt"))
    if a.model == "x-vc":
        files.extend(Path("weights/eres2net") / x for x in ("pretrained_eres2net.ckpt", "configuration.json"))
    for f in files:
        if (a.assets / f).stat().st_size <= 0:
            raise ValueError(f"Empty artifact: {f}")
    py = a.python.resolve(strict=True)
    if py.name.lower() != "python.exe":
        raise ValueError("Windows python.exe required")
    atomic_json(a.assets / "runtime.json", {"python": str(py)})
    atomic_json(a.assets / "verified" / (a.model + ".json"),
                {"model": a.model, "revision": revision, "files": [f.as_posix() for f in files]})
    print("REGISTERED", a.model)


if __name__ == "__main__":
    main()
