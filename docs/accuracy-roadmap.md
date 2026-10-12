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

## Steps, by expected gain

### 1. SD15: its own slider calibration (gain: large, for every SD15 user)
The slider model was measured on one SD1 file only. SD15 may process differently.
- Export A00–A22 for one SD15 photo (as done for SDIM0260), `analyze`, compare the SD1 model's
  error on it; if worse, ship a per-family model (`render/model.json` per family).
- Effort: 23 exports. Code: small (model keyed by family).

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

### 6. What is not modelled at all yet (gain: small but visible)
- **Sharpness** (B21/B22 exports exist in the pack; needs a local sharpen model).
- **Saturation beyond ±1, Exposure/Contrast beyond ±1** (B steps: extrapolated today).
- **Colour modes other than Standard / Portrait / Landscape** (C steps: unknown codes).
- **Fine WB grid offsets** some presets rely on (SPP keeps them outside the XML).
- Effort: the B and C exports already listed in the calibration pack (~35).

### 7. Other bodies (gain: whole new user groups)
DP Merrill (same sensor as SD1: likely shares the SD1 model, verify with 2–3 A00 exports),
DP1/DP2 (SD14/SD15 era), Quattro (different sensor and SPP path: separate calibration).

## What "perfect" would need beyond this

Pixel-identical output means reproducing SPP's own raw pipeline (demosaic-free Foveon colour
separation, its noise reduction, its local Fill Light), not mapping around it. That is a
research project — X3F raw → SPP-identical output — and only worth starting if steps 1–4 leave a
gap users actually notice.
