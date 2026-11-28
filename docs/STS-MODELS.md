# STS 模型：Windows 本機錄音變聲

在工作區選 **流式 STS → STS 模型**，放入 1–25 秒來源錄音同 2–20 秒目標參考。模型只改音色，不將錄音轉成文字再朗讀。**開始啟動 STS 模型**會跑預熱並讓模型常駐；**停止 STS 模型**卸載。切換模型開始工作或使用其他引擎時，前一個 STS worker 會先卸載。

| ID | 模型 | 路徑 |
|---|---|---|
| `cosyvoice3` | CosyVoice 3 | 既有分段輸出及咪高峰 |
| `x-vc` | X-VC | 2400 ms 滑動視窗，120 ms 輸出單位 |
| `seed-vc-tiny` | Seed-VC Tiny v1（XLSR / HiFT，22050 Hz） | 完整檔案、300 ms 原生分幀及即時咪高峰 |
| `meanvc2` | MeanVC 2（40 ms + 40 ms 前瞻） | 錄音分段串流；Fast-U2++ 內容編碼用 CPU |
| `tvtsyn1` | TVTSyn v1 LA4 | 錄音分段串流；實驗性、非商業用途 |

Seed-VC Tiny 即時模式呼叫鎖定上游的 `real-time-gui.py` 內 `custom_infer`，沿用上下文及 SOLA 交疊，使用 TimbreDeck 收音與輸出介面，唔啟動上游 GUI。先按 **開始啟動 STS 模型**，再啟動咪高峰；錄音檔可手動切換 **原生即時分幀（300 ms）**。其他新增模型仍只開放錄音。輸出 WAV 同試聽串流採用 24 kHz PCM16 單聲道。量度檔案首段、收音分段提交及 Windows 虛擬音訊線的延遲要分開。

本機 CLI 支援 `sts --model seed-vc-tiny --streaming --source SOURCE --reference REFERENCE --output-dir OUTPUT`。省略 `--streaming` 沿用整段模式；不指定模型保留 CosyVoice 3。MCP 工具可用 `"streaming": true`。`capabilities` 列出 `stsModels` 就緒狀態；同一名額由桌面及 AI 共用。獨立套件需有 `python-dotenv==1.2.1` 才能載入上游即時入口。

## 本機套件結構與重驗

預設套件位於 `D:\TimbreDeck\sts-models`（由啟動檔上一層的 `sts-models` 解析），可用絕對路徑環境變數 `TIMBREDECK_STS_ASSETS` 覆寫。現有安裝的 Qwen Python 3.11 / Torch 2.8 + cu128 作為基礎，依賴放在套件下的 `deps311`、`deps-x-vc`（X-VC 的 Transformers 4.44.2 / Tokenizers 0.19.1），不改 Qwen 本身。模型只在本地執行；未註冊的模型顯示「未就緒」。

- `tools/fetch_sts_weights.py`：從鎖定的公開版本下載，核對 SHA-256 或 Git blob ID。MeanVC 2 額外需官方 [MeanVC 2 checkpoint 指引](https://github.com/ASLP-lab/MeanVC2/blob/13acf84c1bf135ea5edad9c245b345289b06b33e/initialization.py) 的 fine-tune 權重及由 [Microsoft WavLM-Large](https://github.com/microsoft/unilm/tree/master/wavlm) 抽取的 cfg，放到 `weights/meanvc2-extra`。
- `tests/sts_models_bench.py`：要求 CUDA、測一次冷啟動、至少一次熱推論，驗證 PCM 同下載 WAV 的音訊位元完全相同。
- `tools/register_sts_model.py`：確認測試證據及 upstream commit，寫入 `runtime.json` 和 `verified/<id>.json`。先由官方來源核對權重雜湊，先好註冊；未包含在 TimbreDeck 程式發行包。
- `tests/sts_models_api.py`、`tests/sts_models_browser.cjs`：以獨立 StudioData 驗證本機工作室 API、UI 音訊及 CLI。

上游：[X-VC](https://github.com/Jerrister/X-VC) · [Seed-VC](https://github.com/Plachtaa/seed-vc) · [MeanVC2](https://github.com/ASLP-lab/MeanVC2) · [TVTSyn](https://github.com/PSI-TAMU/TVTSyn)。
