"""Package a tested portable engine directory as GitHub Release assets."""
import argparse
import hashlib
import json
import re
import tempfile
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", choices=("gpt-sovits", "cosyvoice3"))
    parser.add_argument("version")
    parser.add_argument("root", type=Path)
    parser.add_argument("out", type=Path)
    args = parser.parse_args()
    root, out = args.root.resolve(), args.out.resolve()
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}", args.version):
        parser.error("invalid version")
    if not (root / "runtime/python.exe").is_file() or out.is_relative_to(root):
        parser.error("supply a portable engine root and a separate output directory")
    out.mkdir(parents=True, exist_ok=True)
    base = f"{args.engine}-{args.version}.zip"
    with tempfile.TemporaryDirectory(dir=out) as work:
        stage = Path(work)
        archive = stage / base
        with ZipFile(archive, "w", compression=ZIP_DEFLATED, compresslevel=1) as z:
            for file in sorted(root.rglob("*")):
                if file.is_symlink():
                    raise ValueError(f"symlink in portable pack: {file}")
                if file.is_file():
                    z.write(file, file.relative_to(root).as_posix())
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
        info = stage / f"{args.engine}.json"
        info.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        for part in parts:
            (stage / part["name"]).replace(out / part["name"])
        info.replace(out / info.name)


if __name__ == "__main__":
    main()
