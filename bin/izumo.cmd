@echo off
rem Windows shim: runs bin/izumo with Git for Windows' bash (not WSL's).
rem git.exe lives in <Git>\cmd, bash.exe in <Git>\bin.
for %%G in (git.exe) do set "IZUMO_GIT=%%~dp$PATH:G"
if not defined IZUMO_GIT (echo izumo: Git for Windows not found on PATH 1>&2 & exit /b 1)
"%IZUMO_GIT%..\bin\bash.exe" "%~dp0izumo" %*
