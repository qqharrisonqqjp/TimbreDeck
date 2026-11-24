"""Package a tested engine, model, or complete bundle as GitHub Release assets."""
import argparse
import hashlib
import json
import re
import tempfile
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile


MODEL_FOLDERS = {
    "gpt-sovits": {"v2Pro": "GPT_SoVITS/pretrained_models"},
    "cosyvoice3": {"Fun-CosyVoice3-0.5B": "pretrained_models/Fun-CosyVoice3-0.5B"},
    "voxcpm2": {"VoxCPM2": "pretrained_models/VoxCPM2"},
    "indextts2": {"IndexTTS-2": "checkpoints"},
    "qwen3-tts": {name: f"pretrained_models/Qwen3-TTS-12Hz-1.7B-{name}"
                  for name in ("Base", "CustomVoice", "VoiceDesign")},
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", choices=MODEL_FOLDERS)
    parser.add_argument("version")
    parser.add_argument("root", type=Path)
    parser.add_argument("out", type=Path)
    parser.add_argument("--kind", choices=("bundle", "runtime", "model"), default="bundle")
    parser.add_argument("--variant", help="required for a model-only pack")
    args = parser.parse_args()
    root, out = args.root.resolve(), args.out.resolve()
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}", args.version):
        parser.error("invalid version")
    folders = {variant: Path(folder) for variant, folder in MODEL_FOLDERS[args.engine].items()}
    if out.is_relative_to(root):
        parser.error("supply a separate output directory")
    if args.kind == "model":
        if args.variant not in folders or not (root / folders[args.variant]).is_dir():
            parser.error("supply a valid model variant and its model directory within the root")
    elif args.variant or not (root / "runtime/python.exe").is_file():
        parser.error("supply a portable engine root with runtime/python.exe")
    asset = args.engine
    if args.kind == "model":
        asset += f"-model-{args.variant}"
    elif args.kind == "runtime":
        asset += "-runtime"
    out.mkdir(parents=True, exist_ok=True)
    base = f"{asset}-{args.version}.zip"
    with tempfile.TemporaryDirectory(dir=out) as work:
        stage = Path(work)
        archive = stage / base
        with ZipFile(archive, "w", compression=ZIP_DEFLATED, compresslevel=1) as z:
            for file in sorted(root.rglob("*")):
                relative = file.relative_to(root)
                if args.kind == "model" and not relative.is_relative_to(folders[args.variant]):
                    continue
                if args.kind == "runtime" and any(relative.is_relative_to(folder) for folder in folders.values()):
                    continue
                if file.is_symlink():
                    raise ValueError(f"symlink in portable pack: {file}")
                if file.is_file():
                    z.write(file, relative.as_posix())
            size = sum(entry.file_size for entry in z.infolist())
        parts, whole = [], hashlib.sha256()
        with archive.open("rb") as src:
            while data := src.read(1024 * 1024):
                path = stage / f"{base}.{len(parts) + 1:03d}"
                digest, length = hashlib.sha256(), 0
                with path.open("wb") as dest:
                    while data:
                        dest.write(data)
                        whole.update(data)
                        digest.update(data)
                        length += len(data)
                        remaining = 1_800_000_000 - length
                        if not remaining:
                            break
                        data = src.read(min(1024 * 1024, remaining))
                parts.append(dict(name=path.name, bytes=length, sha256=digest.hexdigest()))
        manifest = dict(engine=args.engine, version=args.version, expanded=size, sha256=whole.hexdigest(), parts=parts)
        if args.kind != "bundle":
            manifest["kind"] = args.kind
        if args.kind == "model":
            manifest["variant"] = args.variant
        if size <= 0 or size > 250_000_000_000 or len(parts) > 100:
            parser.error("the archive exceeds the launcher's size or part limits")
        info = stage / f"{asset}.json"
        info.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        for part in parts:
            if (out / part["name"]).exists():
                parser.error(f"{part['name']} already exists; use a new version or output directory")
        for part in parts:
            (stage / part["name"]).replace(out / part["name"])
        info.replace(out / info.name)


if __name__ == "__main__":
    main()
