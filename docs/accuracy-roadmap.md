# Accuracy roadmap: from "same look" to "same pixels"

> The project's mission, principles and current goals are in the [manifest](MANIFEST.md). This file
> is the detailed record of the "match SPP" work; the "Where we are" table below is from
> 13 October 2026 (since then: SD1 3.97, DP Merrill 4.21, SD15 6.03 held out, all from the raw base).

Where Foveon Lab's preview stands against Sigma Photo Pro 6.9, and what each further step would
take. All numbers are mean absolute difference per pixel and channel, 0–255, measured on photos the
fitted part did **not** see (held out). SPP itself repeats to within 0.5.

## Where we are (October 2026, after the second calibration round)

All numbers held out (the photo was not used to fit what is tested on it).

| | SD1 Merrill (Accurate mode) | SD15 |
|---|---|---|
| Neutral starting image vs SPP | **7.2** (raw → SPP mapping, 16 photos) | **9.9** (camera JPEG + per-photo balance and tone curve, 22 photos) |
| Fill Light +0.5 / +1 | 5.2 / 9.8 (12 photos) | 5.5 / 10.3 (12 photos) |
| Other sliders, white balance, colour modes | ~0.3–3 | ~0.6–3.5 |
| Real presets from SPP's neutral (6 presets) | 2.1–6.2 | 4.4–8.4 |

Visibility: under ~3 is invisible, 5–10 shows only side by side, 25+ is obvious. Before the
calibration work the end-to-end error was ~28–40.

### Tone and colour: three photos, every slider (18 October 2026)

Three SD1 photos (SDIM0031, SDIM0046, SDIM0067) were exported from SPP with the whole A ladder
(A00–A22: exposure ±1, contrast ±0.5/1, highlight ±0.5/1, shadow ±0.5, saturation ±0.5/1, colour
modes 6/7, colour adjust R/B). Current model, brightness error (0–255); 0046 and 0067 were never
used for any fit:

| photo | error, all sliders but fill | worst step |
|---|---|---|
| SDIM0031 (SD1) | 0.4–4.0, mean 1.3 | Exposure −1: 4.0 |
| SDIM0046 (SD1, held out) | 0.3–1.8, mean 0.85 | ColorAdjustR: 1.8 |
| SDIM0067 (SD1, held out) | 0.3–2.4, mean 0.95 | Exposure +1: 2.4 |
| SDIM1021 (SD15, in the fit) | 0.2–3.4, mean 1.5 | Saturation +1: 3.4 |
| SDIM1151 (SD15, in the fit) | 0.3–4.7, mean 1.5 | Saturation +1: 4.7 |
| SDIM1200 (SD15, **held out**) | 0.5–4.3, mean 1.5 | ColorAdjustR: 4.3 |

**Tone and colour are at or inside the 3/255 target on unseen photos** (typical 1–2; the worst
single steps are 4.0–4.3). The old "SD1 ~4, SD15 ~6" figures are dominated by Fill Light, whose
error here is 3.1–21.5 (SD1) and 2.7–12.6 (SD15).

The colour-adjust normalisation is right: dropping the brightness-preserving normalisation (SPP
might apply R/G/B without it) makes A20 worse on every photo (1.2 → 1.5, 4.3 → 4.7), so it stays.
The residual ~4 on ColorAdjustR is a model-space difference, not the normalisation.

### Older cameras measured (18 October 2026)

One body per family, full A ladder exported from SPP (the files in `testdata`), measured with the
current emulation — they fall back to the SD1 or SD15 model, there are no per-family models yet:

| body | all steps but fill | Fill Light +0.5 / +1 |
|---|---|---|
| SD9 | 1.0–7.9, mean 3.4 | 8.1 / 14.1 |
| SD10 | 0.3–6.4, mean 2.2 | 5.4 / 10.7 |
| SD14 | 0.2–6.7, mean 2.0 | **16.9 / 31.9** |
| dp2 Quattro | 0.6–6.4, mean 2.2 | **15.2 / 28.7** |

What the numbers say:

- the SD1/SD15 fallback works reasonably on all four bodies (mean 2–3), so they are usable today;
- **Fill Light is far worse than on the families it was fitted on** (SD14 31.9, Quattro 28.7 against
  SD1's 4–21): the fill model is sensor-specific, so those bodies need their own fill data;
- **SPP ignores Colour modes 6/7 on SD9, SD10 and SD14** — its export is identical to the neutral —
  while we applied them, which cost 3.2–6.4 of the error on those bodies. **Fixed in v0.4.40**: the
  renderer asks the camera's own model name (`CAMMODEL`), not the preset family, because the SD14 and
  the SD15 share a family but only the SD15 has modes; the mode is now a no-op on cameras that lack
  it. Measured after: 5.2/1.0 → 0.2/0.2 (SD9), 6.4/0.6 → 0.2/0.2 (SD10), 3.6/3.2 → 0.2/0.2 (SD14);
  SD1, SD15 and Quattro unchanged.

Next for this row: per-family fill data (A16/A17/B14/B15/B16 on three bodies each) and the
colour-mode rule.


Fill Light does not improve by refitting or simplifying: refitting the coefficients across the three
photos drops the fitting-set mean 10.07 → 8.95 but leave-one-out on 0046 scores 21.5 / 32.8 against
19.8 / 31.5 (no generalisation), and zeroing the two local terms makes it much worse (A17 8.3 → 21.2,
B15 16.3 → 38.5). The two-scale terms are load-bearing; the scene dependence is the problem.

Two more variants were measured on five photos (SDIM0031/0046/0067/0092/0138, full fill ladder,
mean error over 25 measurements):

| variant | mean |
|---|---|
| current two-scale model | **9.12** |
| merged coefficients (average of the five) | 9.9 (fitting set; worse held out) |
| the measured Local[8][8] table instead of Coef | 9.88 — fixes the worst photo (0046 +1: 19.8 → 12.3) but costs on the others |
| no local terms (x only) | 21+ at +1 |

So four approaches have been tried against the same exports (refit, no-local, measured table, mask
shape) and none beats the current model.

**Corrected ordering, six photos (SDIM0031/0046/0067/0092/0137/0138).** With more photos the
fill-research ranking settles, and the three-photo result was small-sample noise: the app's guided
surround is best (mean 9.34), the gauss at the same scales 9.38, FIR 9.69, a plain global curve
12.08. A Retinex-style compressive term (d·|d| in addition to d) changes nothing on SD1 (9.36) and
only ~3% on SD15 (10.04 → 9.74), so it does not justify a model-format change. The remaining fill
error is the limit of this model class, not of its tuning.

The next real attempt has to be a different operator — a Retinex with an edge-stopping surround and
its own two scales — or per-family fill data for the older bodies, whose fill is far worse than the
families the model was fitted on.


**Exports are automatic now:** `foveon-lab calibrate -auto -photo <name> -steps A,B,C,V,W` drives SPP
itself (~40 s per export, unattended), so every step below that "needs exports" costs only time.

## Steps, by expected gain

### 1. ~~SD15: its own slider calibration~~ — done (October 2026)
Measured A00–A22 on a second SD1 photo (SDIM0046) and an SD15 photo (SDIM1151), all exported by
`calibrate -auto`. The SD1 model predicts the unseen SD1 photo almost as well as its own (sliders
0.3–2.3/255), but on SD15 it is off by up to 8: the difference is the camera, not the scene. The
renderer now has a model per family (`render/model.json` = SD1, averaged over both SD1 photos;
`render/model_SD15.json`). Confirmed on a second, unseen SD15 photo (SDIM1021): the SD15 model
gets the tone sliders within 0.6–3.5 vs 2.5–7 for the SD1 model; the SD15 model now averages both
SD15 photos. SD15 white balance and colour modes were measured too and differ completely from
SD1 (WB 6: 26 → 2.3 once measured, mode 5: 21.7 → 0.9). Note: pick calibration photos without colours at the
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
SPP's Fill Light is local and edge-preserving; ours lifts by a guided-filter surround (two scales)
through a 16-bin two-scale model.

**Measured 18 October 2026, and the refit does not generalise.** Three SD1 photos
(SDIM0031/0046/0067) were exported with the full fill ladder (B14 0.25, A16 0.5, A17 1, B15 2,
B16 −1). Current model, held-out brightness error (0–255):

| photo | +0.25 | +0.5 | +1 | +2 | −1 |
|---|---|---|---|---|---|
| SDIM0031 | 1.8 | 4.3 | 8.9 | 16.3 | 5.3 |
| SDIM0046 | 5.4 | 11.5 | **19.8** | **31.5** | 19.1 |
| SDIM0067 | 1.4 | 3.3 | 6.4 | 11.4 | 4.6 |

Refitting the fill coefficients from all three (merge the per-photo models, splice the fill
section) lowers the mean from 10.07 to 8.95 — but that is fitting-set: with 0046 left out, it scores
**21.5 / 32.8** on 0046, worse than the current model (19.8 / 31.5). The improvement was the model
memorising 0046.

`fill-research` over the same three photos, held out (mean error):

| candidate | mean |
|---|---|
| global curve only (no surround) | 10.47 |
| app: guided-filter surround | 11.13 |
| FIR: gauss 0.0019 + 0.019 | **10.27** |
| gauss 1/80 + 1/20 | 11.05 |

So the mask shape is not the lever either: every candidate is within ~8%, and the best is barely
better than no local term at all. The problem is that the local coefficients are **scene-dependent**
— the two local terms of the +1 model vary from −0.08 to −0.30 across the three photos while the
x-term stays at ~0.21. A fixed two-scale model cannot capture that.

Next step is therefore not a mask or a coefficient refit but a **scene-adaptive fill**: fit the two
scales per image (or blend by its histogram) from the same measured FIR local means SPP uses. Until
that exists, Fill Light stays at 3–13 on unseen photos, and it is the last large SPP gap: every
other A slider measured 0.4–4.3 on SDIM0031.

### 4. More pairs for the SD1 raw mapping (gain: ~7 → ~5)
Seven A00 pairs today. 15–20 photos (varied light, WB, ISO, medium-res files) would steady the
3×3 matrix + curves; a small 3D LUT could then replace the per-channel curves.
- Effort: ~10 A00 exports.
- **Tried and rejected (2026-10-13): synthetic charts as extra training pairs.** Two SD1 charts
  drawn inside the gamut (`tools/synth -gamut`, 166/192 clean patches in SPP's export) made the
  held-out SD1 starting image worse, 6.87 → 7.58 dE: SPP's response on flat patches is strongly
  non-linear in the shadows (pure-matrix residual 50–140% in the darkest bins), which does not
  carry over to real scenes. Charts stay a black-box measuring tool (`tools/chartread`), not
  training data; `fit-raw -charts` keeps the option for research.

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
