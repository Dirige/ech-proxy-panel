#Requires -Version 5.1
<#
.SYNOPSIS 校验 ech-wk-main/config.json 是否合法（只读，不改任何文件）。
.EXAMPLE 运行目录：ech-wk-main → .\scripts\Test-Config.ps1
#>
[CmdletBinding()]
param(
  [string]$ConfigPath = ''
)
if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
  if ($PSScriptRoot) { $ConfigPath = Join-Path $PSScriptRoot '..\config.json' }
  else { $ConfigPath = Join-Path (Get-Location).Path 'config.json' }
}

$ErrorActionPreference = 'Stop'
$fail = 0
function Fail([string]$msg) { Write-Host "FAIL: $msg" -ForegroundColor Red; $script:fail++ }
function Ok([string]$msg)   { Write-Host "OK: $msg" -ForegroundColor Green }

# 1. JSON 可解析
try { $cfg = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json; Ok "JSON 解析通过 ($ConfigPath)" }
catch { Fail "JSON 解析失败：$($_.Exception.Message)"; exit 1 }

# 2. 9 字段齐全
$required = @('listen_addr','server_addr','server_ip','token','dns_server','ech_domain','routing_mode','web_addr','proxy_ip')
foreach ($k in $required) { if ($cfg.PSObject.Properties.Name -notcontains $k) { Fail "缺字段：$k" } }
if ($fail -eq 0) { Ok '9 字段齐全' }

# 3. server_addr 必须带端口
if ($cfg.server_addr -notmatch ':\d+$') { Fail "server_addr 必须带端口（期望形如 xxx:443），当前：$($cfg.server_addr)" }
else { Ok "server_addr 带端口 ($($cfg.server_addr))" }

# 4. routing_mode 四值之一（上游 ech-workers.go:223）
$modes = @('global','bypass_cn','none','custom')
if ($cfg.routing_mode -notin $modes) { Fail "routing_mode 非法：$($cfg.routing_mode)，允许：$($modes -join '/')" }
else { Ok "routing_mode 合法 ($($cfg.routing_mode))" }

# 5. listen 与 web 端口不冲突
function Get-Port([string]$addr) { if ($addr -match ':(\d+)$') { return $Matches[1] }; return $null }
$lp = Get-Port $cfg.listen_addr; $wp = Get-Port $cfg.web_addr
if ($lp -and $wp -and $lp -eq $wp) { Fail "listen_addr 与 web_addr 端口冲突：$lp" }
else { Ok "监听/面板端口不冲突 ($($cfg.listen_addr) / $($cfg.web_addr))" }

# 6. custom 模式提醒规则文件
if ($cfg.routing_mode -eq 'custom') { Write-Host 'WARN: routing_mode=custom，需同步提供 -rules 规则文件（见 rules.example.txt）' -ForegroundColor Yellow }

if ($fail -gt 0) { Write-Host "校验失败：$fail 项" -ForegroundColor Red; exit 1 }
Write-Host '全部通过' -ForegroundColor Green
