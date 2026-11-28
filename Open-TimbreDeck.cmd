@echo off
setlocal
"%~dp0TimbreDeck-AI.exe" open-studio %*
if errorlevel 1 pause
