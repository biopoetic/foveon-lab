# How Sigma Photo Pro renders an X3F (what we have measured)

A knowledge base for anyone building X3F tools — Foveon Lab, x3fuse, or your own. Everything here
was found **from the outside**: SPP's licence forbids decompiling it, so each statement is a
black-box measurement — export the same raw from SPP with one setting changed, compare against our
emulation — or a reading of the camera's own calibration data (the CAMF block in every X3F).

How to read the evidence marks:

- **held out** — confirmed on photos that no fit has seen. Trust it.
- **in-sample** — measured on the photos it was fitted to. Plausible, not proven.
- **observed** — a behaviour seen in exports, not a fitted model.
- **reported** — someone else's finding, not reproduced here yet.

Errors are mean absolute difference per pixel and channel on a 0–255 scale against SPP's own
export. SPP repeats itself to within 0.5; under ~3 is invisible, 5–10 shows only side by side.
Measured against SPP 6.9.

## 1. The method

`foveon-lab calibrate -auto -photo <X3F> -steps <groups>` drives SPP itself: it adds `[CAL]`
presets to SPP's list (one setting changed each), selects each, exports a TIFF, and the analyser
compares every export with Foveon Lab's emulation of the same setting. The step groups:

| group | what changes |
|---|---|
| A | every slider at ±0.5 / ±1 (exposure, contrast, highlight, shadow, saturation, fill), colour modes 6/7, colour adjust R/B |
| B | the same sliders at ±2, Fill Light 0.25 / 2 / −1 |
| C, V | colour modes and finer steps |
| W | the WhiteBalance codes (0, 2 Sunlight, 6 Fluorescent) |
| X | colour adjust combined with a colour mode or a white balance |

The analysis is offline: `foveon-lab analyze -dir <calibration folder> -photos <X3F folder>`.

## 2. White balance: a preset's code is the camera's own balance

SPP's preset XML stores `WhiteBalance` as a code. Mapping (observed):

| code | meaning |
|---|---|
| 0, 1, 9 | as shot |
| 2 | Sunlight |
| 3 | Shade |
| 4 | Overcast |
| 5 | Incandescent |
| 6 | Fluorescent |
| 7 | Flash |
| 8 | ~Custom |

**Code N is not a fixed colour shift: SPP develops the raw with the camera's calibrated balance
for that light** — the CAMF `WhiteBalanceGains` / `WhiteBalanceColorCorrections` entry of the same
name. Developing the raw with that CAMF balance reproduces SPP's export of each code:

| photo | Sunlight | Shade | Overcast | Incandescent | Fluorescent | Flash |
|---|---|---|---|---|---|---|
| SDIM0363 (SD1) | 4.3 | 4.0 | 4.3 | 6.2 | 4.9 | 4.0 |
| SDIM0031 (SD1) | 8–11 across the codes; its as-shot baseline is 8.4 | | | | | |

Evidence: **observed on two SD1 photos**, no fitting involved (the camera's own numbers).

**Held out (19 October 2026).** On four photos no fit had seen (SD1 SDIM0064, SDIM0048; SD15
SDIM1008, SDIM1101), the WB step isolated on SPP's own neutral export (Sunlight and Fluorescent):

| | camera's own balance | per-family correction refitted in v0.4.42 |
|---|---|---|
| 4 unseen photos | **1.3 – 4.7** | 7.1 – 18.2 |
| the 2 photos it was fitted on | 2.4 – 6.0 | **1.2 – 3.0** |

So the camera's balance is the rule, and a fitted correction is memorisation: it wins only on the
photos it was fitted to. An earlier fixed per-code colour mapping failed the same way (20–50 held
out).

## 3. Colour modes are matrices in the camera

Each X3F carries its colour modes in CAMF: `ColorModeCompensations[<mode>] = CMCM_<mode>` (a 3×3
matrix) and `ColorModeContrastCompensations[<mode>] = CMCC_<mode>` (a contrast offset), for
Standard, Vivid, Neutral, Portrait, Landscape and, on Merrill bodies, FCBlue (FOV Classic Blue).
Applying those reproduces the modes (observed).

- Of SPP's ColorMode codes, 0–3 and 9–12 give one default look; 5 and 8 are distinct; all emulated
  to ≤ 1.8 (in-sample, SD1).
- **SPP ignores the colour mode on the SD9, SD10 and SD14**: the export with mode 6 or 7 is
  identical to the neutral one (observed on one body each). Ask the camera's model name
  (`CAMMODEL`), not a family: the SD14 and SD15 look alike but only the SD15 has modes.

## 4. Sliders

- **Tone and colour sliders** (exposure, contrast, highlight, shadow, saturation): emulated to
  0.3–2.4 on held-out SD1 photos and 0.5–4.3 on a held-out SD15 photo — **held out**, and confirmed
  on five more unseen photos on 19 October (mean 0.6–1.45 per photo, worst single step 1.4–4.1,
  including a dp0 Quattro on the dp2 Quattro's model). They differ by
  camera family: the SD1 model is off by up to 8 on an SD15, so each family needs its own model.
- **Colour adjust R/G/B** is brightness-preserving (luminance kept): removing that normalisation
  makes every photo worse (held out). Whether SPP applies it before or after its colour matrix is not
  settled; with a colour mode the combined error is 1.0–3.8.
- **Sharpening** is applied at full resolution, so it nearly vanishes on a reduced view; emulating
  that per view size took one step from 6.7 to 1.8 (observed).

## 5. Fill Light is local, edge-preserving and scene-dependent

The hardest slider. Fill Light lifts shadows by a local, edge-preserving operator.

- A fixed two-scale model gets 9.1–9.3 mean on six SD1 photos (+1 Fill: 6–20 depending on the
  scene).
- The local coefficients change from photo to photo (−0.08 to −0.30 for the same setting), so no
  fixed model generalises. Refits, merged coefficients, a measured table and other mask shapes were
  all tried, and none beat the current model held out.
- It is **sensor-specific**: on bodies it wasn't fitted to, +1 Fill reached 28–32 (SD14, dp2
  Quattro) until those bodies got their own data (in-sample after: 2–11).
- **The model class is right; the data was thin (20 October 2026).** The lift at each brightness is
  linear in the pixel's two surroundings (an edge-aware mean at 1/80 and 1/20 of the width) with
  coefficients that vary smoothly with brightness: fitted to one photo, that reaches ~3 at Fill +1.
  Coefficients shared across a camera's photos do worse on a photo they never saw, but refitting
  them on all 8 SD1 / 5 SD15 photos instead of 2–3 helps: held out (leave-one-out), Fill +0.5 / +1
  go from 5.62 / 10.37 to **5.01 / 9.18** (SD1) and 3.88 / 7.43 to **3.07 / 5.86** (SD15), and the
  worst photos halve (SDIM0046 +1: 19.8 → 9.3).
- **On the SD1, Fill Light's strength follows the whole frame's mean brightness** (20 October
  2026): bright, low-contrast frames get a stronger local lift, dark contrasty ones a weaker one. A
  term in (frame mean − pixel) brightness, held out over 8 photos: Fill +0.5 4.04 → **3.17**, +1
  7.50 → **6.09**, +2 16.40 → **14.79**; +0.25 and −1 did not improve and are unchanged. On the
  SD15 (5 photos) it did not help. The mean is the full frame's, so a crop does not change the fill.
- **The DP Merrill fills like the SD1** (26 October 2026): one model fitted on 8 SD1 + 14 DP Merrill
  photos beats each family's own, held out, at Fill +0.25 / +2 / −1 (SD1 2.38 / 14.79 / 7.48 →
  1.65 / 12.95 / 4.63; DP Merrill 1.99 / 13.43 / 6.34 → 1.46 / 11.85 / 4.22). Same sensor, same
  operator.
- **The Quattro needs its own fit, and the frame-mean term carries it** (27 October 2026): 10–11
  Quattro photos across dp0/dp1/dp2/dp3/sd/sd Quattro H, pooled leave-one-out. With the frame-mean
  term the held-out error beats the shipped (one-photo) coefficients at every step: +0.25
  2.25 → **1.85**, +0.5 5.35 → **4.07**, +1 11.02 → **8.23**, +2 21.79 → **15.84**, −1 5.98 → **5.27**.
  Without the frame-mean term +2 is 17.78 and −1 is 6.34, so the term is load-bearing on this sensor.
  All five steps were refitted on every photo (`tools/fillstudy/fillstudy12.py`); in the app
  (`spp-check -step`, 9 photos) the five steps go 7.52→6.94, 10.70→9.13, 16.78→12.98, 27.92→19.35 and
  8.20→7.44.

## 6. The starting image

- **SD1 / Merrill:** a raw → SPP mapping (matrix + curves) gets the neutral image to 3.97 dE2000
  held out (DP Merrill 4.21).
- **SD15:** SPP's neutral seems to vary per photo. Neither the raw data (22.7) nor a camera-JPEG
  mapping (21.5) beat a compensated JPEG (17). The cause is still open.
- **Synthetic charts are a poor training set:** SPP is strongly non-linear on flat patches in the
  shadows, so charts drawn inside the gamut made the held-out starting image worse (6.87 → 7.58 dE).

## 7. Colour space: SPP reads the preview's EXIF, not the X3F property — held out

H Dot (dpreview, October 2026) reports that SPP takes a different gain branch when the camera's
colour space is set to AdobeRGB, and that without it blues turn magenta. SPP 6.0.5's release note
mentions fixing AdobeRGB processing for SD1/Merrill files. Tested on one SD1 photo (SDIM0031), same
raw data, only the colour space marks changed:

- **X3F property `COLORSPACE` = AdobeRGB alone: no effect** — SPP's export is byte-identical
  (`tools/setprop`).
- **Also marking the embedded preview's EXIF as AdobeRGB** (ColorSpace 0xFFFF, interop index
  "R03", exactly as a camera set to AdobeRGB writes them — checked on an SD14 file; `tools/exifcs`):
  **the rendering changes** — the red channel drops by 15–28 in greens, cyans and blues and rises
  by ~7 in reds, green untouched (output still tagged sRGB). Lower red in blues and cyans is H Dot's
  "more teal, less magenta".

**Confirmed with controls (19 October 2026).** Exported both ways in both formats: the shift is the
same as TIFF and as JPEG, and the format alone makes 0.67 with no bias. One linear-sRGB matrix (rows
sum to 1, so neutrals are unchanged) takes the unmarked export to the marked one:

```
R' = 1.398 R - 0.398 G
G' = G
B' = 1.043 B - 0.043 G
```

4.46 -> 0.28 on SDIM0031 where it was fitted, **2.78 -> 0.24 on SDIM0064, held out**. Foveon Lab
applies it to Merrill-generation files (SD1, DP Merrill) whose preview EXIF is marked AdobeRGB
(`render/adobergb.go`). Older bodies are untested: SPP 6.0.5's note names the SD1 and Merrill series.

## 8. Export colour spaces: SPP clips to sRGB, then re-encodes

SPP's save dialog offers sRGB, Apple RGB, Adobe RGB, ColorMatch RGB and ProPhoto RGB. The same
photo (SDIM0031) exported as 16-bit TIFF in all five and compared through each file's own embedded
ICC profile (`tools/cscheck`): all five agree to 0.0001 in XYZ, and **no pixel of the ProPhoto,
Adobe RGB or ColorMatch export lies outside sRGB** — including the 0.29% of pixels the sRGB export
pins at its gamut edge, where a wider rendering would show more colour. So choosing a wide space in
SPP does not keep Foveon colours that sRGB cannot hold; it re-encodes an sRGB-limited rendering.

Confirmed on two extreme photos (19 October 2026): SDIM0367 and SDIM0260, **infrared frames**
from a full-spectrum camera (red and yellow false colour), where 94% of the sRGB export's pixels are
pinned at the gamut edge. Their ProPhoto exports still hold **no** colour outside sRGB (0.00%) and
match the sRGB export to 0.0001 in XYZ. Evidence: **observed on three photos, conclusive** — SPP
clips to sRGB, then re-encodes into the space you choose.

**How much that clip costs on ordinary photos — little.** Measured on our own raw development with
SPP's colour transform applied, before its clamp (20 October 2026): the share of pixels outside
sRGB is 0.0% (SDIM0031), 0.0% (SDIM0098, saturated green leaves), 0.2% (SDIM0446, deep blue sky and
red roof) and 1.0% (SDIM0450, a red flower; 0.6% clearly outside). With SPP's look, ordinary Foveon
photos sit almost entirely inside sRGB; only infrared frames go far outside (96%). A wider export
that keeps SPP's look would therefore change very little, so Foveon Lab does not offer one. Wide
gamut data is what the raw DNG export is for.

## 9. Open questions

- What makes SPP's SD15 neutral vary per photo.
- The colour adjust's stage relative to SPP's colour matrix.
- A scene-adaptive Fill Light.
- The fine WB grid offsets some presets rely on (SPP keeps them outside the XML).
- Whether the AdobeRGB branch (section 7) also exists for the SD14/SD15 and the Quattro.

The full measurement record, with every table, is in [accuracy-roadmap.md](accuracy-roadmap.md).
Corrections and new measurements are welcome — open an issue with the exports.
