$ErrorActionPreference = 'Stop'
$target = Join-Path $PSScriptRoot 'source.zip'
$temp = Join-Path $PSScriptRoot ([guid]::NewGuid().ToString() + '.tmp')
try {
    $output = [IO.File]::OpenWrite($temp)
    try {
        foreach ($i in 0..3) {
            $part = Join-Path $PSScriptRoot ('source.zip.part{0:00}' -f $i)
            $inputStream = [IO.File]::OpenRead($part)
            try { $inputStream.CopyTo($output) } finally { $inputStream.Dispose() }
        }
    } finally { $output.Dispose() }
    if ((Get-FileHash -Algorithm SHA256 $temp).Hash -ne '3a93a3aa1121a8d65f23fce13d7cb1d0adc5113a81a7988744adb5b561aaa037') { throw 'Archive checksum mismatch.' }
    Move-Item -Force $temp $target
} finally {
    if (Test-Path $temp) { Remove-Item -LiteralPath $temp }
}
