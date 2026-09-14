@echo off
rem hymREC 持续监听自启脚本：启动 FunASR 与 hymREC（若未运行）
rem 由计划任务 hymREC-live（登录时）调用

rem 本机部署目录（修改为实际安装位置，无绝对路径硬编码）
set BASE=%~dp0..\..
set FUNASR_DIR=%BASE%\funasr-server
set FUNASR_ENV=%BASE%\funasr-env
set HYMREC_ROOT=%~dp0..

rem 1) 启动 FunASR 流式服务
netstat -an | findstr ":10095" >nul
if errorlevel 1 (
  start "funasr-wss" /min cmd /c "cd /d %FUNASR_DIR% && %FUNASR_ENV%\Scripts\python.exe funasr_wss_server.py --host 127.0.0.1 --port 10095 --device cpu --ngpu 0 --certfile "" > %FUNASR_DIR%\server.log 2>&1"
)

rem 2) 启动 hymREC（等待 FunASR 加载模型，最长 5 分钟）
tasklist | findstr /i "hymREC.exe" >nul
if errorlevel 1 (
  timeout /t 180 /nobreak >nul
  cd /d %HYMREC_ROOT%
  start "hymREC-live" /min cmd /c "bin\hymREC.exe -c config.yaml > hymrec.log 2>&1"
)
