param([Parameter(Mandatory=$true)][string]$OutputRoot)
$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
$output=[IO.Path]::GetFullPath($OutputRoot)
if (Test-Path -LiteralPath $output) { throw "Use a new output directory: $output" }

# Derive the ID from the build inputs, not from HEAD alone.
$demo=@('docs/examples/2B/profile.json','docs/examples/2B/reference.wav')
$inputs=@('go.mod','Open-TimbreDeck.cmd','studio/VERSION')+$demo
$inputs+=Get-ChildItem -LiteralPath (Join-Path $root 'studio'),(Join-Path $root 'cmd'),(Join-Path $root 'tools') -Recurse -File |
  Where-Object { $_.Extension -in '.go','.py','.js','.html','.css','.svg','.ps1' } |
  ForEach-Object { [IO.Path]::GetRelativePath($root,$_.FullName).Replace('\','/') }
$ledger=($inputs | Sort-Object -Unique | ForEach-Object {
  $_+':'+(Get-FileHash -LiteralPath (Join-Path $root $_) -Algorithm SHA256).Hash.ToLowerInvariant()
}) -join "`n"
$sha=[Security.Cryptography.SHA256]::Create()
try {$digest=[Convert]::ToHexString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($ledger))).ToLowerInvariant()} finally {$sha.Dispose()}
$id=$digest.Substring(0,12)
$version=(Get-Content -LiteralPath (Join-Path $root 'studio/VERSION') -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+-preview\.\d+$') { throw "Invalid Studio version" }
$stage=Join-Path $output 'TimbreDeck'
[IO.Directory]::CreateDirectory((Join-Path $stage 'tools')) | Out-Null
[IO.Directory]::CreateDirectory((Join-Path $stage 'docs')) | Out-Null
[IO.Directory]::CreateDirectory((Join-Path $stage 'docs/examples/2B')) | Out-Null
Push-Location $root
try {
  & go build -buildvcs=false -trimpath -ldflags "-s -w -X main.buildID=$id" -o (Join-Path $stage 'Start.exe') ./studio
  if($LASTEXITCODE){throw "Studio build failed: $LASTEXITCODE"}
  & go build -buildvcs=false -trimpath -ldflags '-s -w' -o (Join-Path $stage 'TimbreDeck-AI.exe') ./cmd/td-agent
  if($LASTEXITCODE){throw "Launcher build failed: $LASTEXITCODE"}
} finally {Pop-Location}

foreach($file in (@('Open-TimbreDeck.cmd','README.md','docs/WORKSTATION.zh-HK.md','docs/AI.zh-HK.md','tools/td_agent.py','tools/td_mcp.py')+$demo)){
  Copy-Item -LiteralPath (Join-Path $root $file) -Destination (Join-Path $stage $file)
}
$manifest=[ordered]@{version=$version;id=$id;inputSHA256=$digest;files=[ordered]@{}}
$allow=@('Open-TimbreDeck.cmd','README.md','docs/WORKSTATION.zh-HK.md','docs/AI.zh-HK.md','Start.exe','TimbreDeck-AI.exe','tools/td_agent.py','tools/td_mcp.py')+$demo
foreach($file in $allow) {
  $manifest.files[$file]=(Get-FileHash -LiteralPath (Join-Path $stage $file) -Algorithm SHA256).Hash.ToLowerInvariant()
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $stage 'build.json') -Encoding utf8NoBOM
$zip=Join-Path $output 'TimbreDeck-Windows-x64.zip'
Compress-Archive -LiteralPath $stage -DestinationPath $zip
"$((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant())  TimbreDeck-Windows-x64.zip" | Set-Content -LiteralPath (Join-Path $output 'SHA256SUMS.txt') -Encoding ascii
"PACKAGED $version + $id"
