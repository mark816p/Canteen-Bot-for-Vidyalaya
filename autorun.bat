@echo off
setlocal EnableDelayedExpansion

:: ---------------------------------------------------------------------------
:: Canteen Meal Notifier – autorun.bat
:: Runs daily (e.g. via Scheduled Task at 20:00) to scrape tomorrow's menu
:: and send it to all configured WhatsApp groups.
:: ---------------------------------------------------------------------------

:: Resolve the directory this .bat file lives in (no trailing slash)
set "SCRIPT_DIR=%~dp0"
set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

:: The Python package lives inside the "nss" subfolder
set "NSS_DIR=%SCRIPT_DIR%\nss"

:: Log file sits next to this .bat for easy inspection
set "LOG_FILE=%SCRIPT_DIR%\autorun.log"

echo [%DATE% %TIME%] ===== Canteen Notifier started ===== >> "%LOG_FILE%"

:: Move into the nss directory so relative paths in main.py resolve correctly
cd /d "%NSS_DIR%"

:: ------------------------------------------------------------------
:: 1. Ensure the virtual-environment Python binary is healthy
:: ------------------------------------------------------------------
if exist "venv\Scripts\python.exe" (
    "venv\Scripts\python.exe" --version >nul 2>&1
    if errorlevel 1 (
        echo [%DATE% %TIME%] venv Python is broken – rebuilding... >> "%LOG_FILE%"
        rmdir /s /q "venv"
    )
)

:: ------------------------------------------------------------------
:: 2. Create the venv if it does not exist
:: ------------------------------------------------------------------
if not exist "venv\Scripts\python.exe" (
    echo [%DATE% %TIME%] Creating virtual environment... >> "%LOG_FILE%"
    py -3 -m venv venv >> "%LOG_FILE%" 2>&1
    if errorlevel 1 python -m venv venv >> "%LOG_FILE%" 2>&1
)

:: ------------------------------------------------------------------
:: 3. Install / upgrade dependencies
:: ------------------------------------------------------------------
echo [%DATE% %TIME%] Installing dependencies... >> "%LOG_FILE%"
"venv\Scripts\python.exe" -m pip install --quiet -r requirements.txt >> "%LOG_FILE%" 2>&1

:: ------------------------------------------------------------------
:: 4. Run the notifier (pass any extra arguments from the caller)
:: ------------------------------------------------------------------
echo [%DATE% %TIME%] Running notifier... >> "%LOG_FILE%"
"venv\Scripts\python.exe" main.py %* >> "%LOG_FILE%" 2>&1
set "EXIT_CODE=%ERRORLEVEL%"

echo [%DATE% %TIME%] Notifier exited with code %EXIT_CODE% >> "%LOG_FILE%"
echo [%DATE% %TIME%] ===== Canteen Notifier finished ===== >> "%LOG_FILE%"

endlocal
exit /b %EXIT_CODE%
