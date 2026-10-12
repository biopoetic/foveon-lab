param([string]$photo, [string]$codes, [string]$outDir)
# For each code: pick "[CAL] <code> ..." in SPP, Ctrl+S, save as <photo>_<code>.tif, wait.
$d = Split-Path -Parent $MyInvocation.MyCommand.Path
Add-Type -AssemblyName System.Windows.Forms, UIAutomationClient, UIAutomationTypes
$A=[System.Windows.Automation.AutomationElement]; $TS=[System.Windows.Automation.TreeScope]
$names = @{}
$p = Get-Process SIGMA_PhotoPro6
foreach ($line in (Get-Content "$env:LOCALAPPDATA\SIGMA\SIGMA_PhotoPro6\X3F_Setting.xml" -Encoding UTF8)) {
  if ($line -match '<Name>(\[CAL\] ([A-Z]\d\d) [^<]*)</Name>') { $names[$Matches[2]] = $Matches[1] }
}
foreach ($code in $codes.Split(',')) {
  $name = $names[$code]
  if (-not $name) { "SKIP ${code}: no [CAL] preset"; continue }
  $out = Join-Path $outDir "${photo}_$code.tif"
  if (Test-Path $out) { "SKIP ${code}: $out exists"; continue }
  $t0 = Get-Date
  & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 300
  $r = & "$d\pick.ps1" -name $name
  if ($LASTEXITCODE -ne 0 -and $r -notmatch 'picked|already') { "FAIL $code pick: $r"; break }
  Start-Sleep -Milliseconds 800
  & "$d\front.ps1" | Out-Null; Start-Sleep -Milliseconds 300
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
