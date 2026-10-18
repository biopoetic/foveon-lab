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
| Colour & tone vs SPP | ≤ 3/255 held out, every family | SD1 ~4, DP Merrill ~4, SD15 ~6 including Fill Light; without it, every slider measures 0.3–4.0 on three SD1 photos (mean 1.1, two never used for a fit) |
| Older cameras | SD9 / SD10 / SD14 / Quattro measured against SPP | Decoded, not yet measured |
| Sharpening & detail | Matches SPP's default per ISO and generation | Done for SD1 Merrill and SD15 |
| Noise reduction | Beats SPP side by side at ISO 1600+ | SPP still wins |
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
