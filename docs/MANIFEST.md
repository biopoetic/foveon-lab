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
photos the fit didn't see; under ~3 is invisible.

### 1. Match, then outmatch SPP

| Goal | Done when | Now (October 2026) |
|---|---|---|
| Raw decoding | Every X3F generation, native, bit-identical to reference | ~90% — native Go, value-identical to x3f_extract on 60+ files |
| Colour & tone vs SPP | ≤ 3/255 held out, every family | Every body has its own raw base fitted on SPP's neutral exports (~260 references). Held out: SD1 4.7, DP Merrill 5.7, SD15 5.8, DP1/DP2 5.8; Quattro 9.5, and 6.2 on the regression check since the per-model exposure (v0.4.62). Sliders: 0.3–4.7 on six SD1/SD15 photos (mean ~1.5); white balance (W02/W06) 0.7–3.0 |
| Older cameras | SD9 / SD10 / SD14 / Quattro measured against SPP | **Done to ~9–10:** own raw bases since October 2026, held out SD10 9.5 (19 photos), SD14 9.7 (25), SD9 10.6 (13), from several bodies each (Sigma's 2003–05 sample galleries via the Internet Archive and other public samples); Auto white balance from the camera's own measurement (AutoRGBNeutral) |
| Sharpening & detail | Matches SPP's default per ISO and generation | Done for SD1 Merrill, SD15 and the DP Merrills (their own finer mask; ISO 100 within ~1% of SPP per band) |
| Noise reduction | Beats SPP side by side at ISO 1600+ | Detail: we keep more of the ISO 100 frame's texture than SPP on the DP1 Merrill series (ISO 1600 0.83 vs 0.71, ISO 6400 0.64 vs 0.40) and on the dp2 Quattro. Colour noise: SPP still cleaner at ISO 1600+ (colour error vs ISO 100, 4.7 vs 2.9 at 1600). Colour holds like SPP's across the ISO range (Quattro cast fixed in v0.4.63) |
| Full-spectrum IR | Calibrated per generation | Merrill only |
| DNG with SPP colours | The hand-off to other editors opens looking like SPP | ~85% — SPP-neutral profile embedded, not yet checked in a real editor |

### 2. A complete film simulator

| Goal | Done when | Now (October 2026) |
|---|---|---|
| B&W film & darkroom | The makers' curves, developers, push/pull, papers and grades | 9 films with developers and push, Ilford paper grades 00–5 |
| Colour film | The makers' curves, sensitivities and dyes for slides and negatives, checked against real slides and prints | 7 films (5 slides, 2 negatives), 2 papers, colour darkroom (Y/M/C filtration, print exposure) — not yet checked against real slides or prints |
| Grain & halation | Each film's own granularity and halation, per format | Grain from each film's RMS (locked to the film, not a dial); halation modelled |
| Lens filters | Physical filters through each sensor generation | Named real filters: the Wratten set on Kodak's measured transmittance (nine of ten; Wratten 47 approximate), brand filters by model (B+W, Hoya, Cokin, Tiffen, K&F, Kolari) on measured SCHOTT glass or published specs; neutral-density and graduated-ND products as density |
| Other cameras' files | Raw files from other cameras into the same simulator | JPEG / TIFF / PNG only, no other raws yet |
| Speed | Instant sliders | GPU preview, ~60 fps (film included) |

## Non-goals

- Competing with Lightroom, Capture One or other editors — general editing goes through the DNG
  export.
- A catalogue or photo-management app.
- Cloud features or accounts.
- Imitating SPP's interface.

## See also

- [Accuracy roadmap](accuracy-roadmap.md) — the detailed "match SPP" measurements and next steps.
