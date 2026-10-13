param([string]$name)
# Picks preset $name in SPP's settings dropdown: opens it, scrolls the popup
# grid with the mouse wheel until the row is visible, clicks it.
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class W2 {
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint x, uint y, int d, IntPtr e);
}
"@
[W2]::SetProcessDPIAware() | Out-Null
$A = [System.Windows.Automation.AutomationElement]
$TS = [System.Windows.Automation.TreeScope]
$p = Get-Process SIGMA_PhotoPro6
$pc = New-Object System.Windows.Automation.PropertyCondition($A::ProcessIdProperty, $p.Id)
function Win($n) { $A::RootElement.FindAll($TS::Children, $pc) | ? { $_.Current.Name -like $n } | Select -First 1 }
function Click($x, $y) { [W2]::SetCursorPos([int]$x, [int]$y) | Out-Null; Start-Sleep -Milliseconds 60; [W2]::mouse_event(2,0,0,0,[IntPtr]::Zero); [W2]::mouse_event(4,0,0,0,[IntPtr]::Zero) }

$main = Win '*.X3F'
$all = $main.FindAll($TS::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
$save = $all | ? { $_.Current.Name -eq 'Save Settings' } | Select -First 1
$sr = $save.Current.BoundingRectangle
# The settings combo sits right above "Save Settings".
$combo = $all | ? { $r = $_.Current.BoundingRectangle; [math]::Abs($r.X - $sr.X) -lt 8 -and $r.Height -gt 15 -and $r.Height -lt 26 -and ($sr.Y - ($r.Y + $r.Height)) -ge 0 -and ($sr.Y - ($r.Y + $r.Height)) -lt 12 -and $_.Current.Name -ne '' } | Select -First 1
if (-not $combo) {
  # Some layouts don't expose the side panel: use the toolbar's copy of the
  # settings list, i.e. any control whose text is one of SPP's setting names.
  $known = @{}
  foreach ($line in (Get-Content "$env:LOCALAPPDATA\SIGMA\SIGMA_PhotoPro6\X3F_Setting.xml" -Encoding UTF8)) {
    if ($line -match '<Name>([^<]*)</Name>') { $known[$Matches[1]] = $true }
  }
  $combo = $all | ? { $r = $_.Current.BoundingRectangle; $r.Height -ge 12 -and $r.Height -lt 30 -and $r.Width -gt 80 -and $known.ContainsKey($_.Current.Name) } | Select -First 1
}
if (-not $combo) { "ERROR combo not found"; exit 1 }
if ($combo.Current.Name -eq $name) { "already $name"; exit 0 }
if (-not $combo.Current.IsEnabled) {
  # Adjustment Mode "X3F" (the camera's own settings) locks the list:
  # switch to "Custom" (the dot above its label, next to the combo).
  $cr = $combo.Current.BoundingRectangle
  # The mode dots (X3F, Auto, Custom) are small unnamed panes in a row
  # above the combo; Custom is the rightmost.
  $dot = $all | ? { $r = $_.Current.BoundingRectangle; $r.Width -ge 10 -and $r.Width -le 22 -and $r.Height -ge 10 -and $r.Height -le 22 -and $r.Y -lt $cr.Y -and $r.Y -gt $cr.Y - 80 -and $r.X -gt $cr.X + $cr.Width/2 } | Sort-Object { $_.Current.BoundingRectangle.X } | Select -Last 1
  if (-not $dot) { "ERROR the settings list is locked and no Custom mode was found"; exit 1 }
  $lr = $dot.Current.BoundingRectangle
  Click ($lr.X + $lr.Width/2) ($lr.Y + $lr.Height/2)
  Start-Sleep -Milliseconds 800
  $all = $main.FindAll($TS::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
  $combo = $all | ? { $_.Current.NativeWindowHandle -eq $combo.Current.NativeWindowHandle } | Select -First 1
  if (-not $combo.Current.IsEnabled) { "ERROR could not switch to Custom mode"; exit 1 }
}
$cr = $combo.Current.BoundingRectangle
if (-not (Win 'CtmComboBoxList')) { Click ($cr.X + $cr.Width/2) ($cr.Y + $cr.Height/2); Start-Sleep -Milliseconds 600 }
$pop = Win 'CtmComboBoxList'
if (-not $pop) { "ERROR popup did not open"; exit 1 }
$pr = $pop.Current.BoundingRectangle
$editCond = New-Object System.Windows.Automation.PropertyCondition($A::ControlTypeProperty, [System.Windows.Automation.ControlType]::Edit)
$VP = [System.Windows.Automation.ValuePattern]::Pattern
# Scroll to the top first.
[W2]::SetCursorPos([int]($pr.X + 100), [int]($pr.Y + $pr.Height/2)) | Out-Null
for ($i = 0; $i -lt 60; $i++) { [W2]::mouse_event(0x0800, 0, 0, 120, [IntPtr]::Zero) }
Start-Sleep -Milliseconds 300
$seen = New-Object System.Collections.Generic.List[string]
for ($step = 0; $step -lt 200; $step++) {
  $cells = $pop.FindAll($TS::Descendants, $editCond)
  foreach ($c in $cells) {
    $v = $c.GetCurrentPattern($VP).Current.Value
    if (-not $seen.Contains($v)) { $seen.Add($v) }
    if ($v -eq $name) {
      $r = $c.Current.BoundingRectangle
      if ($r.Y -ge $pr.Y - 1 -and ($r.Y + $r.Height) -le ($pr.Y + $pr.Height + 1)) {
        Click ($r.X + 60) ($r.Y + $r.Height/2)
        Start-Sleep -Milliseconds 800
        $now = (Win '*.X3F').FindAll($TS::Descendants, [System.Windows.Automation.Condition]::TrueCondition) | ? { $_.Current.NativeWindowHandle -eq $combo.Current.NativeWindowHandle } | % { $_.Current.Name }
        "picked '$name' after $step scrolls; combo now '$now'"
        exit 0
      }
    }
  }
  [W2]::SetCursorPos([int]($pr.X + 100), [int]($pr.Y + $pr.Height/2)) | Out-Null
  [W2]::mouse_event(0x0800, 0, 0, -120, [IntPtr]::Zero)
  Start-Sleep -Milliseconds 120
}
# Say what the list held, so a photo SPP shows other settings for can be told apart.
$cal = @($seen | ? { $_ -like '`[CAL`]*' }).Count
"ERROR '$name' not reached ($($seen.Count) settings seen, $cal [CAL]; first: $(($seen | Select -First 6) -join ' | '))"; exit 1
