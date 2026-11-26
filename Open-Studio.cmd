@echo off
setlocal
set "PYTHONPATH="
set "PYTHONHOME="
set "PYTHONDONTWRITEBYTECODE=1"
set "PYTHONUTF8=1"
set "OMP_NUM_THREADS=4"
for %%I in ("%~dp0.") do set "TD_DATA=%%~fI"
if defined TD_STUDIO_DATA set "TD_DATA=%TD_STUDIO_DATA%"
if not defined TIMBREDECK_ASR_ASSETS set "TIMBREDECK_ASR_ASSETS=%~dp0assets"
if not defined TIMBREDECK_CLEAN_ASSETS set "TIMBREDECK_CLEAN_ASSETS=%~dp0assets"
set "MPLCONFIGDIR=%TD_DATA%\cache\mpl"
set "MODELSCOPE_CACHE=%TD_DATA%\cache\modelscope"
set "NUMBA_CACHE_DIR=%TD_DATA%\cache\numba"
"%~dp0Start.exe" -data "%TD_DATA%" %*
