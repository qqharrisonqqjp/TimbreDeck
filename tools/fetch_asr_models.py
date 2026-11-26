"""Fetch named CTranslate2 models into an existing local ASR assets folder."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import urllib.request

MODELS = {
    "faster-whisper-large-v3": "Systran/faster-whisper-large-v3",
    "faster-whisper-large-v3-turbo": "dropbox-dash/faster-whisper-large-v3-turbo",
    "qwen3-asr-0.6b": "Qwen/Qwen3-ASR-0.6B",
    "qwen3-asr-1.7b": "Qwen/Qwen3-ASR-1.7B",
    "fun-asr-nano": "csukuangfj/sherpa-onnx-funasr-nano-int8-2025-12-30",
    "fun-asr-mlt-nano": "luigi000/fun-asr-mlt-nano-onnx-int8-1024",
}
REQUIRED = {"model.bin", "config.json", "tokenizer.json"}
ALLOWED = REQUIRED | {"preprocessor_config.json", "vocabulary.json", "vocabulary.txt"}

def read(url):
    with urllib.request.urlopen(url, timeout=60) as f:
        return json.load(f)

def install(base, name):
    repo = MODELS[name]
    prefix = "https://huggingface.co"
    sha = read(f"{prefix}/api/models/{repo}")["sha"]
    if not re.fullmatch(r"[0-9a-f]{40}",sha):
        raise ValueError("Missing immutable model revision")
    qwen = name.startswith("qwen3-asr-")
    nano = name.startswith("fun-asr-")
    nanoFiles={"encoder_adaptor.int8.onnx","llm.int8.onnx","embedding.int8.onnx",
        "Qwen3-0.6B/tokenizer.json","Qwen3-0.6B/tokenizer_config.json","Qwen3-0.6B/merges.txt","Qwen3-0.6B/vocab.json","Qwen3-0.6B/config.json"}
    if name=="fun-asr-mlt-nano":
        nanoFiles={p.replace("Qwen3-0.6B/","tokenizer/") for p in nanoFiles}|{"LICENSE","NOTICE"}
    allowed = {"config.json","tokenizer.json","preprocessor_config.json","tokenizer_config.json","generation_config.json",
        "vocab.json","merges.txt","model.safetensors.index.json","chat_template.json","chat_template.jinja","added_tokens.json","special_tokens_map.json","LICENSE"} if qwen else ALLOWED
    suffix="?recursive=true" if nano else ""
    files = {x["path"]:x for x in read(f"{prefix}/api/models/{repo}/tree/{sha}{suffix}")
             if (x["path"] in nanoFiles if nano else x["path"] in allowed or qwen and re.fullmatch(r"model(?:-[0-9]+-of-[0-9]+)?\.safetensors",x["path"]))}
    if (nano and not {"encoder_adaptor.int8.onnx","llm.int8.onnx","embedding.int8.onnx",
        "tokenizer/tokenizer.json" if name=="fun-asr-mlt-nano" else "Qwen3-0.6B/tokenizer.json"}<=files.keys()) or (not nano and not qwen and not REQUIRED <= files.keys()) or (qwen and ("config.json" not in files or not any(x.endswith(".safetensors") for x in files))):
        raise ValueError("Incomplete upstream export")
    pending = base / ("." + name + "-" + sha[:8])
    pending.mkdir(exist_ok=True)
    manifest = {}
    for filename, item in sorted(files.items()):
        local = filename.replace("tokenizer/","Qwen3-0.6B/") if name=="fun-asr-mlt-nano" else filename
        dest = pending / local
        dest.parent.mkdir(parents=True,exist_ok=True)
        h=hashlib.sha256(); n=0
        with urllib.request.urlopen(f"{prefix}/{repo}/resolve/{sha}/{filename}",timeout=120) as response, dest.open("wb") as output:
            while (b:=response.read(1<<20)):
                n+=len(b)
                if n>item["size"]:
                    raise ValueError(f"Exceeded published size: {filename}")
                h.update(b);output.write(b)
        if n!=item["size"] or (item.get("lfs") and h.hexdigest()!=item["lfs"]["oid"]):
            raise ValueError(f"Upstream size/SHA mismatch: {filename}")
        if not item.get("lfs"):
            with dest.open("rb") as f:
                git=hashlib.sha1(f"blob {n}\0".encode()+f.read()).hexdigest()
            if git!=item["oid"]:
                raise ValueError(f"Git blob mismatch: {filename}")
        manifest[local]={"sha256":h.hexdigest(),"bytes":n}
        print(name,filename,n,flush=True)
    (pending/"timbredeck-asr.json").write_text(json.dumps({"source":repo,"revision":sha,"files":manifest},indent=2),"utf8")
    if (base/name).exists():
        raise FileExistsError(base/name)
    pending.rename(base/name)

if __name__=="__main__":
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument("assets",type=Path)
    ap.add_argument("model",choices=MODELS,nargs="+")
    args=ap.parse_args()
    for model in args.model:
        if not (args.assets/model/"timbredeck-asr.json").is_file():
            install(args.assets,model)
