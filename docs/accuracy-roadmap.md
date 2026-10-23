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

## Beyond SPP: high-ISO noise (21 October 2026)

The SD1 ISO series (SDIM0358-0363, one kitchen scene at ISO 200-6400, f/5.6) exported neutral
from SPP and rendered neutral by Foveon Lab at full size (v0.4.58), each pipeline judged against
its **own** ISO 200 frame (so calibration differences cancel). Noise measured on the flattest
15% of the centre (8-bit units; luminance: residual from a 5 px mean; colour: residual of each
channel minus luminance from a 15 px mean):

| ISO | luminance SPP / ours | colour SPP / ours |
|---|---|---|
| 200 | 2.72 / 2.79 | 2.68 / 2.84 |
| 400 | 3.99 / 3.69 | 3.57 / 3.57 |
| 800 | 3.85 / 3.51 | 3.16 / 3.57 |
| 1600 | 4.35 / 3.97 | 4.92 / 4.12 |
| 3200 | 7.16 / 6.99 | 8.81 / 4.20 |
| 6400 | 10.25 / 8.26 | 15.61 / 5.04 |

At ISO 6400 Foveon Lab has ~20% less luminance and ~3x less colour noise; a 100% crop shows SPP's
magenta/green blotches gone and the wood grain intact. Overall dE2000 to the own ISO 200 frame is
4-7% lower than SPP's at every ISO, but SPP is closer in colour alone at ISO 6400 (9.08 vs 9.90:
ours is a little pinker / less saturated there).

**Caveats.** Less noise is only better if detail survives, and this series cannot measure detail:
its ISO 200 frame is motion-blurred (1/8 s), so the "detail kept" slope is ~0 for both. dE to a
reference rewards smoothing (the reason an earlier "beats SPP" claim was withdrawn). A tripod
series of a detailed scene, exported from SPP, would settle detail; until then the claim is
"less colour noise at high ISO, detail by eye comparable".

**Tripod studio series (21 October 2026).** The studio still life (DP1 Merrill DP1MhSLI and dp2
Quattro DP2QhSLI, ISO 100-6400) settles detail, and it qualifies the claim above:

- **DP1 Merrill: SPP is better at every ISO.** It keeps more detail from ISO 800 up and holds
  its colour steady across the series.
- **dp2 Quattro: we keep more detail, but our colour drifts** green/yellow as ISO rises (ISO 6400
  vs our own ISO 100: a* -16, b* +11; SPP's drift is small). Not caused by the denoiser (see
  below); still open.

Part of the Merrill drift was the denoiser. Near black it added the denoised luminance back
equally to all three layers. Once the white balance is applied (DP1 Merrill gains ~2.0 / 1 /
0.6), equal amounts are no longer grey, so the cast grew with ISO. It now adds them back in a
neutral grey's proportions (`DenoiseOptions.Grey`, the inverse WB gains). Results:

- **DP1 Merrill vs SPP at the same ISO:** area error at ISO 3200 fell 6.13 -> 4.62 and at ISO
  6400 9.10 -> 7.91. ISO 100-400 is unchanged.
- **DP1 Merrill drift at ISO 6400:** a* -8.0 -> -6.7.
- **spp-check over all 187 photos:** 6.623 -> 6.584, with 16 photos better and 1 worse
  (SDIM0369, SD1 at ISO 6400, +0.66; its series neighbours improve 0.3-0.9).
- **dp2 Quattro:** unchanged.

Rejected while looking: using the X3F's PGain and the ISO 100 colour matrices / gains (all
worse), and the DFSOffset black level (already in the black).

**dp2 Quattro drift: where it comes from (open).** Measured on 32 px block means in linear light
(so noise averages out before Lab), ISO 6400 vs the frame's own ISO 100: SPP a* -3.1 b* +1.1; ours
-15.1 / +10.9, and -9.9 / +8.6 with noise reduction off. So about two thirds is already in the raw
data and a third comes from the denoiser. The denoiser part is not its luminance recombination,
detail restore or chroma restore (switching each off changes nothing).

In the raw data, regressing the lower layers on the top layer (16 px blocks, same scene) shows them
gaining an offset as the gain rises. ISO 200, 400 and 800 record the same signal level, yet at 800
both lower layers carry ~5 DN more. By ISO 6400 their slopes are also ~4% higher. The bottom layer
reads mostly red and the middle mostly green, which matches the cast.

The camera's own auto white balance is fooled by it too (4726 K at ISO 400, 4232 K at 6400), but a
fixed white balance does not help (6400: a* -13.1, b* +15.8).

The dark shields disagree for the lower layers: the rows above and below the image read up to
7 DN higher than the side columns at ISO 6400, and the columns dominate the mean. Taking the rows
only cuts the yellow shift (+10.9 -> +7.9) but deepens the green. spp-check Quattro is unchanged
(9.184 -> 9.185) and the ISO 6400 frame is worse (+1.12), so not shipped.

Unused CAMF candidates that may carry the per-ISO correction: PreCaptureOffset (-152 / -125 / 0
per layer), OBGAIN_* and EstimatedISOreference. AGain is constant, so it cannot cause an
ISO-dependent shift.

**SD14 (21 October 2026, resolved the same day).** Resolved: 24 more SD14 raws turned up (Carl
Rytterfalk's 2007-08 files, recovered from the Internet Archive). The SD14 now has its own base,
held out 9.7 on 25 photos; the DP base averaged 19.0 on the same photos. History: The SD14 now uses the DP compacts' raw base (same sensor generation):
31.9 -> 8.4 and 31.5 -> 15.6 on its only two SPP pairs. Both still lean magenta (a* +4.6 and +15).
Tried and rejected:
- **Refitting the DP base with 9 more DP2 photos.** They come from kronometric.org and are mostly a
  tungsten card exposure bracket. All 30 DP photos got worse (7.78 -> 7.91 without the SD14 pairs,
  8.28 with them). Out of sample the SD14s got worse too: 11.8 / 16.2.
- **A per-channel SD14 gain, fitted on one photo and tested on the other.** The two photos disagree:
  0.96 / 1 / 0.94 vs 0.54 / 1 / 0.67.

It needs ~10 SD14 photos with SPP exports. No more public ones were found: raw.pixls.us and
rawsamples.ch have one each, and Imaging Resource's raws are gone.

**DP1 Merrill studio series, measured with the shipped options (22 October 2026).** `tools/isocmp`
used to call `Denoise` without `ISO` and `Grey`, so it measured a pipeline the app does not use (no
ISO-based colour radius, no neutral-grey proportions near black). Corrected, each pipeline against
its **own** ISO 100 frame (fitted dE2000):

| ISO | SPP | ours | render shift ours / SPP |
|---|---|---|---|
| 1600 | 5.67 | 9.73 | 5.53 / 6.09 |
| 3200 | 8.96 | 10.97 | 8.26 / 10.28 |
| 6400 | 12.23 | 12.39 | 9.62 / 14.93 |

So at 6400 we now match SPP's noise and beat its colour drift, at 3200 we are 1.2x, and at 1600 we
are 1.7x. The 100% crop shows the remainder: chroma speckle in the black bottle and the cloth that
SPP smooths away.

Knobs, tried on the corrected pipeline (ISO 1600, baseline 9.73):

| change | dE |
|---|---|
| detail restore off | 11.45 |
| luma wavelet threshold K = 3 | 9.88 |
| colour eps x2 | 9.89 |
| colour radius 48 px (after the fix) | 10.23 |
| colour radius 64 px (before the fix) | 15.76 |
| per-pixel colour eps (before the fix) | 13.19 |

**The same filter in the app, judged by spp-check (22 October 2026).** Wired into the develop path at
ISO 800 and up, it makes spp-check *worse*: six photos worse, none better (DP1MhSLI0800 +1.13,
1600 +0.95, 3200 +0.63, DP3_05 +1.08, DP3_06 +0.79, DP3_04 +0.53; group mean 6.846 -> 7.0+).
So the isocmp gain (9.73 -> 6.49) was the metric rewarding smoothing: SPP keeps more chroma than the
filter leaves. spp-check is the arbiter for this, and the filter is rejected.

A 5x5 median on the chroma differences (same luminance, speckle removed the classic way) was also
tried and rejected: ISO 1600 9.73 -> 10.09, ISO 6400 12.39 -> 13.22. The chroma noise is correlated
blotches, not isolated outliers, so a median keeps it.

**A post-matrix chroma filter (22 October 2026, prototype).** Guided by luminance on Cb/Cr in linear
sRGB, luminance untouched, applied after the raw base at ISO 1600 (isocmp, fitted dE):

| sigma (px) | dE | by eye |
|---|---|---|
| 2 | 6.49 | acceptable: noise gone, a slight pink cast on the caps |
| 4 | 4.21 | too much: colour bleeds across edges, caps go magenta |
| 8 | 3.41 | unacceptable |
| 16 | 3.14 | unacceptable |

So the "better than SPP" numbers at 4 px and up are the smoothing artefact the earlier caveat warns
about; at 2 px it is 6.49 against SPP's 5.67, close, and the crop keeps its colour. Two things it
exposed: (a) `isocmp` also omitted the app's shadow colour fix (`render.ShadowChromaFix`); adding it
makes the dE *worse* (1600 9.73 -> 14.57) because the comparison rewards a colourful shadow the
app deliberately mutes (SPP mutes too), so dE alone cannot judge that choice - `spp-check` must.
(b) The app's `shadowMute` (0.5) may be stronger than SPP's at 1600-6400; that is the next thing to
measure, with spp-check on high-ISO groups, not dE.

The defaults are the best of these, so the guided filter is at its local optimum. The remaining
1600-3200 gap needs a different chroma model (a perceptual-space bilateral, which is what SPP's
blotch-free look suggests) or noise reduction in the raw domain before the colour matrix.

**The shadow fix's tonal reach, not the denoiser (22 October 2026).** The DP1 Merrill studio series
(`DP1MhSLI0100-6400`), measured with noisebench against each frame's own ISO 100. Stage means
(per-stage linear RGB over the whole frame) show the denoiser leaves the frame's global colour
almost unchanged: at 6400 a* is -0.44 after the matrix, -1.15 after the shadow fix, -0.94 after the
raw base, against +3.99 at ISO 100 - so the large green drift of the crop (a* -7.4) is already in
the raw/matrix output, not made by the denoiser.

The denoiser's restore stages are not the lever either. Turning `restoreChroma`'s five bands off
entirely changes colour-only by 0.01-0.12 at 1600-6400 (4.66 -> 4.62, 7.02 -> 6.90, 10.91 -> 10.90)
and by 0.30 at 800 (2.98 -> 3.28), and leaves detail kept identical: the fine chroma bands are not
what survives. Turning both restores off improves 1600-6400 (4.29 / 6.30 / 9.45) but is worse at 800
(3.19) and costs real luminance detail (0.83 -> 0.72, 0.74 -> 0.58, 0.64 -> 0.47), so it is not
shipped.

The lever is `render.SmoothShadowChroma`'s tonal reach: `hi = 0.06 + 0.10*amount` reached linear
0.16 (sRGB ~112) when `amount` saturates at high ISO, so it desaturated real midtone colour (the
bottles and cloth of the studio scene). The reach now stays at 0.06; the radius still grows with the
measured shadow noise. Colour only (blurred), before -> after, with SPP's number:

| ISO | ours before | ours after | SPP | detail before / after | SPP |
|---|---|---|---|---|---|
| 800 | 2.98 | **2.93** | 2.50 | 0.99 / 0.99 | 0.90 |
| 1600 | 4.66 | **4.46** | 2.92 | 0.83 / 0.83 | 0.71 |
| 3200 | 7.02 | **6.31** | 6.44 | 0.74 / 0.73 | 0.56 |
| 6400 | 10.91 | **9.89** | 8.68 | 0.64 / 0.65 | 0.40 |

Ours is now below SPP's colour error at 3200 and detail stays above SPP's at every ISO. On the
series itself 3200 and 6400 improve by 0.09 / 0.13 and 800 / 1600 are 0.03 / 0.01 worse. `spp-check`
DPM over the 32 photos: **6.846 -> 6.835** (8 better, 20 slightly worse, 4 unchanged; the group
gain is DP3_07 -2.01 and DP1MHVFAB -0.43, while DP3_03 is 0.83 worse and DP3_01 0.46). The full
172-photo check also improves: **7.229 -> 7.206**, with Quattro **6.053 -> 5.945** (33 photos better,
8 worse), SD14 8.938 -> 8.953, DP 6.144 -> 6.148, SD9 8.746 -> 8.747, SD15 10.694 -> 10.691 and SD1
unchanged at 3.994; the one visible group cost is SD10 **8.826 -> 8.888**, almost all of it
SIGMA-PHOTO_IMG00177_MOD (+0.79). A smaller reach instead (`hi = 0.06 + 0.04*amount`, max 0.10) is
more even on DPM (**6.821**) but worse on the ISO series (2.96 / 4.54 / 6.58 / 10.39), so the
constant 0.06 ships. The noise-adaptive muting itself (`shadowMute` 0.5) is kept: turning it off at
6400 leaves colour-only at 9.89 and slightly improves the a* mean (-6.54 -> -5.90) and residual
spread (14.9 -> 14.4), so it is not the cause and was not touched.

**The other Quattro bodies (22 October 2026).** The samples on disk settle part of the handoff's task
2. dp0, dp1 and dp3 Quattro have sample photos, not ISO ladders (`sigma_dp*_quattro_NN` are different
scenes at ISO 100-200). **sd Quattro H does have a ladder** (sigma_sd_quattro_h_01..07, ISO 100-6400), but it changes
resolution across it - 6192x3480 at ISO 100-400, then 3096, 2064, 2064, 1548. `isocmp` now box-averages
the reference down to each frame's size (and scales the crop with it) instead of skipping, so the
whole ladder can be measured; the 800-6400 numbers therefore compare against a downscaled ISO 100,
which averages some noise away, so treat them as slightly flattering:

| ISO | SPP | ours | render shift ours / SPP |
|---|---|---|---|
| 200 | 3.60 | **2.19** | 2.31 / 3.71 |
| 400 | 4.49 | **3.07** | 3.30 / 4.66 |
| 800 | 5.91 | **4.05** | 5.05 / 6.50 |
| 1600 | 6.35 | **5.38** | 6.48 / 6.82 |
| 3200 | 7.98 | 8.00 | 9.56 / 7.92 |
| 6400 | 10.46 | **9.48** | 13.48 / 10.69 |

Ahead at 200-1600 and at 6400, level at 3200. The colour/tone drift still grows from 3200 up, the
same shape as the dp2 Quattro drift on the same sensor generation, and that is the open issue for
this body rather than noise.

(Correction: an earlier run showed these frames rendering near-black - a linear mean of 0.019
against SPP's 0.29 - and that was an artefact of the tool, not the decode: our render is always full
sensor size while SPP exports the reduced mode smaller, so the two crops covered different parts of
the scene. `isocmp` now brings the render to the export's size per frame before comparing.)

**Public ladders for dp0/dp1/dp3 Quattro (22 October 2026): none found.** What was checked:
Imaging Resource's dp0 Quattro page is a Preview with no lab samples and its dp1/dp3 "SAMPLES" pages
are 404; DPReview reviewed the dp2 Quattro only (which is why that is the one "hSLI" series on
disk) and its forum/studio pages block automated fetches; a 2015 DPReview thread linked 12 dp0
test X3Fs at ISO 200 on Google Drive, and that folder is gone (404); the Internet Archive has no
Foveon raw collections; rawsamples.ch is unreachable from here. Two further sources were checked:
**raw.pixls.us** (the CC0 raw library behind darktable/rawspeed) has a Sigma section with DP0
Quattro, sd Quattro and sd Quattro H but **exactly one DP0 file** (`/data/Sigma/DP0 Quattro/
_SDI0263.X3F`, the same photo already on disk) and no DP1/DP3 Quattro at all; the **Focus Numérique**
dp1 review had twelve downloadable X3F raw files at ISO 100-800, but its image host
(img1.focus-numerique.com) no longer resolves and the files were never archived. The only public dp1
Quattro ISO ladder found (seattlesteve.com, ISO 100-6400) is JPEGs from SPP exports, not raws, so it
cannot be compared against SPP's output. So the three bodies stay unmeasured at high ISO until someone shoots
a ladder with one (same scene, tripod, SPP neutral exports). sd Quattro H is the only Quattro ladder
on disk, and it changes resolution mid-series (see above).

**Quattro drift: a bottom-layer black offset is not the lever (22 October 2026).** The middle layer
already carries an ISO-dependent black (`quattroMiddleBlack`, half the SensorISO/100 term on the
sd Quattro H). Adding the same term to the bottom layer, from -0.5 to +1.0 of SensorISO/100, moves
the sd Quattro H ISO 6400 render shift only between 17.05 and 18.64 (baseline 17.13) and the fitted
dE between 6.51 and 6.59 (baseline 6.56) - i.e. nothing. So the drift is not a black offset on that
layer; the raw-data layer *slopes* (~4% higher by 6400, per the dp2 Quattro measurement above) are
the next thing to model.

**Per-layer gains too (22 October 2026).** Sweeping the bottom and middle layer gains from 0.96 to
1.04 on the sd Quattro H ISO 6400 frame moves the render shift only between 16.79 and 18.42
(baseline 17.13) and the fitted dE between 6.52 and 7.09 (baseline 6.56). So neither a black offset
nor a layer gain explains the drift. Blind sweeps of the Quattro decode are exhausted: the next step
has to be a diagnostic - compare ours against SPP per channel in linear light (offset vs gain vs
hue) on the sd Quattro H and dp2 ladders - and only then a model.

**Quattro drift: the top layer's black (22 October 2026).** `isocmp -drift` prints the mean linear
R/G/B of our render and of SPP against each pipeline's own ISO 100. On the dp2 Quattro ladder our blue
falls with ISO (ours/ref 0.63 at 6400) while SPP keeps 0.886, and red/green hold at ~1.0 - so the top
(blue) layer is over-subtracted at high ISO, not a hue rotation. Reducing the top layer's black helps
monotonically: at ISO 6400 the fitted dE goes 11.34 (none) -> 10.91 (-4 DN) -> 10.82 (-6) -> 10.78
(-8) -> 10.83 (-12) -> 10.99 (-16), an optimum near -7 DN. A model keyed on **CaptureISO** (delta =
0.11 x (CaptureISO/100 - 1), about 7 DN at 6400) reads 400 2.27 (SPP 3.14), 1600 5.30 (5.33), 3200
7.68 (6.87), 6400 10.93 (9.51): low ISOs untouched, 6400 better by 0.4. Not shipped - it is a constant
fitted on one scene per ISO and the gain is small; a second scene per ISO is needed for a held-out
check. SensorISO is the wrong key for it (it is ~1600 at CaptureISO 6400).

The same diagnostic found a separate bug on sd Quattro H: its lower-resolution frames render far too
dark (linear mean 0.019 against SPP's 0.29 at ISO 1600), so its 800-6400 rows are measuring that, not
a colour drift.

**sd Quattro H reduced-resolution frames: not a decode bug (22 October 2026).** A debug print in
`cameraLinearQuattro` showed the ISO 100 and ISO 1600 frames decode with the same raw geometry
(3328x2240 raw, 6656x4480 top, the same kept rectangle) and the same black within 15 DN
(260.9/259.7/259.1 against 275.0/272.4/268.0). The near-black render was the tool comparing crops of
different pixel sizes; with the render brought to the export's size the frames are fine. Probe
removed.

**The top-black model does not transfer between bodies (22 October 2026).** The dp2-fitted correction
(delta = 0.11 x (CaptureISO/100 - 1)) applied to the sd Quattro H makes ISO 6400 *worse*: fitted dE
9.48 -> 9.66 and render shift 13.48 -> 19.32 (1600/3200 unchanged). So the top layer's black offset
is body-specific and the dp2 gain was small anyway; shipping it would need a ladder per body, and
there is no second scene for a held-out check. Rejected.

**Quattro group baseline with the fixed tool (22 October 2026).** `spp-check -only Quattro` over the
49 Quattro photos on disk: **all 6.053**, area 4.455, shadows 5.744. The spread is the story - most
frames sit at 2-6, while the high-ISO frames carry the group: sd Quattro H ISO 6400 **17.82** (area
14.94, shadows 21.96) and dp2 Quattro ISO 6400 11.09 (area 9.39). Any future Quattro work should use
this as the regression baseline: the drift, not the base ISO, is what moves the number.

**spp-check baseline, every group (22 October 2026, after the tool fixes).** 172 photos:
**all 7.229**, area 6.239, shadows 5.860. By group: SD1 3.99 (2 photos), Quattro 6.05 (49),
DP 6.14 (28), DPM 6.85 (32), SD9 8.75 (12), SD10 8.83 (20), SD14 8.94 (25), **SD15 10.69 (4)**.
Use this as the regression anchor; SD15 is the weakest group on this set despite its model, which is
the next thing to look at there (the four photos may simply be hard ones - check before fitting).

**Why SD15 is the weakest group (22 October 2026): a warm cast, not brightness.** The four photos are
the kronometric SD15 set (SDIM0009-0012), scoring 10.35, 10.29, 11.11, 11.03 with ev -0.05 on each.
The channel means on SDIM0011 (`isocmp -drift` with the same frame as reference) show what it is:
ours R 0.4592 / G 0.3164 / B 0.2083 against SPP's 0.3499 / 0.3578 / 0.3119 - red **+31%**, green
**-12%**, blue **-33%**, a strong warm cast rather than a 0.05 EV brightness error (which alone would
be about 2 dE, not 10). The lens-less aperture-gain fix (`FnumberGainFact`) is already in the tree.
The CAMF white balance is *not* missing data: the file carries OvercastWBGain and OvercastCCMatrix,
the name maps ("Overcast"), and `WBTransform` already uses the per-illuminant
`WhiteBalanceColorCorrections` matrix. But **SPP's neutral export is not using the recorded
Overcast** - sweeping the WB name on SDIM0011 gives (R/G/B means):

| WB | R | G | B |
|---|---|---|---|
| Overcast (what we apply) | 0.459 | 0.316 | 0.208 |
| Sunlight | 0.436 | 0.325 | 0.240 |
| **Auto** | 0.406 | 0.320 | **0.357** |
| Shade | 0.476 | 0.314 | 0.173 |
| **SPP's export** | 0.350 | 0.358 | 0.312 |

Auto is much closer than the recorded setting (blue 0.357 against SPP's 0.312, versus 0.208), though
still red-heavy. Measured as dE against SPP's export, the four WB choices on that photo read:
Overcast **13.25**, Auto 12.98, **Sunlight 10.81**, Shade 15.25 - so SPP's neutral is closest to a
Sunlight-like balance, not the recorded Overcast. The SD15 model was fitted on Auto-WB photos, so this
set's mismatch is expected; even at the best WB the dE is 10.8, so the raw base's fit on Auto-WB
photos contributes too. Next: either apply SPP's WB rule (whatever it computes for the neutral
export) or keep WB_DESC != Auto photos out of the group's fit - the second is easy and honest.
**Done (22 October 2026):** `calib.FitRawBase` now fits on Auto-WB photos only (photos without a
WB_DESC count as Auto; the filter is skipped when fewer than three would remain).

**The SD15 refit cannot be honest yet.** All four SD15 photos on disk are the kronometric Overcast
set, so there is no Auto-WB photo to fit on: the filter is skipped and `fit-raw` fits the same four
photos it then evaluates. It reads 4.99 in-sample against the shipped base's 10.69 - a big apparent
gain, but not a held-out one (the cross-check in `fit-raw` reports the raw error on the fitted
photos). The refit was therefore **not shipped**; the code change stays, ready for the day an
Auto-WB SD15 photo (or a second SD15 set) exists.

**Measured the other way round (22 Oct 2026): the raw base is heavily white-balance dependent.** Three
Auto-WB SD15 photos (SDIM0973-0975, found on D:\Foto\Photos\SD15 after the Desktop\SD15 folder
disappeared) were exported and fitted: held-out raw 5.64 against the shipped base's 6.41 on those
photos. Applying that base and running `spp-check -only SD15` on the kronometric Overcast set made it
**10.69 -> 51.26** (all four photos 38-47 worse). So a per-group base cannot serve both balances:
the honest options are a base per white balance, or a WB-aware mapping, or keeping the Overcast photos
out of the SD15 group entirely. The refit was reverted; the Auto-WB fit filter stays for that work.

**Per-white-balance raw bases are implemented (22 October 2026).** `RawCorrection` carries a `wb`
field, `RawBaseFor`/`RawBaseFitted` take the photo's `WB_DESC` and prefer a `family|WB` entry over the
family's WB-less one, `fit-raw` tags a fit that used Auto photos only and merges by `family|WB`, and
every call site passes the balance. The shipped data is **unchanged**: with the SD15 Auto entry
installed the kronometric Overcast set stayed exactly where it was (10.694, all four photos), which is
the property the mechanism was built for.

**But SD15 itself needs a closer look before anything ships.** The Auto fit reads 5.64 against the
shipped base's 6.41 on its four photos - a 12% gain, under the project's 20% rule, so `useRaw` stays
false - and the wider `D:\Foto\Photos\SD15` set (33 photos that have SPP exports) sits at **12.92**
with individual photos at 41.7, 30.4, 26.7 and 23.6 dE and ev down to -1.26. That is not a base-fitting
problem on its face, so the photos were checked: the exports match the camera (2640x1760), and the
worst ones carry the same exposure metadata as the good ones (`CaptureExpComp` -0.31, EXPNET -0.30 on
SDIM1174, 1243 and 0973 alike). What differs is the scene: the bad ones are dark, `ev` down to -1.26,
so SPP's tone is lifting deep shadows far more than our SD15 tone curves (fitted on brighter sets).
That looked like a tone question - but the two tools disagree, so it is not settled. On SDIM1174
`isocmp -drift` (centre 1200x800 crop) gives ours 0.2209/0.2409/0.2083 against SPP's
0.2298/0.2373/0.2090: a 4% difference. `spp-check` reports 41.74 dE and ev -1.26 for the same pair.
One of them is measuring something else - most likely `spp-check` on photos outside the calibration
set pairs a different reference (a JPEG or another export) than the A00 file, or measures the whole
frame where ours falls off. The 12.92 average was **the Auto-WB base itself, not `-compare`** (corrected 22 Oct 2026 after a
controlled rerun). With the 4-photo `SD15|Auto` entry installed and no `-compare`, the same 33 photos
read exactly **12.923**; with the entry removed they read **5.196** (area 4.64, shadows 4.09), and
SDIM1174 is 5.41 with ev -0.02, matching `isocmp`. So the four-photo fit is badly overfit: applied to
the 13 Auto photos in the wider set it costs 12-54 dE, three of them photos it was fitted on
(SDIM0973 5.37 -> 29.52). `FitRawBase` had already refused it (`useRaw` false, 12% gain against the 20%
rule) - but `RawBaseFitted` (used by `camfread.go` and `spp-check`) applies a fitted base regardless of
`useRaw`, which is how a rejected entry reached the render. Two things follow: a per-WB entry must pass
the `useRaw` test before it is installed, and SD15 at **5.20 over 33 photos** is a healthy group - the
earlier "weakest group" reading was the four-photo kronometric set plus this overfit.

The `ref` column in `spp-check` is still worth keeping: it makes it obvious which file a row
compared against.

**SD15 needs no per-balance base (22 October 2026).** With all the SD15 exports on disk (33 photos, of
which 12 are Auto-WB) `fit-raw` gives a held-out error of **8.05** and the tool now accepts it
(8.05 against the camera JPEG's 12.05 clears the 20% rule) - but the shipped WB-less base reads
**5.25 on the same photos**. So an Auto-only refit is worse than what ships, the earlier 12.92 was the
four-photo overfit, and the honest decision is to keep the shipped base and add no per-balance entry.
The mechanism stays (`wb` field, `family|WB` keys, the useRaw guard) for a body where a fixed-WB set
really is the majority - SD15 is not one.

**No regression from the per-balance work (22 October 2026).** `spp-check -compare` over every group:
**172 photos 7.229 -> 7.229**, 0 better, 0 worse, so the `wb` field, the `family|WB` lookup and the
useRaw guard changed nothing that ships. The per-group table is unchanged too (DP 6.14, DPM 6.85,
Quattro 6.05, SD1 3.99, SD9 8.75, SD10 8.83, SD14 8.94).

Note the SD15 row there is 10.694 over 4 photos: the default scan only reaches the calibration desk, so
the 33 SD15 raws on `D:\Foto\Photos\SD15` are not in it. With `-photos D:\Foto\Photos\SD15` the same
group reads **5.196 over 33**, which is the number to quote.

**The worst photos in the set are non-neutral exports, not render errors (22 October 2026).** Sorting
`spp-check` by `all` puts KRON_PROCESSING_SDIM7824 (DPM) at 58.39 and RAW_SIGMA_DP1S (DP) at 17.97 on top.
Checked the first one properly: its `area@ev0` is **9.71** against `all` 58.39, i.e. the whole error is a
**-1.40 EV brightness difference**, and `isocmp`'s centre crop puts our render within 12% of SPP's per
channel. The name says it: it is a "processing" test shot, developed in SPP with settings that are not
the neutral ones the calibration set is supposed to use. So it (and the other high-`all`, high-`|ev|`
rows - SDIM1174 before it) belongs **out of the set**, not in a bug report. When reading the table,
`spp-check`'s `area@ev0` column is the one that separates "our tone is wrong" from "this export was not
neutral": a big `all` with a small `area@ev0` is an exposure/lift difference in the reference.

`spp-check` now says how much those rows distort a group, after the per-group lines:
"3 of 60 photos are non-neutral (|ev| > 0.25), averaging 26.25". On DP+DPM that is three photos
carrying 26.25 between them, so the DPM group's honest figure is about **4.8 rather than 6.85** and the
pair's is **5.4 rather than 6.52**. Quote the neutral-only figure when comparing pipelines; the
all-photos figure is what a user sees if their export settings differ from ours, which is a different
question.**Six of the 172 photos are non-neutral references (22 October 2026).** Listed by |ev|, with what
`area@ev0` says about each:

| photo | group | all | ev | area@ev0 | reading |
|---|---|---|---|---|---|
| KRON_PROCESSING_SDIM7824 | DPM | 58.39 | -1.40 | 9.71 | the reference is not neutral; drop it |
| RAW_SIGMA_DP1S | DP | 17.97 | -1.24 | **21.18** | **worse after the EV correction - a real error** |
| RYTTERFALK_SDIM0656 | SD14 | 15.85 | +0.39 | 8.75 | partly exposure |
| SIGMA-PHOTO_VW_BEETLE | SD10 | 13.60 | -0.32 | 6.08 | partly exposure |
| SIGMA-PHOTO_IMG00046_A | SD10 | 5.08 | +0.35 | 3.97 | partly exposure |
| KRON_DP2CARDX3FS_SDIM0686 | DP | 2.41 | +0.28 | 1.09 | mostly exposure |

So only one is a bad reference on its own; three are mild exposure differences in the export; and
**DP1s is a real render error** - 18 dE, and removing the -1.24 EV makes it *worse*, so it is not a
brightness story at all. The DP1s is the old DP1 body (not the Merrill). Checked its channels against SPP (same-frame `isocmp
-drift`): ours R 0.0937 / G 0.0138 / B 0.0197 against SPP's 0.0918 / 0.0739 / 0.0382. Red is right,
**green is five times too dark and blue twice**, a strong magenta cast - not an exposure story and not a
white-balance name (the photo records Sunlight and the DP1s has that gain set). A channel that is
nearly zero points at the black level: the green (and blue) dark shield is over-subtracted on this body
at ISO 50, or its matrix is the DP1's rather than the DP1s's. Printed the per-channel black for this file: **0.68 / 0.59 / 0.54** - essentially zero, so the DP1s
file's dark shield is **not found** and `blackLevel` falls back to nothing. That alone cannot explain a
dark green (no subtraction would make it *brighter*), but it says the body's CAMF layout is not what the
DP1/SD14 path expects, and the WB/matrix that goes with it may be the DP1's. Printed what is applied: `WBTransform` returns gains **0.7586 / 0.5205 / 0.5098** and the colour matrix
[4.19 -4.93 1.74; -1.59 3.91 -1.31; 0.73 -3.20 3.48] - i.e. the DP1s takes the **TRUE II branch**
(`WhiteBalanceGains` + `WhiteBalanceColorCorrections`), the same one the SD14/DP1 path uses. Green comes
out five times dark with that pair, so either the DP1s's CAMF holds the DP1's corrections, or SPP puts
this body on the older `WhiteBalanceIlluminants`/`WhiteBalanceCorrections` (camera-to-XYZ + WB) branch
instead. Checked which matrices the file actually carries: **only the `DP1_*` family** (`DP1_WhiteBalanceGains`,
`DP1_WhiteBalanceColorCorrections`, per illuminant) - so the DP1 fallback in `WBTransform` is already the
right branch, and it is applying the DP1s's own gains and corrections. No older-branch matrices
(`WhiteBalanceIlluminants`) exist in the file, so the older branch is not the answer.

What *is* in the file and unused: **`DP1_CP1_Matrix` (2x3 matrices) and `DP1_CP2_Matrix` (3x3)** - a
two-stage colour-processing pair that sits between the WB gains and the output, and our render applies
neither. A stage that is missing entirely would shift the channels exactly like this (green crushed,
red untouched), so the next step is to apply CP1 then CP2 after the gains and before the correction -
in whichever order and normalisation SPP uses - and re-read the channel ratios against SPP's
0.0918/0.0739/0.0382.

A numeric sanity check of the *order* we use now, for a raw pixel with equal channels (gains
0.7586/0.5205/0.5098, corrections whose rows each sum to 1): applying the gains and then the
correction gives **R:G:B = 1 : 0.10 : 0.44**, applying the correction first and the gains after gives
**1 : 0.68 : 0.67**; SPP's own ratios on this photo are **1 : 0.805 : 0.416**. Neither order matches,
and the equal-channel input is a crude stand-in for a daylight neutral (the real neutral has camera
channels in the inverse ratio of the gains), so this is a *pointer*, not a conclusion: the green end of
our output sits near order A's 0.10 (we measure 0.15) while SPP is at 0.8, which is the size of the
error a missing colour stage produces. Worth testing the two orders and CP1/CP2 on the real file before
changing anything.**Coloured specks in dark areas (22 October 2026, seen by the user).** `SDIM0095.X3F` (SD1 Merrill,
ISO 100, WB Shade, Vivid preset) in the accurate preview shows single-pixel green/red/blue dots over
the cat's dark fur. What the code says about them:

- bad pixels *are* repaired on this path - `camfread.go:113` calls `x3f.FixBadPixels(raw, c)` before
  `CameraLinear` - so these are pixels the CAMF `BadnessTable` does not list;
- the accurate preview denoises at `defaultNR = 0.3` (`accurate.go`), not at zero, so this is not a
  "no noise reduction" view;
- there is **no outlier/despeckle pass anywhere** in `x3f/` or `render/` (only the single `median`
  helper in `denoise.go`), so a lone high sample survives the colour matrix untouched;
- the Vivid preset then raises saturation, which turns each surviving outlier into a bright coloured
  dot instead of a dim one.

So the likely shape is a statistical outlier (a hot pixel the table does not know, or a cosmic-ray
style single sample) amplified by the matrix and the preset. The fix to try, in order:
1. count them first - in the camera-linear image of this frame, pixels whose channel deviates from its
   3x3 median by more than, say, 20% and 8x the local sigma; if there are hundreds, it is hot pixels;
2. an outlier rejection pass on camera-linear RGB before the raw base (median of 3x3 per channel,
   replace only the deviating channel above a threshold), kept off for smooth gradients;
3. validate with `spp-check` (the group numbers must not move) and visually on this frame in Vivid and
   in the neutral preset.

**The coloured dots come from the denoiser (22 October 2026).** Bisected with `speckdump_test.go`
(`FL_DUMP_PHOTO=... FL_DUMP_COLOR=... go test -run TestDumpPreview`), which renders the frame through
the accurate-preview path and writes `dist/preview-dump.png`:

| render | dots |
|---|---|
| `sRGB` (denoise at defaultNR = 0.3) | **yes** |
| `sRGB-nodenoise` (denoiser skipped) | **no - clean** |
| `sRGB` with `FOVEON_DN_NORESTORE=1` | **yes, more of them** |

So the decode is fine, the camera-linear outliers measure 520 and the colour matrix is not the
problem: the denoiser itself paints them, and its detail restore is not the cause (turning it off makes
it worse). The shape of the bug points at the chroma path at very low signal - near black a tiny
difference is a huge ratio - so the fix is to make the denoiser's chroma work vanish with brightness
(scale it by luma, or skip near-black pixels as `ShadowChromaFix` already does) rather than to reject
single samples. `render/despeckle.go` stays (it is harmless and cheap) but it is not the cure for this.

**Despeckling does not fix it (22 October 2026).** Tried the obvious cure three ways on SDIM0095: a
filter on the camera-linear image (before the denoiser), one at the denoiser's output over dark pixels
(luma < 0.15, then < 0.5 with a 25% threshold), and one on the Linear after the colour transform. All
three leave the dots. That is itself a finding: the dots are **not single-sample outliers relative to
their 3x3 neighbourhood** - they read as small vertical dashes and 2x2-ish clusters in the render, which
a median test cannot see because the median lands on the cluster - so they are produced by the
denoiser's structure (the a-trous luminance transform or the guided per-channel model), not by one hot
sample. The cure has to be inside `x3f/denoise.go` (where the local model is built), and the dump test
is the fast loop for it. Nothing was shipped: the probes were reverted and the tree is clean.

**DP1s: the obvious causes are ruled out (22 October 2026).** Tried, measured against SPP's
0.0918/0.0739/0.0382 on the same file:

| change | result |
|---|---|
| none (shipped) | 0.0937 / 0.0138 / 0.0197 - red right, green and blue far low |
| WB gains inverted | 0.0000 / 0.0732 / 0.0035 - green lands on SPP, red collapses |
| gain after the colour correction (GAIN·CC) | 0.0054 / 0.0483 / 0.0114 - worse |
| CP1/CP2 applied | not tried - the matrices are 0.92-1.08 and appear in neither x3fuse-core nor dcraw |

So no ordering or gain-direction fix works, and CP1/CP2 are too small to explain a factor of five. That
points at the **plane mapping itself**: the DP1s is the 2009 generation, and its raw planes may not be
the bottom/middle/top order the Merrill-era code assumes. Next session: print each raw plane's mean and
compare with SPP's channel means - if the planes' ratios are already wrong before any matrix, the decode
order is the issue, not the colour transform.**Render timing framework (23 October 2026).** `tools/bench.ps1` runs the stage summary and the
benchmarks on one photo and keeps the output in `dist/bench-<stamp>.txt`; `-Compare` prints the deltas
against an earlier run. Everything is measured, nothing is felt:

```
SDIM0095.X3F (4704x1568, bin 3)
  open+decode      747.8 ms    56%
  camera linear    170.9 ms    13%
  bin               10.6 ms     1%
  denoise          399.6 ms    30%
  to linear          4.6 ms     0%
  preview          998.0 ms
```

Micro benchmarks (15 MP SD1 frame, `-benchtime 1x`): preview 995 ms / 803 MB allocated, develop 417 ms,
camera linear 109 ms, denoise 201 ms / 339 MB, bin 12 ms, to linear 40 ms. The next two targets are
therefore the file read and decode (56%), and the denoiser's allocation churn (339 MB per frame is
mostly scratch buffers that could be pooled).

**Where the render time goes now (23 October 2026).** After the 12 s -> 1.29 s preview fix and the
denoiser's scratch pooling, a 15 MP SD1 preview reads: open 1 ms, **decode raw 435 ms (49%)**, bad
pixels 30 ms, camera linear 123 ms, bin 14 ms, **denoise 239-285 ms (32%)**, to linear 5 ms. The
denoiser pooling is measured (339 MB -> 253 MB, 243-306 ms -> 239-241 ms); the decoder's inner loop was
attacked by hoisting its per-pixel seed tests and the difference could not be seen through the
measurement noise, so it was reverted rather than claimed. The decoder is a stateful bit reader - the
accumulator chains along each row - so the remaining big win there would be a rewrite (word-at-a-time
refills, splitting the first two columns out properly), not a tweak. The honest next targets are
CameraLinear (123 ms for three full-frame passes) and reusing the decoded raw across views (it is
already cached per photo, so this is only about the first view).

## What "perfect" would need beyond this## What "perfect" would need beyond this## What "perfect" would need beyond this## What "perfect" would need beyond this## What "perfect" would need beyond this## What "perfect" would need beyond this

Pixel-identical output means reproducing SPP's own raw pipeline (demosaic-free Foveon colour
separation, its noise reduction, its local Fill Light), not mapping around it. That is a
research project — X3F raw → SPP-identical output — and only worth starting if steps 1–4 leave a
gap users actually notice.

**dp0 / dp1 Quattro: their own middle-layer black, k = 0.6 (22 October 2026).** Two complete
ladders that had sat unmeasured (same scene, f/8, ISO 100-6400, SPP exports) settle the borrowed
dp2 value for the compact Quattro bodies. The middle-layer black was swept per ISO as an extra
offset on top of the shipped rule (-8, -4, 0, +4, +8 raw units), with the full-size render, the
shipped noise reduction and raw base; drift is the 32 px block mean in linear light, each frame
against its own ISO 100, as a*/b*. Drift before -> after and SPP's own drift:

| body | ISO | before | after | SPP | error before -> after |
|---|---|---|---|---|---|
| dp0 | 200 | +0.4 / +1.1 | +0.4 / +1.1 | +0.4 / +1.0 | 0.08 -> 0.08 |
| dp0 | 400 | +1.0 / +1.1 | +0.4 / +1.5 | +0.4 / +1.0 | 0.61 -> 0.46 |
| dp0 | 800 | +1.2 / +2.8 | +0.2 / +3.4 | -0.8 / +3.6 | 2.20 -> 1.05 |
| dp0 | 1600 | +1.7 / +4.5 | -0.3 / +5.5 | -1.5 / +4.1 | 3.18 -> 1.84 |
| dp0 | 3200 | +2.4 / +5.4 | -1.4 / +7.2 | -2.4 / +2.5 | 5.65 -> 4.81 |
| dp0 | 6400 | +5.6 / +10.1 | -1.1 / +13.2 | -5.1 / +6.8 | 11.18 -> 7.53 |
| dp1 | 200 | -0.6 / +0.9 | -0.6 / +0.9 | -0.0 / +0.0 | 1.00 -> 1.00 |
| dp1 | 400 | +0.6 / +0.9 | +0.0 / +1.2 | -0.8 / +1.1 | 1.36 -> 0.80 |
| dp1 | 800 | +0.4 / +2.3 | -0.6 / +2.8 | -2.4 / +3.1 | 2.90 -> 1.76 |
| dp1 | 1600 | -0.4 / +4.4 | -2.4 / +5.4 | -3.1 / +3.5 | 2.91 -> 2.03 |
| dp1 | 3200 | +1.4 / +9.5 | -2.5 / +11.1 | -5.6 / +7.7 | 7.21 -> 4.60 |
| dp1 | 6400 | +3.8 / +15.2 | -3.1 / +18.2 | -6.8 / +9.6 | 11.97 -> 9.34 |

A single k times SensorISO/100, fitted over ISO 400-6400, is **0.63 on the dp0** and **0.59 on the
dp1**; the per-ISO optima are ~0 at ISO 200, ~1.3 at 400 and ~2.4-2.8 at 800-6400, so the k is
well determined and much lower than the dp2's 1.0. The old value over-corrected the compacts and
pushed their shadow drift *red* at the top (dp0 ISO 6400 +5.6/+10.1 against SPP's -5.1/+6.8);
**k = 0.6 ships for both bodies**. The residual b* at 3200-6400 (ours more yellow than SPP's) grows
as the middle black is reduced, while a* needs it reduced - the two pull opposite ways, so this is
the least-squares compromise, not a perfect match.

`spp-check -only Quattro`, 60 photos: **6.064 -> 5.979** (area 4.582 -> 4.490, shadows 5.554 ->
5.457), 8 photos better, none worse by more than 0.05 (the largest change the wrong way is +0.05).
dp0 6.010 -> 5.820 over 12 photos and dp1 7.342 -> 7.089 over 11; **SIGMA_DP1_QUATTRO_12 8.05 ->
6.92** and the ISO 6400 frames dp0_34 13.51 -> 11.67, dp1_20 13.83 -> 12.84. dp2, dp3, sd Quattro
and sd Quattro H are unchanged to the hundredth. dp3 Quattro and sd Quattro still have no ladder:
they stay on the dp2's k = 1.0.
