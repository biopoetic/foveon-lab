# Foveon Lab — manifest

## Mission

Two things, for Foveon X3F files of every Sigma generation:

1. **Match Sigma Photo Pro, then outmatch it** — SPP's own look without SPP, then more than SPP can
   give.
2. **A complete film simulator** — the Foveon sensor records each colour at every pixel through
   layers of silicon, the closest a digital sensor comes to film's stacked emulsion layers; Foveon
   Lab takes it the rest of the way, to film, development and the print, from the makers' own data.

General editing is not the aim: that is what the DNG export is for (Lightroom, Capture One,
darktable…). Other cameras' files can come in too, to go through the same film simulator.

## Principles

1. **SPP is the gold standard.** First match it, measured against SPP's own exports and held out.
   Then beat it, and only where a measurement shows it.
2. **First principles.** Silicon physics, the camera's own calibration data and the makers'
   published film data, rather than guesses or hand-tuned looks. Predict, then measure.
3. **Honest numbers.** Every claim is measured on data the fit didn't see; failures and withdrawn
   claims stay on record.
4. **Black-box only.** SPP is never decompiled. Ported code is credited (x3fuse-core, Kalpanika).
5. **One self-contained, local app.** No cloud, no runtime dependencies; Windows, macOS and Linux.

## Goals and where we are

Updated with each release. Errors are mean absolute difference per pixel and channel (0–255) on
photos the fit didn't see; under ~3 is invisible. The 183-photo `spp-check` below is a fixed
regression set, not all held out: six of its 181 sample-folder photos are non-neutral reference
exports, and the two SD1 files are the calibration pair. Held-out figures are marked as such.

### 1. Match, then outmatch SPP

| Goal | Done when | Now (2 November 2026) |
|---|---|---|
| Raw decoding | Every X3F generation, native, bit-identical to reference | ~90% — native Go, value-identical to x3f_extract on 60+ files |
| Colour & tone vs SPP | ≤ 3/255 held out, every family | Every body has its own raw base. 183-photo regression (2 Nov): **6.705** overall — DP 4.79, DPM 6.39, Quattro 5.62, SD1 3.63, SD9 8.74, SD10 8.87, SD14 8.77, SD15 10.69. Held out, the raw mapping reads SD1 4.70, DP Merrill 5.7, SD15 5.84, DP1/DP2 5.8 (Quattro's held-out fit is the older 9.5). Sliders: 0.3–4.7 on six SD1/SD15 photos (mean ~1.5); white balance (W02/W06) 0.7–3.0 |
| Older cameras | SD9 / SD10 / SD14 / Quattro measured against SPP | Own raw bases since October 2026. 2 Nov regression: SD10 8.87 (20 photos), SD14 8.77 (25), SD9 8.74 (12). Held out: SD10 9.5, SD14 9.7, SD9 10.6, from several bodies each (Sigma's 2003–05 public galleries and other public samples). Not done to ~3: the limit is tone, not a group correction |
| Sharpening & detail | Matches SPP's default per ISO and generation | Done for SD1 Merrill, SD15 and the DP Merrills (their own finer mask; ISO 100 within ~1% of SPP per band); the DPM/SD1 amounts were retuned to two thirds and Quattro got SPP's sharpening (1 Nov 2026) |
| Noise reduction | Beats SPP side by side at ISO 1600+ | Detail: we keep more of the ISO 100 frame's texture than SPP on the DP1 Merrill series (ISO 1600 0.83 vs 0.71, ISO 6400 0.64 vs 0.40) and on the dp2 Quattro. Base ISO: the <0.7 px band was 0.78–0.93 of SPP's energy, so DPM/Quattro use 0.15 denoise at ISO ≤400 (2 Nov). Colour noise: SPP is still cleaner at ISO 1600+ (colour error vs ISO 100, 4.7 vs 2.9 at 1600). Colour holds like SPP's across the ISO range (Quattro cast fixed in v0.4.63) |
| Full-spectrum IR | Calibrated per generation | Merrill measured on four real full-spectrum SD1 shots (dust protector out); Quattro, SD14/SD15 and SD9/SD10 are silicon-model predictions, labelled approximate where the IR look is chosen (2 Nov 2026) |
| DNG with SPP colours | The hand-off to other editors opens looking like SPP | Rendered DNG against our own render: 0.34/255 mean, 0.23 dE2000. Raw DNG tags pass DNG 1.4 on eight generations (SD9, SD10, SD14, SD15, SD1, DPM, DP, Quattro); portrait orientation was fixed 2 Nov (it was written as 1). The embedded SPP-neutral profile lands 2.1–4.4 dE2000 from our render on the other seven generations and 7.3 on the DP2, whose raw DNG keeps the file's uncorrected Auto white balance; AsShotNeutral cannot carry the DP2's post-tone balance (forcing it: 23.3 dE). darktable 5.6.2 opened all eight (no black/white warning, upright, right size) but ignores `ProfileToneCurve`, `ProfileHueSatMap`, `ForwardMatrix1`, `BaselineExposure` and `DefaultScale`, so its own sigmoid leaves 3.2–9.9 dE on the rendered DNG and 5.6–14.7 on the camera DNG. The rendered DNG is the hand-off that carries the look; the SD14 3:1 outlier now declares a 2× `DefaultScale`. Lightroom/Camera Raw, Capture One and Apple Photos are still to run |

### 2. A complete film simulator

| Goal | Done when | Now (2 November 2026) |
|---|---|---|
| B&W film & darkroom | The makers' curves, developers, push/pull, papers and grades | 9 fully modelled films with developers and push, Ilford paper grades 00–5 |
| Colour film | The makers' curves, sensitivities and dyes for slides and negatives, checked against real slides and prints | 15 films (5 slides, 10 negatives), 2 papers, colour darkroom (Y/M/C filtration, print exposure) — not yet checked against real slides or prints |
| Grain & halation | Each film's own granularity and halation, per format | Grain from each film's RMS (locked to the film, not a dial); halation modelled |
| Lens filters | Physical filters through each sensor generation | Named real filters: the Wratten set on Kodak's measured transmittance (nine of ten; Wratten 47 approximate), brand filters by model (B+W, Hoya, Cokin, Tiffen, K&F, Kolari) on measured SCHOTT glass or published specs; neutral-density and graduated-ND products as density |
| Other cameras' files | Raw files from other cameras into the same simulator | DNG first: Bayer CFA and linear DNG, uncompressed/Deflate/LZW/PackBits/lossless-JPEG, own colour matrices/AsShotNeutral, Malvar–He–Cutler demosaic, film looks; IR and layer mixes hidden (no Foveon layers), colour films on a colourimetric sRGB fit. Tested on synthetic Bayer DNGs and DNGs this app writes; no third-party camera DNG on this machine. CR2/CR3, NEF and ARW not yet |
| Speed | Instant sliders | GPU preview ~60 fps (film included); folder open 1.4–2.4 s (183 files, six connections), next photo 0.23 s when prefetched, preset grid 10–12 s; raw export Merrill 10.8 s, Quattro 17.5 s (2 Nov 2026, shared machine) |

## Non-goals

- Competing with Lightroom, Capture One or other editors — general editing goes through the DNG
  export.
- A catalogue or photo-management app.
- Cloud features or accounts.
- Imitating SPP's interface.

## See also

- [Accuracy roadmap](accuracy-roadmap.md) — the detailed "match SPP" measurements and next steps.
