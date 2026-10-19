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

What does *not* work: a fixed per-camera colour mapping fitted for each code. It reproduces the
photo it was fitted on and fails on others (20–50 held out). A per-family correction refitted on one
photo per body (v0.4.42) is **in-sample**; the one held-out SD1 photo got worse with it (7.9 → 9.8).

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
  0.3–2.4 on held-out SD1 photos and 0.5–4.3 on a held-out SD15 photo — **held out**. They differ by
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

## 6. The starting image

- **SD1 / Merrill:** a raw → SPP mapping (matrix + curves) gets the neutral image to 3.97 dE2000
  held out (DP Merrill 4.21).
- **SD15:** SPP's neutral seems to vary per photo. Neither the raw data (22.7) nor a camera-JPEG
  mapping (21.5) beat a compensated JPEG (17). The cause is still open.
- **Synthetic charts are a poor training set:** SPP is strongly non-linear on flat patches in the
  shadows, so charts drawn inside the gamut made the held-out starting image worse (6.87 → 7.58 dE).

## 7. Colour space tag — reported, being tested

H Dot (dpreview, October 2026) reports that SPP takes a different gain branch when the camera's
colour space is set to AdobeRGB, and that without it blues turn magenta. SPP 6.0.5's release note
mentions fixing AdobeRGB processing for SD1/Merrill files. All 183 of our SD1 test files are sRGB,
so we have not measured it yet. The test: the same raw with only `COLORSPACE` changed
(`go run ./tools/setprop in.X3F out.X3F COLORSPACE AdobeRGB`), both exported from SPP.

## 8. Open questions

- What makes SPP's SD15 neutral vary per photo.
- The colour adjust's stage relative to SPP's colour matrix.
- A scene-adaptive Fill Light.
- The fine WB grid offsets some presets rely on (SPP keeps them outside the XML).
- The AdobeRGB branch (section 7).

The full measurement record, with every table, is in [accuracy-roadmap.md](accuracy-roadmap.md).
Corrections and new measurements are welcome — open an issue with the exports.
