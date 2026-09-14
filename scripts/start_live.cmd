@echo off
rem hymrec 持续监听自启脚本：启动 FunASR 与 hymrec（若未运行）
rem 由计划任务 hymrec-live（登录时）调用

rem 1) 启动 FunASR 流式服务
netstat -an | findstr ":10095" >nul
if errorlevel 1 (
  start "funasr-wss" /min cmd /c "cd /d G:\funasr-server && G:\funasr-env\Scripts\python.exe funasr_wss_server.py --host 127.0.0.1 --port 10095 --device cpu --ngpu 0 --certfile "" > G:\funasr-server\server.log 2>&1"
)

rem 2) 启动 hymrec（等待 FunASR 加载模型，最长 5 分钟）
tasklist | findstr /i "hymrec.exe" >nul
if errorlevel 1 (
  timeout /t 180 /nobreak >nul
  cd /d G:\GitHub\henyemiren-live
  start "hymrec-live" /min cmd /c "bin\hymrec.exe -c config.yaml > hymrec.log 2>&1"
)
