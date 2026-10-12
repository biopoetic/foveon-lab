# Accuracy roadmap: from "same look" to "same pixels"

Where Foveon Lab's preview stands against Sigma Photo Pro 6.9, and what each further step would
take. All numbers are mean absolute difference per pixel and channel, 0–255, measured on photos the
fitted part did **not** see (held out). SPP itself repeats to within 0.5.

## Where we are (October 2026, v0.3.3)

| | SD1 Merrill (Accurate mode) | SD15 |
|---|---|---|
| Neutral starting image vs SPP | **~7** (raw data → SPP mapping) | ~17 (camera JPEG) |
| Whole real presets, end to end | **3.6–11.4, mean ~8** | not measured (est. ~20) |
| Single sliders, from SPP's neutral | 0.4–2 (Fill Light 4–7) | not measured |
| White balance codes 2 / 6 | 2.8 / 1.9 | not measured |

Visibility: under ~3 is invisible, 5–10 shows only side by side, 25+ is obvious. Before the
calibration work the end-to-end error was ~28.

**Exports are automatic now:** `foveon-lab calibrate -auto -photo <name> -steps A,B,C,V,W` drives SPP
itself (~40 s per export, unattended), so every step below that "needs exports" costs only time.

## Steps, by expected gain

### 1. ~~SD15: its own slider calibration~~ — done (October 2026)
Measured A00–A22 on a second SD1 photo (SDIM0046) and an SD15 photo (SDIM1151), all exported by
`calibrate -auto`. The SD1 model predicts the unseen SD1 photo almost as well as its own (sliders
0.3–2.3/255), but on SD15 it is off by up to 8: the difference is the camera, not the scene. The
renderer now has a model per family (`render/model.json` = SD1, averaged over both SD1 photos;
`render/model_SD15.json`). Still open: a second SD15 photo to confirm, and SD15 white balance /
colour modes (SD1's are used meanwhile). Note: pick calibration photos without colours at the
sRGB edge — a saturated red flower (75 % clipped channels) measured gamut clipping, not sliders.

### 2. SD15: a better starting image (gain: ~17 → maybe ~8)
Neither raw data (22.7) nor a camera-JPEG mapping (21.5) beat the compensated JPEG (17) on SD15;
SPP's neutral for SD15 seems to vary per photo.
- Find out what varies: compare the six SD15 A00 exports for exposure/white-balance differences
  against their X3F metadata (CAMF white-balance tables, exposure info).
- Try x3f_extract options (different colour/WB handling, `-color none` + our own matrix).
- If SPP applies a per-photo auto adjustment, model it from the metadata.
- Effort: research first; maybe a few more A00 exports.

### 3. X3F Fill Light (gain: ~2–4 on presets that use it, i.e. most of the pack)
SPP's Fill Light is local and edge-preserving; ours lifts by a blurred surround (8×8 table).
- Measure at more settings (B14 0.25, B15 2) and on a second photo.
- Replace the box-blur mask with an edge-aware one (guided filter / bilateral) and fit its radius
  and edge strength to the exports.
- Effort: 2–4 exports + an evening of fitting.

### 4. More pairs for the SD1 raw mapping (gain: ~7 → ~5)
Seven A00 pairs today. 15–20 photos (varied light, WB, ISO, medium-res files) would steady the
3×3 matrix + curves; a small 3D LUT could then replace the per-channel curves.
- Effort: ~10 A00 exports.

### 5. Slider interactions (gain: ~1–2)
Sliders were measured one at a time and chained. Measure a few pairs (Exposure+Contrast,
Highlight+Fill, Contrast+Saturation) and correct the chaining order/strength where it misses.
- Effort: ~6 exports.

### 6. ~~What is not modelled at all yet~~ — done (October 2026, via `calibrate -auto`)
- **Sharpness**: SPP sharpens the full image, so on reduced views it nearly vanishes; ours now
  scales with view size (6.7 → 1.8).
- **Beyond ±1**: B steps measured (Exposure/Contrast/Highlight/Shadow ±2, Fill Light 0.25/2/−1).
- **All colour modes**: measured; 0–3 and 9–12 are one default look, 5 and 8 distinct; all ≤1.8.
- Still open: **fine WB grid offsets** some presets rely on (SPP keeps them outside the XML).

### 7. Other bodies (gain: whole new user groups)
DP Merrill (same sensor as SD1: likely shares the SD1 model, verify with 2–3 A00 exports),
DP1/DP2 (SD14/SD15 era), Quattro (different sensor and SPP path: separate calibration).

## What "perfect" would need beyond this

Pixel-identical output means reproducing SPP's own raw pipeline (demosaic-free Foveon colour
separation, its noise reduction, its local Fill Light), not mapping around it. That is a
research project — X3F raw → SPP-identical output — and only worth starting if steps 1–4 leave a
gap users actually notice.
