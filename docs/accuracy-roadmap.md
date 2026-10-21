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

### Checking accuracy: `foveon-lab spp-check`

Every photo with a neutral SPP export (`<name>_A00.tif` under the calibration folder) is developed
exactly as the app shows its preview and scored against the export: all pixels, "area" (both
images reduced 4×: colour and tone without fine detail) and shadows (SPP darker than linear 0.06).

    foveon-lab spp-check -save after.json -compare before.json

`-compare` prints each photo's change and flags those worse by more than `-worse` (0.3). Run it
before and after any change to rendering or calibration. The v0.4.58 baseline
(`foveon-calibration/spp-check-v0.4.58.json`, 83 photos, 4½ min): SD1 6.12 / area 5.04 /
shadows 3.87 (53 photos), SD15 5.40 / 4.83 / 4.27 (30). These include photos the raw-base fit
skips as clipped or out of gamut, so they sit above the held-out fit numbers.

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

The colour adjust's *stage* was also tested: applying the tint at the output (after the tone curve)
instead of the input gives byte-identical output, because the normalisation keeps luminance equal in
both cases — so the residual is a colour-space difference (SPP may apply R/G/B before its colour
matrix), which the A ladder cannot resolve: it has no white-balance or colour-mode step, so the
analysis renders with neither op in play. Measuring it needs a ladder that combines ColorAdjust with
a colour mode or a non-neutral white balance.

**Colour adjust in context (18–19 October 2026).** The X steps do that: R 1.10 combined with a
colour mode (Portrait, Landscape) or a white balance (Sunlight, Fluorescent). With a colour mode the
emulation is fine — X01/X02 read 1.0–3.8 — but with a white balance it was not: X03/X04 read 5.0–25.5.
Switching the WB correction off showed the cause: it *helped* SD1 (X03 5.0 against 15.7 without) and
*harmed* SD15 (24.2 against 7.4) and SD14 — the per-family WhiteBalance correction was wrong.

**Fixed, 19 October 2026.** W00/W02/W06 were exported for SD1, SD15, SD14 and the dp2-Quattro and the
WB corrections refitted:

| | W02 | W06 | X03 | X04 |
|---|---|---|---|---|
| SD1 (SDIM0031) | 5.1 → **2.5** | 5.3 → **1.7** | 5.0 → **2.7** | 5.0 → **2.0** |
| SD15 (SDIM1021) | 22.1 → **1.2** | 22.7 → **3.0** | 24.2 → **3.3** | 25.5 → **5.2** |
| SD14 | 21.3 → **0.7** | 25.8 → **2.5** | 19.2 → **2.4** | 23.3 → **4.2** |
| Quattro (dp2) | 16.3 → **1.0** | 20.2 → **2.5** | — | — |

The dp2-Quattro also got its own model (`model_Quattro.json`, keyed by CalibGroup because the body
has no SD number): its fill went 15.2 / 28.7 → **5.4 / 11.2** and colour mode 6 5.8 → 2.3. The one
held-out SD1 photo (SDIM0138) reads 7.9 → 9.8 at X03, so the WB fit does not transfer perfectly
between scenes; a second photo per family is needed to confirm it.

(The analyzer's export scanner only accepted the codes A, B, C, V and W, so the X exports were
invisible to it at first; it now accepts X too.)

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

**Per-camera models (v0.4.41).** With the fill ladder exported for each body, `render/model_SD9.json`,
`model_SD10.json` and `model_SD14.json` were fitted (tone + fill; WB and colour modes kept from the
default model) and the renderer now prefers a model measured on the camera itself over its preset
family's — the SD14 needed this because it shares the SD15 family but not its fill. Measured on the
same exports (fitting set — one body per family, so optimistic):

| body | every step but fill | Fill Light +0.5 / +1 | before |
|---|---|---|---|
| SD9 | 0.4–2.1 | 0.7 / 1.6 | 8.1 / 14.1 |
| SD10 | 0.3–2.8 | 1.7 / 2.9 | 5.4 / 10.7 |
| SD14 | 0.2–2.0 | 2.1 / 4.3 | 16.9 / 31.9 |

A second body per family is the validation still to run; dp2 Quattro is unchanged on the shared model.


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

**Refit on all photos (20 October 2026), shipped for Fill +0.5 and +1.** A black-box study
(`tools/fillstudy`) found the earlier "floor" was partly an artefact of step-wise bins; with the
model's own smooth interpolation a per-photo fit reaches ~3 at +1, so the model class is right and
the shared coefficients were the weak part — fitted on 2–3 photos. Pooled over all photos and
scored leave-one-out, in the same metric as the shipped coefficients (which had seen most of them):

| | SD1 +0.5 | SD1 +1 | SD15 +0.5 | SD15 +1 |
|---|---|---|---|---|
| shipped | 5.62 | 10.37 | 3.88 | 7.43 |
| refit, held out | **5.01** | **9.18** | **3.07** | **5.86** |

The Go analyser on the app's real code agrees (in-sample now): +0.5 5.95 → 4.47 (SD1), 5.28 → 4.00
(SD15); +1 11.04 → 8.44, 9.64 → 7.44; SDIM0046 +1 19.8 → 9.3, SDIM0048 16.8 → 8.2; two photos a
little worse (SDIM0092 11.1 → 12.5, SDIM0031 8.9 → 9.4). The +0.25/+2/−1 steps were **not**
changed: they have no unseen photos, and there the shipped (in-sample) coefficients beat a held-out
refit, so a gain cannot be shown. A Gaussian surround did no better than the guided one.

**Frame mean, SD1 (20 October 2026), shipped.** The per-photo strengths of the local terms track
the image's mean brightness (`fillstudy9.py`); adding a (frame mean − pixel) term per brightness
bin, refitted per step and kept only where the held-out refit beats the shipped coefficients
(`fillrefit2.py`): Fill +0.5 4.04 → 3.17, +1 7.50 → 6.09, +2 16.40 → 14.79 held out (+0.25 and −1
unchanged). Go analyser on the real code, all eight SD1 photos (now in-sample): +0.5 4.47 → 3.21,
+1 8.44 → 6.29, +2 17.68 → 11.78 (SDIM0046 +2: 31.5 → 7.2). The SD15 is unchanged: the term did
not help its five photos.

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

### 4. ~~More pairs for the SD1 raw mapping~~ — done (21 October 2026)
60 more neutral exports (35 SD1, 25 SD15; `spp-references.ps1` in the calibration folder, picked
for variety in scene, ISO, MED/HI and lens) and an app-faithful `fit-raw` (developed, turned and
rendered as the app shows each photo): held out, SD1 4.70 vs the previous base's 4.85 (49
photos), SD15 5.84 vs 6.17 (29), shipped in v0.4.58. A separate SD1 MED base did not pay (4.90 vs
4.38 on the 7 MED photos).

Earlier note: seven A00 pairs then. 15–20 photos (varied light, WB, ISO, medium-res files) would steady the
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
