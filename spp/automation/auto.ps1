param([string]$photo, [string]$codes, [string]$outDir)
# For each code: pick "[CAL] <code> ..." in SPP, Ctrl+S, save as <photo>_<code>.tif, wait.
$d = Split-Path -Parent $MyInvocation.MyCommand.Path
Add-Type -AssemblyName System.Windows.Forms, UIAutomationClient, UIAutomationTypes
$A=[System.Windows.Automation.AutomationElement]; $TS=[System.Windows.Automation.TreeScope]
$names = @{}
$p = Get-Process SIGMA_PhotoPro6

# Wait for SPP to finish rendering. A fixed pause is not enough: a Quattro
# file can take tens of seconds, and Ctrl+S during the render opens the save
# dialog before the image is ready. CPU time is a good proxy for "still
# working" - wait for a floor, then until the process stops burning CPU.
function Wait-Idle([int]$floorMs = 2500, [int]$maxMs = 90000) {
  $t0 = Get-Date
  Start-Sleep -Milliseconds $floorMs
  while (((Get-Date) - $t0).TotalMilliseconds -lt $maxMs) {
    $proc = Get-Process SIGMA_PhotoPro6 -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $proc) { return }
    $a = $proc.TotalProcessorTime
    Start-Sleep -Milliseconds 500
    $b = (Get-Process -Id $proc.Id -ErrorAction SilentlyContinue).TotalProcessorTime
    if (-not $b) { return }
    if (($b - $a).TotalMilliseconds -lt 80) { return } # quieter than ~16% of one core
  }
}
foreach ($line in (Get-Content "$env:LOCALAPPDATA\SIGMA\SIGMA_PhotoPro6\X3F_Setting.xml" -Encoding UTF8)) {
  if ($line -match '<Name>(\[CAL\] ([A-Z]\d\d) [^<]*)</Name>') { $names[$Matches[2]] = $Matches[1] }
}
foreach ($code in $codes.Split(',')) {
  $name = $names[$code]
  if (-not $name) { "SKIP ${code}: no [CAL] preset"; continue }
  $out = Join-Path $outDir "${photo}_$code.tif"
  if (Test-Path $out) { "SKIP ${code}: $out exists"; continue }
  $png = [IO.Path]::ChangeExtension($out, '.png')
  if (Test-Path $png) { "SKIP ${code}: $png exists"; continue }
  $t0 = Get-Date
  & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 300
  $r = & "$d\pick.ps1" -name $name
  if ($r -notmatch 'picked|already') {
    # The list can lose its scroll position or close: Esc, then try again.
    [System.Windows.Forms.SendKeys]::SendWait('{ESC}'); Start-Sleep 1
    & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 500
    $r = & "$d\pick.ps1" -name $name
  }
  if ($r -notmatch 'picked|already') { "FAIL $code pick: $r"; break }
  Start-Sleep -Milliseconds 500
  & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 300
  # Only save once SPP has finished rendering the setting (Quattro files are
  # slow; the dialog used to appear mid-render).
  Wait-Idle
  & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 200
  [System.Windows.Forms.SendKeys]::SendWait('^s')
  $s = & "$d\save.ps1" -dir $outDir -file "${photo}_$code.tif"
  if ($s -notmatch 'OK clicked') { "FAIL $code save: $s"; break }
  $last = -1; $ok = $false
  while (((Get-Date) - $t0).TotalSeconds -lt 180) {
    if (Test-Path $out) { $len = (Get-Item $out).Length; if ($len -gt 0 -and $len -eq $last) { $ok = $true; break }; $last = $len }
    Start-Sleep -Milliseconds 700
  }
  if (-not $ok) { "FAIL ${code}: no file"; break }
  "OK $code $([int]((Get-Date)-$t0).TotalSeconds)s $((Get-Item $out).Length) bytes  ($name)"
}
