# Foveon Lab — manifest

## Mission

Get the most out of Foveon X3F files, for every Sigma generation, without Sigma Photo Pro: first
SPP's own look, then more than SPP can give.

## Principles

1. **SPP is the gold standard.** First match it, measured against SPP's own exports and held out.
   Then beat it, and only where a measurement shows it.
2. **First principles.** Silicon physics, the camera's own calibration data and the makers'
   published data, rather than guesses or hand-tuned looks. Predict, then measure.
3. **Honest numbers.** Every claim is measured on data the fit didn't see; failures and withdrawn
   claims stay on record.
4. **Black-box only.** SPP is never decompiled. Ported code is credited (x3fuse-core, Kalpanika).
5. **One self-contained, local app.** No cloud, no runtime dependencies; Windows, macOS and Linux.

## Goals and where we are

Updated with each release. Errors are mean absolute difference per pixel and channel (0–255) on
photos the fit didn't see; under ~3 is invisible.

| Goal | Done when | Now (October 2026) |
|---|---|---|
| Raw decoding | Every X3F generation, native, bit-identical to reference | ~90% — native Go, value-identical to x3f_extract on 60+ files |
| Colour & tone vs SPP | ≤ 3/255 held out, every family | SD1 ~4, DP Merrill ~4, SD15 ~6 |
| DNG with SPP colours | Opens in Lightroom / Capture One looking like SPP | ~85% — SPP-neutral profile embedded, not yet checked in a real editor |
| Noise reduction | Beats SPP side by side at ISO 1600+ | SPP still wins |
| Sharpening & detail | Matches SPP's default per ISO and generation | Done for SD1 Merrill and SD15 |
| Older cameras | SD9 / SD10 / SD14 / Quattro measured against SPP | Decoded, not yet measured |
| Speed & UI | Instant sliders | GPU preview, ~60 fps |
| Full-spectrum IR | Calibrated per generation | Merrill only |
| B&W film & darkroom | Makers' curves, developers and papers | 9 films with developers and push, Ilford paper grades 00–5 |
| Colour film | Makers' dyes and curves, checked against real slides and prints | 5 films, 2 papers — not yet checked against real slides or prints |

## Non-goals

- A catalogue or photo-management app.
- Cloud features or accounts.
- Imitating SPP's interface.

## See also

- [Accuracy roadmap](accuracy-roadmap.md) — the detailed "match SPP" measurements and next steps.
