# TimbreDeck 本機 Agent：CLI 與 MCP

**建議先用 CLI。** 開啟 TimbreDeck 聲台、確認所需引擎與模型已安裝，並複製主控台顯示的本機網址（連接埠每次啟動可能不同）。工具需要 Python 3.9+；CLI 和 MCP 包裝器都使用標準函式庫。

```powershell
python -B -X utf8 tools\td_agent.py --url http://127.0.0.1:49125 status
python -B -X utf8 tools\td_agent.py --url http://127.0.0.1:49125 asr --reference 'D:\Studio\AgentIn\demo.wav' --language en
python -B -X utf8 tools\td_agent.py --url http://127.0.0.1:49125 dataset --reference 'D:\Studio\AgentIn\demo.wav' --language en --output-dir 'D:\Studio\AgentOut'
```

將範例網址和路徑換成實際位置。`td_agent.py --help` 和 `td_agent.py <動作> --help` 列出參數。

若使用這台 Windows 電腦的本機交付版，也可執行 `D:\TimbreDeck\TimbreDeck-AI.exe status`，或以 `D:\TimbreDeck\TimbreDeck-AI.exe mcp` 啟動 MCP。預設只共享 `D:\TimbreDeck\AI-Inbox`，新產物寫到 `D:\TimbreDeck\AI-Exports`。

## 用支援本機 stdio 的 MCP 客戶端

本工具不會安裝到任何客戶端，也不開啟 MCP 網路埠。客戶端須能在 **TimbreDeck 同一台電腦** 上啟動 Python 子程序；遠端或只接受 HTTP MCP 的客戶端不能直接啟動這個 stdio 程序。準備兩個專用資料夾，輸入只放要交給工具處理的 WAV，輸出用來存新產物。請先建立兩個資料夾，並保留聲台運行。

```powershell
python -B -X utf8 tools\td_mcp.py `
  --input-root 'D:\Studio\AgentIn' `
  --output-dir 'D:\Studio\AgentOut' `
  --timeout 600
```

若你的客戶端用 `mcpServers` JSON 設定本機程序，可參考下列格式（設定檔位置及功能因客戶端而異）：

```json
{
  "mcpServers": {
    "timbredeck": {
      "command": "C:\\Path\\To\\python.exe",
      "args": [
        "-B", "-X", "utf8",
        "D:\\Studio\\tools\\td_mcp.py",
        "--input-root", "D:\\Studio\\AgentIn",
        "--output-dir", "D:\\Studio\\AgentOut",
        "--timeout", "600"
      ]
    }
  }
}
```

預設沿用 CLI 的聲台發現機制，不必固定連接埠。若有多個工作區，可指定 CLI 所用的 `TD_STUDIO_DATA`，或加入 `--url http://127.0.0.1:<實際連接埠>`。

若你希望工具只在預設工作區未運行時嘗試啟動聲台，可在本機桌面版本的客戶端環境設定 `TD_AGENT_AUTOSTART=1`。這是選用設定，不適用於明確指定 `--url` 或非預設工作區。

### 工具

| 工具 | 用途與例子 |
| --- | --- |
| `timbredeck_capabilities` | 查詢本機引擎及功能 |
| `timbredeck_status` | 查詢聲台和作業狀態 |
| `timbredeck_asr` | 指定 WAV 辨識；例如 `{"reference":"demo.wav","language":"en"}` |
| `timbredeck_synthesize` | 合成 WAV；例如 `{"engine":"gpt-sovits","mode":"clone","text":"Good morning.","language":"en","reference":"demo.wav","transcript":"This is the reference sentence.","reference_language":"en"}` |
| `timbredeck_clean` | 依序去噪、分離人聲或合併；例如 `{"references":["demo.wav","take2.wav"],"mode":"denoise","gap_ms":150}` |
| `timbredeck_dataset` | 自動切段、辨識及打包；例如 `{"references":["demo.wav"],"language":"en","speaker":"speaker-1","denoise":false}` |

`tools/list` 傳回完整參數 schema。相對路徑以 `--input-root` 為準，絕對路徑也必須解析到該目錄之內。工具呼叫不能另指定網址、輸出資料夾或系統指令，也沒有刪除、安裝或訓練入口。首次載入模型可能較慢；工具逐個執行，MCP 並無額外的取消工具，使用中可從聲台管理其作業。

工具結果含 JSON 文字；新版本 MCP 也回傳 `structuredContent`。成功的 WAV 或 ZIP 以產物路徑、大小、類型、SHA-256 和 `file:` URI 表示，較新的 MCP 回應另有 `resource_link`。工具回覆不會附整份檔案。支援資源的客戶端可以用 `resources/list` 列出此會話最近登記的產物，再用 `resources/read` 及原 URI 主動讀取。讀取會重新核對路徑及 SHA-256；每個檔案上限為 8 MiB，二進位內容按 MCP 規格以 Base64 傳送。較大的產物可直接從 `--output-dir` 開啟，再由你選擇是否附加到客戶端。重新啟動 MCP 不會公開輸出資料夾的舊檔案。

語音及辨識文字來自所選錄音；請先試聽、核對，再使用結果。

協定：UTF-8 單行 JSON-RPC；參閱 [MCP stdio 傳輸規格](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[工具規格](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)和[資源規格](https://modelcontextprotocol.io/specification/2025-11-25/server/resources)。測試可用 `python -B -m unittest discover -s tests -p test_td_mcp.py -v`，不需要模型或 GPU。
