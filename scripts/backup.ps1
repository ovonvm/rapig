# Rapig 本机备份（Windows 测试机用；生产在 Linux，见 backup.sh）。
# 布局与 Linux 一致：<exe 同级>\backup\<时间戳>\{db,storage,bootstrap,manifest.json}
# 需要 python（含 sqlite3 模块）做一致性快照；storage 用 robocopy 全量镜像（Windows 非生产，不追求硬链增量）。
# 用法：powershell -ExecutionPolicy Bypass -File scripts\backup.ps1
$ErrorActionPreference = 'Stop'

$exe = Join-Path $PSScriptRoot '..\dist\rapig.exe'
if (-not (Test-Path $exe)) { $exe = (Read-Host 'rapig.exe 路径').Trim('"') }
$exe = (Resolve-Path $exe).Path
$exeDir = Split-Path -Parent $exe
$data = Join-Path $exeDir 'data'
$backup = Join-Path $exeDir 'backup'
$db = Join-Path $data 'db\rapig.db'
if (-not (Test-Path $db)) { throw "找不到数据库：$db" }

$ts = (Get-Date).ToUniversalTime().ToString('yyyyMMdd-HHmmss')
$snap = Join-Path $backup $ts
if (Test-Path $snap) {
    $base = $ts; $n = 1
    while (Test-Path (Join-Path $backup $ts)) { $ts = "$base-$n"; $n++ }
    $snap = Join-Path $backup $ts
}
New-Item -ItemType Directory -Force -Path (Join-Path $snap 'db') | Out-Null

$py = @'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
s = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
d = sqlite3.connect(dst)
s.backup(d)
d.close(); s.close()
'@
$pyExe = if ($env:RAPIG_PYTHON) { $env:RAPIG_PYTHON } else { 'python' }
$tmp = Join-Path $env:TEMP "rapig-backup-$pid.py"
Set-Content -Path $tmp -Value $py -Encoding UTF8
try {
    & $pyExe $tmp $db (Join-Path $snap 'db\rapig.db')
    if ($LASTEXITCODE -ne 0) { throw "python 快照失败（退出码 $LASTEXITCODE）；可用 RAPIG_PYTHON 指定解释器" }
} finally {
    Remove-Item $tmp -ErrorAction SilentlyContinue
}

if (Test-Path (Join-Path $data 'storage')) {
    robocopy (Join-Path $data 'storage') (Join-Path $snap 'storage') /MIR /NFL /NDL /NJH /NJS /NP | Out-Null
}
if (Test-Path (Join-Path $data 'bootstrap')) {
    Copy-Item (Join-Path $data 'bootstrap\*') (Join-Path $snap 'bootstrap') -Recurse -Force -ErrorAction SilentlyContinue
}

$dbFile = Join-Path $snap 'db\rapig.db'
$sha = (Get-FileHash $dbFile -Algorithm SHA256).Hash.ToLower()
$size = (Get-Item $dbFile).Length
$ver = (& $exe version 2>$null | Select-Object -First 1)
$manifest = [ordered]@{
    created_at = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    snapshot   = $ts
    db_sha256  = $sha
    db_bytes   = $size
    version    = "$ver"
}
$manifest | ConvertTo-Json | Set-Content (Join-Path $snap 'manifest.json') -Encoding UTF8
Write-Host "备份完成：$snap（db $size 字节）"
