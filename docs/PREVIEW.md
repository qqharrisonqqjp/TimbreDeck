# TimbreDeck 聲台 · Windows 預覽版

## 開始使用

1. 從 [Releases（發佈頁）](https://github.com/qqharrisonqqjp/TimbreDeck/releases) 下載 `TimbreDeck-Windows-x64.zip`，解壓到自己可寫入的資料夾。雙擊 `Start.exe`。啟動器會開啟預設瀏覽器。工作期間保留主控台，若瀏覽器未自動開啟，可複製主控台顯示的本機網址。
2. 在右上角選引擎，開啟「模型管理」。分別按「下載引擎」和「下載模型」，確認容量後安裝。若已有本機安裝包，可在同一頁離線匯入，或連接已有的引擎資料夾。發佈了相應檔案後，下載按鈕會顯示可用版本。
3. 回到「語音合成」並匯入參考 WAV。GPT-SoVITS 需要 3 至 10 秒的清晰錄音。錄音複製需填寫正確逐字稿。CosyVoice 的指令朗讀模式另有「講法指示」，例如「請用廣東話，節奏稍慢」。
4. 在「要讀出的文字」填寫新的台詞，選擇「試讀第一句」或「生成全文」。作業期間可以切換畫面，或按「取消作業」。在「輸出歷史」可預聽並匯出 WAV。每次合成使用獨立引擎程序，載入模型需時。完成或取消後會結束該程序。
5. 主控台按 Ctrl+C 結束。複製整個資料夾可帶走草稿、匯入錄音和輸出。外接的引擎若搬遷，請重新指定路徑。

| 引擎 | 預設資料夾（相對於 Start.exe） | 資料夾內容 |
|---|---|---|
| GPT-SoVITS | `runtimes/gpt-sovits/` | `runtime/python.exe` 與相容依賴、固定版本的 `GPT_SoVITS/` 及配套模型，包含 v2Pro 聲碼器、s1v3、中文語音與文字編碼器、說話者嵌入模型。 |
| CosyVoice 3 | `runtimes/cosyvoice3/` | `runtime/python.exe` 與相容依賴、固定版本的 `cosyvoice/`、`third_party/Matcha-TTS/`，以及 `pretrained_models/Fun-CosyVoice3-0.5B/` 下的完整 Fun-CosyVoice3-0.5B-2512 模型與配套資源。 |
| VoxCPM2 | `runtimes/voxcpm2/` | `runtime/python.exe`、固定版本的 `src/voxcpm/`，以及 `pretrained_models/VoxCPM2/` 下的設定、tokenizer、主模型和 AudioVAE。 |
| IndexTTS-2 | `runtimes/indextts2/` | `runtime/python.exe`、`indextts/`、`checkpoints/` 下的主模型及情緒權重。輔助模型使用 `checkpoints/hf_cache/`，保留完整快照及 refs。封包前將快照中的 Blob 連結實體化為檔案。 |
| Qwen3-TTS | `runtimes/qwen3-tts/` | `runtime/python.exe`、`qwen_tts/`、`pretrained_models/Qwen3-TTS-12Hz-1.7B-Base/`、`-CustomVoice/` 或 `-VoiceDesign/`。可只放所需型號，每個型號需附自己的 speech tokenizer。 |

在「聲音庫」建立角色，可共用參考錄音、錄音語言及逐字稿。合成頁可以覆寫這一次的逐字稿。歷史記錄保存提交時的角色名稱、模型及取樣率，也可選兩筆並排試聽。移除角色會保留原錄音及已生成的 WAV。

VoxCPM2、IndexTTS-2 及 Qwen3-TTS 已完成介接替身測試。請依鎖定的上游版本備齊執行環境與模型，並在實機確認生成及離線能力。[上游版本](../engines/README.md)

Qwen 的三個型號可放在同一引擎根目錄，亦可分別用本機引擎包匯入。生成時優先使用目前選用版本所含的型號，並從已安裝包補齊其他型號。

### 刪除及重裝

- 「刪除引擎」只處理聲台安裝的版本。舊整合包的官方模型會搬到獨立模型庫，供重裝時重用。整合包內的其他已識別模型與錄音資料會搬到 `preserved/<引擎>/<版本資料夾>/`。外部資料夾可解除連接，原本檔案由使用者管理。
- 「刪除模型」只移除所選的模型版本。需要時按「下載模型」或匯入模型包重裝。聲音庫、參考錄音、草稿、生成歷史及 WAV 會保留。
- 「刪除微調模型」移除推理權重及該次訓練的檢查點。專案、片段、逐字稿、特徵及合成記錄會保留。角色若曾選用該模型，會顯示找不到模型，可改選 v2Pro 基礎模型。
- 「設定」可試聽並分別刪除未被引用的參考錄音或已生成的 WAV。刪除 WAV 後，歷史仍會顯示台詞、模型及合成設定。資源移除紀錄保存在 `config/removed-assets/`。

專案的 [engines/](../engines/README.md) 存放固定版本原始碼。啟動器內置網頁及介接程式。主程式與語音模型分開派發。工作資料寫到啟動器旁邊的 `config/`、`voices/`、`outputs/`、`cache/` 和 `logs/`。執行記錄以作業編號命名。可用的 Windows 顯示卡驅動及每個引擎需要的依賴由對應執行環境提供。

## Build and verify

With Go 1.23+ and Python 3.10+ available to the developer:

```powershell
go test ./...
go build -trimpath -ldflags="-s -w" -o Start.exe ./studio
python tests/smoke.py Start.exe
python tests/lifecycle.py Start.exe
python tests/training_tools.py
```

Run `Start.exe -no-browser -data "D:\TimbreDeck Data"` for an explicit workspace. The server binds an ephemeral IPv4 loopback port. The launcher has no third-party Go dependencies. Fixture tests exercise all five adapters, 15 synthesis modes, WAV export, voice profiles and the training tool contracts. Engine pack validation requires listening tests with real weights and target hardware.

## 用真實引擎作粵語對照

`tests/acceptance.py` 透過啟動器的本機介面匯入同一段錄音，向已安裝的引擎提交相同台詞，再從輸出歷史的下載介面取得 WAV。請使用 3 至 10 秒的單人粵語錄音，第二個文字參數填入錄音的實際逐字稿。輸出資料夾須使用新名稱。

```powershell
python tests/acceptance.py .\Start.exe .\reference.wav "今日天氣幾好，我哋一齊出去行吓。" .\run-01 --gpt "D:\Engines\GPT-SoVITS" --cosy "D:\Engines\CosyVoice3" --text "今晚八點半喺旺角站等。"
```

只傳入其中一個引擎路徑也可測試。每個成功的引擎會輸出以引擎命名的 WAV。`comparison.json` 記錄輸入及輸出雜湊、取樣率、長度、峰值、耗時及失敗原因。報告的 generated 代表已取得有訊號的音檔；粵語讀音、語尾及音色相似度須另外試聽。

在有 NVIDIA 顯示卡的 Windows 上，使用本版本重新建置的 `Start.exe`，在上面的命令加上 `--require-cuda`。驗收工具會核對每個音檔所屬作業的 `execution.json`，確認實際產生波形的張量，以及主合成模型的參數，都在同一個 CUDA 裝置。任何引擎缺少證據或回退到 CPU 時，報告會標示 `cuda_status: failed` 並回傳非零退出碼，供試聽的有效音檔仍會保存。

`comparison.json` 同時保留各作業的模型、PyTorch 與 CUDA 版本、GPU 名稱及可取得的 PyTorch 峰值分配量。峰值只計 PyTorch 在該程序內分配的記憶體。CosyVoice 的 ONNX 前處理執行提供者另行記錄。執行時請在驗收記錄另填 Windows 版本、NVIDIA 驅動及引擎包版本。

## Engine pack publishing

Prepare a tested engine root with the above layout, third-party license notices, matching source revisions, complete offline weights, and its portable interpreter and dependencies. Package from a separate output directory:

```powershell
python tools/make_pack.py gpt-sovits 1.0.0-nvidia "D:\Builds\GPT-SoVITS" "D:\Packs"
```

To publish the engine and its weights as separately downloadable assets, run both commands from the same tested root. Select a variant for each model package.

```powershell
python tools/make_pack.py gpt-sovits 1.0.0-nvidia "D:\Builds\GPT-SoVITS" "D:\Packs" --kind runtime
python tools/make_pack.py gpt-sovits 1.0.0 "D:\Builds\GPT-SoVITS" "D:\Packs" --kind model --variant v2Pro
```

The download buttons look up `<engine>-runtime.json` and `<engine>-model-<variant>.json` under `packs-v1`. Legacy bundle manifests still use `<engine>.json`. Variant names match the official model names shown in the studio, such as `v2Pro` or `VoiceDesign`.

Upload the versioned ZIP parts to this repository's release tagged `packs-v1`. Upload the engine JSON manifest last. Use a new version for changed packages. The launcher displays compressed and expanded sizes and available space, resumes downloads, verifies each part and the entire archive, then extracts into a staging directory. Only a complete install switches the selected runtime. No pack is bundled with the launcher preview.

離線安裝時，將 JSON 清單與全部分卷放在同一資料夾。在「模型管理」選對應引擎，於「匯入本機引擎包」輸入清單的完整路徑，再按「校驗並安裝」。安裝會直接讀取分卷。已安裝的版本出現在同一面板，可切回先前版本。

## GPT-SoVITS v2Pro 訓練流程

在「訓練素材」新建專案，匯入 PCM16 WAV。先按停頓自動切片，或自行輸入每段的起訖毫秒。儲存後可逐段預聽。若引擎執行環境已安裝 faster-whisper，可在同一頁使用本機 CTranslate2 模型為空白片段產生候選逐字稿。預設模型路徑為 `GPT_SoVITS/pretrained_models/faster-whisper-large-v3/`，包含模型、設定與 tokenizer。轉錄完成後重新選取專案。校對後將每段標為訓練、保留試聽或剔除，再儲存並匯出。ZIP 的 `dataset.list` 只含訓練片段，`holdout.list` 和 `holdout/` 另存試聽素材。

將資料集 ZIP 解壓，並使用與本倉庫鎖定版本相符、含訓練依賴的 GPT-SoVITS 執行環境。S2 微調另需 v2Pro 的 `s2Dv2Pro.pth`。下例中的 `<project-id>` 就是工作室 `training/` 下該專案的目錄名稱。請將它替換成實際值。

```powershell
python tools/prepare_gpt_dataset.py "D:\Engines\GPT-SoVITS" "D:\Dataset" "D:\Features" --gpu 0
python tools/train_gpt_model.py "D:\Engines\GPT-SoVITS" "D:\Features" ".\training\<project-id>\run" --gpu 0 --epochs 8 --batch 2
```

特徵準備省略 `--gpu` 時走 CPU。微調工具要求執行環境可使用 CUDA。中斷後，以相同引擎、特徵、輸出目錄和參數重跑微調命令，保留已完成的階段與上游檢查點。兩組推理權重完整後，工作室可以在「語音合成」按「重新偵測微調模型」，用同一段保留台詞比較基礎及微調模型，也可把微調模型設定為角色的預設。特徵準備、轉錄、微調交接及模型切換經替身測試，真實權重的訓練和試聽仍待驗收。

如果基礎模型和引擎分開安裝，兩個指令均加上 `--models "<模型檔案位置>"`，路徑可在模型管理頁展開查閱。
