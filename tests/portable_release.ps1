param([Parameter(Mandatory=$true)][string]$ReleaseRoot,[Parameter(Mandatory=$true)][string]$TestData)
$ErrorActionPreference='Stop'
$stage=Join-Path $ReleaseRoot 'TimbreDeck'
$manifest=Get-Content -LiteralPath (Join-Path $stage 'build.json') -Raw | ConvertFrom-Json
$zip=Join-Path $ReleaseRoot 'TimbreDeck-Windows-x64.zip'
$payload=[IO.Compression.ZipFile]::OpenRead($zip)
try {
  $expected=@($manifest.files.PSObject.Properties.Name)+@('build.json')
  $names=@($payload.Entries | Where-Object {$_.Name} | ForEach-Object {$_.FullName.Replace('\','/') -replace '^TimbreDeck/'})
  if(Compare-Object -ReferenceObject ($expected|Sort-Object) -DifferenceObject ($names|Sort-Object)){
    throw "Only manifest-listed files may be packaged"
  }
  $audio=@($names | Where-Object {$_ -match '\.(wav|mp3|mp4|m4a|aac|mov|ogg|flac)$'})
  if($audio.Count -ne 1 -or $audio[0] -cne 'docs/examples/2B/reference.wav'){
    throw "Only the opt-in 2B recording may be bundled"
  }
  foreach($item in $manifest.files.PSObject.Properties){
    $local=Join-Path $stage $item.Name
    if((Get-FileHash -Algorithm SHA256 -LiteralPath $local).Hash.ToLowerInvariant() -ne $item.Value){throw "Hash mismatch: $local"}
  }
} finally {$payload.Dispose()}
"PASS: ZIP contains only the manifest-listed files"

if(Test-Path -LiteralPath $TestData){throw "Use a new test data folder"}
$old=$env:TD_STUDIO_DATA
$env:TD_STUDIO_DATA=[IO.Path]::GetFullPath($TestData)
$exe=Join-Path $stage 'TimbreDeck-AI.exe'
try {
  $layout=& $exe launcher-info | ConvertFrom-Json
  if($LASTEXITCODE -or $layout.Data -ne $env:TD_STUDIO_DATA -or $layout.Studio -ne (Join-Path $stage 'Start.exe')){
    throw "Launcher did not resolve the packaged paths"
  }
  $visible=& (Join-Path $stage 'Open-TimbreDeck.cmd') --print-url | ConvertFrom-Json
  if($LASTEXITCODE -or !$visible.ok){throw "Desktop entry did not start Studio"}
  $headless=& $exe ensure-running | ConvertFrom-Json
  if($LASTEXITCODE -or !$headless.ok -or $headless.url -ne $visible.url){throw "CLI launched a second Studio"}
  $browser=Invoke-RestMethod -Uri ($visible.url+'/api/state') -TimeoutSec 6
  $ai=& $exe status | ConvertFrom-Json
  if($LASTEXITCODE -or $browser.build.id -ne $manifest.id -or $ai.build.id -ne $manifest.id -or $browser.build.version -ne $ai.build.version){
    throw "GUI and CLI have different build IDs"
  }
  "PASS: desktop and AI share a process and build $($ai.build.version) + $($ai.build.id)"
  $caps=& $exe capabilities | ConvertFrom-Json
  if($LASTEXITCODE -or !$caps.ok){throw "Cannot read portable CLI capabilities"}
  $messages=@(
    '{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2025-11-25"}}',
    '{"jsonrpc":"2.0","method":"notifications/initialized"}',
    '{"jsonrpc":"2.0","method":"tools/list","id":2}'
  )
  $reply=@($messages | & $exe mcp | ForEach-Object {$_|ConvertFrom-Json})
  if($LASTEXITCODE){throw "Packaged MCP could not open"}
  $tools=($reply | Where-Object id -eq 2).result.tools
  $mcpClean=($tools | Where-Object name -eq 'timbredeck_clean').inputSchema.properties.mode.enum
  $mcpDataset=($tools | Where-Object name -eq 'timbredeck_dataset').inputSchema.properties.clean_mode.enum
  if(!$mcpClean -or !$mcpDataset -or (Compare-Object $caps.cleaningModes $mcpClean) -or
     (Compare-Object $caps.datasetOptions.cleanModes $mcpDataset)){
    throw "Studio, CLI and MCP cleaning options diverged"
  }
  "PASS: Studio/CLI/MCP options agree and fresh portable MCP launched"
} finally {
  $sessionFile=Join-Path $TestData 'cache/agent-session.json'
  if(Test-Path -LiteralPath $sessionFile) {
    $session=Get-Content -LiteralPath $sessionFile -Raw | ConvertFrom-Json
    $process=Get-Process -Id $session.pid -ErrorAction SilentlyContinue
    if($process -and $process.Path -eq (Join-Path $stage 'Start.exe')){
      Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
    }
  }
  $env:TD_STUDIO_DATA=$old
}
