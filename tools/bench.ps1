# Runs the render benchmarks on one photo and keeps the output, so a change
# can be compared against the last run.
#
#   .\tools\bench.ps1                                  # first X3F under Desktop\foveon-samples
#   .\tools\bench.ps1 -Photo 'D:\Foto\Photos\SD1\SDIM0095.X3F'
#   .\tools\bench.ps1 -Compare dist\bench-2026-10-23-2301.txt
#
# The summary says where the time goes; the benchmarks give numbers a
# regression can be checked against (-benchmem includes allocations).

param(
  [string]$Photo = "",
  [string]$Dir = "dist",
  [string]$Compare = "",
  [int]$BenchTime = 3
)

$ErrorActionPreference = "Stop"
$env:GOCACHE = Join-Path $env:TEMP "fl-gocache"
$env:GOTMPDIR = Join-Path $env:TEMP "fl-gotmp"
New-Item -ItemType Directory -Force $env:GOCACHE, $env:GOTMPDIR, $Dir | Out-Null

if (-not $Photo) {
  $samples = Join-Path $env:USERPROFILE "Desktop\foveon-samples"
  $Photo = (Get-ChildItem $samples -Recurse -File -Filter *.X3F -ErrorAction SilentlyContinue |
            Select-Object -First 1).FullName
}
if (-not $Photo -or -not (Test-Path $Photo)) { throw "no X3F photo: pass -Photo <path>" }
$env:FL_BENCH_PHOTO = (Resolve-Path $Photo).Path

$stamp = Get-Date -Format "yyyy-MM-dd-HHmm"
$out = Join-Path $Dir "bench-$stamp.txt"

"# render bench $stamp" | Out-File $out -Encoding utf8
"# photo: $env:FL_BENCH_PHOTO" | Out-File $out -Append -Encoding utf8
"# go: $(go version)" | Out-File $out -Append -Encoding utf8
"" | Out-File $out -Append -Encoding utf8

"== stage summary ==" | Tee-Object -FilePath $out -Append
$env:FL_BENCH_SUMMARY = "1"
go test -run TestBenchSummary -v . 2>&1 |
  Where-Object { $_ -match "^\s|^SDIM|^D:|^[A-Z]" } |
  Tee-Object -FilePath $out -Append
Remove-Item Env:FL_BENCH_SUMMARY

"" | Out-File $out -Append -Encoding utf8
"== benchmarks ==" | Tee-Object -FilePath $out -Append
go test -run XXX -bench . -benchmem -benchtime "${BenchTime}s" . 2>&1 |
  Where-Object { $_ -match "^(Benchmark|PASS|ok|FAIL)" } |
  Tee-Object -FilePath $out -Append

Write-Host ""
Write-Host "saved $out"

if ($Compare -and (Test-Path $Compare)) {
  Write-Host ""
  Write-Host "== against $Compare =="
  $old = Get-Content $Compare | Where-Object { $_ -match "^Benchmark" }
  $new = Get-Content $out | Where-Object { $_ -match "^Benchmark" }
  foreach ($n in $new) {
    $name = ($n -split "\s+")[0]
    $o = $old | Where-Object { $_ -like "$name *" } | Select-Object -First 1
    if ($o) {
      $nsOld = [double](($o -split "\s+")[2])
      $nsNew = [double](($n -split "\s+")[2])
      $pct = 100 * ($nsNew - $nsOld) / $nsOld
      "{0,-28} {1,10:N0} -> {2,10:N0} ns/op  {3,6:+0.0;-0.0}%" -f $name, $nsOld, $nsNew, $pct
    }
  }
}
