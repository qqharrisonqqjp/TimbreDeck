# 流式 STS 語音克隆

在左邊「工作區」選 **流式 STS**。匯入來源錄音（1–25 秒）和目標音色參考（2–20 秒），按「開始轉換及試聽」。來源音訊決定內容，參考錄音決定目標音色。結果可在該頁或「輸出歷史」下載完整 WAV。停止試聽不會取消作業；要停止合成，使用狀態列中的取消作業。

這是已安裝的 CosyVoice 3 的原生 `inference_vc(..., stream=True)`，不轉錄、不重新做文字 TTS，無需增加模型。它先讀取整份匯入的來源及參考錄音，再按區塊輸出音訊。這是**錄音輸入、流式輸出**，不是一邊講咪、一邊持續送入並雙向串流。上游所稱的雙流 TTS 是文字輸入流與音訊輸出流，並不等同直接音訊輸入流。

首次需載入模型。短時間連續用 STS 會重用模型程序；預設閒置 75 秒卸載，開始其他引擎作業前也會卸載，以免佔住顯存。普通語音合成不改成常駐，亦沒有改其他引擎的推理設定。取消或失敗會關閉該 STS worker，未完成的音訊不會寫入歷史。

本機 AI 入口：

```powershell
& 'D:\TimbreDeck\TimbreDeck-AI.exe' ensure-running
& 'D:\TimbreDeck\TimbreDeck-AI.exe' sts --source 'D:\TimbreDeck\AI-Inbox\來源.wav' --reference 'D:\TimbreDeck\AI-Inbox\目標音色.wav'
```

工具只接受明確指定的兩個檔案並匯出完整 WAV；MCP 對應 `timbredeck_sts`，檔案仍須位於設定的輸入資料夾。底層串流為 24 kHz、單聲道、little-endian PCM16；完整輸出為同一批樣本的 WAV，不將試聽的暫存作永久檔案。

[CosyVoice 上游專案](https://github.com/QwenAudio/CosyVoice)
