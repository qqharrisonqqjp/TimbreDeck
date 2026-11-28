"""Normalize an untrusted model archive with the restricted PyTorch loader."""

from collections import OrderedDict
from io import BytesIO
import re
from pathlib import Path
import sys
import zipfile

try:
    import torch
except ImportError:
    raise SystemExit(78)

release = re.match(r"(\d+)\.(\d+)", torch.__version__)
if not release or tuple(map(int, release.groups())) < (2, 6):
    raise SystemExit(78)


class ConvertedConfig:
    """Inert placeholder for the pinned upstream config class."""


def sanitized_config(x, depth=0):
    if depth > 12:
        raise ValueError("configuration is too deeply nested")
    if type(x) is ConvertedConfig:
        x = vars(x)
    if type(x) in (dict, OrderedDict):
        if len(x) > 1024 or any(type(k) is not str or len(k) > 256 for k in x):
            raise ValueError("configuration contains invalid keys")
        return {k: sanitized_config(v, depth + 1) for k, v in x.items()}
    if type(x) in (tuple, list):
        if len(x) > 1024:
            raise ValueError("configuration contains too many values")
        return [sanitized_config(v, depth + 1) for v in x]
    if type(x) is str and len(x) <= 32768 or type(x) in (bool, int, float, type(None)):
        return x
    raise ValueError("configuration is not plain data")


def validate_archive(f):
    if f.read(4) != b"PK\x03\x04":
        raise ValueError("expected modern PyTorch ZIP serialization")
    f.seek(0)
    with zipfile.ZipFile(f) as a:
        info = a.infolist()
        names = {e.filename for e in info}
        if (not info or len(info) != len(names) or len(info) > 20000
                or sum(e.file_size for e in info) > (1536 << 20)
                or sum(e.filename.endswith("/data.pkl") for e in info) != 1
                or any("/code/" in n or n.endswith(("/constants.pkl", "/traced_inputs.pkl")) for n in names)):
            raise ValueError("invalid tensor-only PyTorch archive")
    f.seek(0)


def clean(source, target, sovits=False):
    with Path(source).open("rb") as src:
        if sovits:
            if src.read(2) != b"05":
                raise ValueError("SoVITS checkpoint is not v2Pro")
            f = BytesIO(b"PK" + src.read())
        else:
            f = src
        validate_archive(f)
        with torch.serialization.safe_globals([
            (ConvertedConfig, "utils.HParams"),
            (ConvertedConfig, "GPT_SoVITS.utils.HParams"),
        ]):
            checkpoint = torch.load(f, map_location="cpu", weights_only=True)
        if sovits:
            f.close()
    if type(checkpoint) not in (dict, OrderedDict):
        raise ValueError("not a compatible checkpoint")
    weights = checkpoint.get("weight")
    config = sanitized_config(checkpoint.get("config"))
    if (type(weights) not in (dict, OrderedDict) or not weights or len(weights) > 20000
            or any(type(k) is not str or len(k) > 512 or type(v) is not torch.Tensor
                   or v.layout != torch.strided or v.ndim > 8 for k, v in weights.items())
            or not isinstance(config, dict) or not all(isinstance(config.get(k), dict) for k in ("model", "data", "train"))):
        raise ValueError("checkpoint is missing the model's tensor state or configuration")
    if sovits and config["model"].get("version") != "v2Pro":
        raise ValueError("SoVITS configuration is not v2Pro")
    # Serialize just the fields both upstream inference loaders consume. No
    # pickle globals from the supplied checkpoint pass through to that runtime.
    with Path(target).open("w+b") as out:
        torch.save({"weight": dict(weights), "config": config}, out, pickle_protocol=2)
        if sovits:
            out.seek(0)
            out.write(b"05")


try:
    clean(sys.argv[1], sys.argv[3])
    clean(sys.argv[2], sys.argv[4], sovits=True)
except (ValueError, OSError, RuntimeError, EOFError):
    print("Invalid or incompatible checkpoint.", file=sys.stderr)
    raise SystemExit(1)
