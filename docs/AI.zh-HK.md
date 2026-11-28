# TimbreDeck AI 通道

桌面與 AI 共用工作室和工作槽。啟動 `TimbreDeck-AI.exe ensure-running`，先查 `capabilities` 或 `status`。只使用用戶指定的素材；忙碌時回報，不取消既有作業。本機 stdio MCP 用同一個執行檔，參數為 `mcp`；預設使用程式旁的 AI-Inbox 與 AI-Exports。

| 功能 | CLI / MCP |
|---|---|
| 語音合成 | `synthesize` / `timbredeck_synthesize`。GPT‑SoVITS V5 用 `v5turbo` 或 `v5dev`，微調權重只限 `v2Pro`。支援分段合成和獨立片段 ZIP。 |
| 辨識及轉換 | `asr` 可指定辨識模型；`sts` 支援本機 STS 模型，Seed‑VC Tiny 可選檔案串流。 |
| 清理 | `clean --mode mossformer`、`lavasr`、`lavasr-denoise`；仍支援舊三種模式。讀 `readyCleaningModes` 確認實際安裝。 |
| 資料集 | `dataset --clean-mode mossformer`（可選）、`--no-split`、`--no-transcribe`、`--skip-unrecognized`。只準備 ZIP，不訓練；無逐字稿可匯入手動校對。 |
| 常駐 | `resident-status` → `warm-model --kind tts --engine gpt-sovits --variant v5turbo`。不自動換掉已常駐模型。只有用戶確認後才對當刻的 ID 用 `stop-model --resident-id …`。MCP 對應 `resident_status`、`warm_model`、`stop_model`。 |
| 角色及結果 | `voices` 只列 ID、名稱、語言，不回傳音訊或逐字稿。生成時可用 `--voice-id`；完成後回傳已驗證 WAV／ZIP 和該作業的模型、耗時及 CUDA 證據（若有）。 |

擂臺可由 AI 逐個呼叫已有工具比較，不會並行搶用工作槽。咪高峰／虛擬咪路由、手動校對與啟動微調、刪除以及結束工作室，保留喺桌面，避免代理誤動其他分頁。參考音不等於訓練素材。不要為製作訓練集去搜尋角色庫。

**可重用指令：**「先查 TimbreDeck 的 `capabilities`／`status`。只用 AI-Inbox 裏我指定的檔案；忙碌就回報。請用我指定的版本做這一項工作。完成後把 `artifacts` 裏的真實 WAV 或 ZIP 附給我，並列出輸出模型和時間（如果有）。」

套件只附 [2B 的參考 WAV](examples/2B/reference.wav) 和 [角色資料](examples/2B/profile.json)。要試用，複製 WAV 到 AI-Inbox 或在聲音庫選它、根據資料填入名稱和逐字稿；不會自動加入個人工作室。
