# TimbreDeck 聲台

繁體中文｜Windows 免安裝多引擎語音工作室。使用者先選引擎，再進入語音合成、聲線設計或模型訓練。規劃以本倉庫的 Releases 按需下載引擎、模型與執行環境。

English | A portable multi-engine speech studio for Windows. Select an engine, then choose synthesis, voice design, or model training where supported. The planned launcher downloads versioned engine packs, models and runtimes on demand from this repository’s Releases.

## 專案狀態 · Project status

Windows x64 聲台支援語音合成、即時轉換、角色管理、素材清理及 GPT-SoVITS 訓練。各引擎需要獨立的執行環境及權重；已安裝項目與可用模式以工作室回報為準。真實 GPU 測試與替身測試分開列於驗收資料，預覽版 ZIP 本身不含模型或使用者資料。

**[下載 Windows 預覽版 / Windows previews](https://github.com/qqharrisonqqjp/TimbreDeck/releases)** · [啟動與工作區 / Getting started](docs/WORKSTATION.zh-HK.md)

The launcher and AI command share the same data directory and running Studio. Open the extracted package with `Open-TimbreDeck.cmd`; the exact version and build ID are shown in Settings and by `TimbreDeck-AI.exe status`. Engine runtimes, model weights, audio and project data are separate from the portable package.

| Engine | 預定工作流程 / Planned workflow |
|---|---|
| GPT-SoVITS v2Pro | 錄音複製、角色微調 / Reference cloning and voice fine-tuning |
| CosyVoice 3 | 粵語、指令朗讀 / Cantonese and instruction-guided synthesis |
| VoxCPM2 | 聲線設計、可控複製 / Voice design and controllable cloning |
| IndexTTS-2 | 分開提供音色與情緒錄音 / Independent voice and emotion references |
| Qwen3-TTS | Base、CustomVoice、VoiceDesign / Cloning, preset voices and voice design |

## 介面與發佈 · Interface and distribution

左邊工作區同合成引擎各自保留選擇。底部作業島可睇進度或試聽歷史輸出。首次啟動使用套件旁的 `StudioData`，既有本機部署沿用原資料路徑；可用絕對路徑環境變數 `TD_STUDIO_DATA` 指定其他工作區。

The Windows builder creates a ZIP, a checksum file, and a manifest of the included files. The only bundled voice is the opt-in [2B reference](docs/examples/2B/reference.wav) with its [profile metadata](docs/examples/2B/profile.json); other recordings, histories, drafts, data and weights are excluded.

[AI CLI / MCP 使用指南](docs/AI.zh-HK.md)

[使用指南](docs/WORKSTATION.zh-HK.md) · [原始碼、開發規格及上游授權](https://github.com/qqharrisonqqjp/TimbreDeck)
