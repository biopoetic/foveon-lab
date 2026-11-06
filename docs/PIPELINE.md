# The pipeline: X3F to 8-bit

Every stage a photo passes through, in the order the code runs it, for the
three consumers: the CPU render (`render.RenderOpt`), the GPU preview
(`web/gpu.js`, from a Base the server sent) and the export. The line numbers
are from the task-60 tree (base `44c61d4` plus the `film@wb:` change).

Colour-space names used below:

* **raw** — the decoded sensor samples (16-bit counts) before black/white
  normalisation.
* **camera-linear** — linear camera RGB, 0 = black, 1 = each layer's white,
  after `CameraLinear`; the white-balance gains have *not* been applied.
* **WB layers** — camera-linear times the white-balance gains, with the colour
  matrix set to identity: the sensor's three layers as the light balanced
  them. This is what a colour film and an IR look consume.
* **linear sRGB** — `CC · diag(Gain) · camera-linear` (or an sRGB JPEG
  decoded to linear light).
* **linear output** — the render's working linear sRGB before encoding.
* **density** — the colour film model's `-log10` dye density, inside
  `colorFilm.apply`.
* **encoded sRGB** — gamma-encoded sRGB, 0…1 or 8/16-bit.

A "layer source" means the Base is WB layers, not linear sRGB: a colour film
(`p.Film` with a colour model) or an IR look (`p.Filter` = `ir-…`).

---

## 1. Source: X3F to Base (shared by all three consumers)

These stages run inside `rawLinearFor` and its callees; the Base they produce
is what `render.RenderOpt`, `GPUBase` and the export all consume.

| # | Stage | What it does | Function / file:line | Space in → out | `preset.Params` fields | Why here |
|---|-------|--------------|----------------------|----------------|------------------------|----------|
| S1 | Decode / repair | Decode the X3F raw planes, parse CAMF, fix bad pixels | `File.DecodeRaw` `x3f/raw.go:62`; `FixBadPixels` `x3f/badpix.go:14`; cached in `cachedRaw` `camfread.go:144-189` | raw → raw | none | First possible stage; the decode is cached because the preset grid develops one file several times (`camfread.go:128-134`). |
| S2 | White balance + black/white + spatial shading | Choose a camera balance, subtract black, scale to per-layer white, apply the spatial gain; the WB gain/matrix stays in `CamTransform` for S5 | `goDevelopFor` `camfread.go:191-245`; `developWB` `camfread.go:897`; `x3f.CameraLinear` `x3f/develop.go:45`; `WBTransform` `x3f/transform.go:74` | raw → camera-linear | `WhiteBalance` (camera code 2–7; `cameraWBName` `camfread.go:911`). A colour film adds it as `film@wb:<name>` (`layersSource` `accurate.go:698-710`, `splitFilterWB` `accurate.go:349-357`) | The balance is a property of the light, so it is selected at development; for a layer source it is applied later as the per-layer gain (S5) so the film sees balanced light. |
| S3 | Clip highlights | Pull clipped layers to white (a look's white for IR) so a clipped sky is not yellow/red | `x3f.ClipHighlightsTo` `x3f/develop.go:166`, called `camfread.go:335-339` | camera-linear → camera-linear | colour film / IR look choose the white via `render.IRWhite`; otherwise neutral | Before the denoiser: its chroma filter would spread the clipped layers' false colour into neighbours (`camfread.go:333-335`). |
| S4 | Bin, despeckle, denoise | Box-bin the raw, repair hot/dead samples at high ISO, run the Foveon denoiser | `reducedView`/`binCam2`, `render.Despeckle`, `x3f.Denoise` `camfread.go:362-377`; `x3f/denoise.go:296` | camera-linear → camera-linear | none (strength is `defaultNR` or the export's `-dn`) | Binning first: it averages noise, and the denoiser measures what is left (`camfread.go:353-360`). Despeckle is before the denoiser because its thresholds were fitted in camera space (`camfread.go:341-352`). |
| S5 | Camera matrix | Apply `CC · diag(Gain)`: either to linear sRGB, or — for a layer source — with `CC = I`, to WB layers | `render.FromCamLinear` / `FromCamLinearSize` `camfread.go:385-390`; `render/camraw.go:112,127` | camera-linear → linear sRGB **or** WB layers | `Film` (colour model) / `Filter` (`ir-…`) select the layer source (`layersSource`) | After denoise so the matrix and the box downscale see cleaned data; the downscale is exact in linear light (`render/camraw.go:107-111`). |
| S6 | Shadow chroma noise | Smooth low-frequency chromaticity noise in the shadows, guided by luminance | `render.ShadowChromaFix` `camfread.go:408-413`; `render/shadowchroma.go:213` | linear (sRGB or layers) → same | none (noise reduction > 0; skipped for IR unless `sceneWB`) | Needs the colour image; the per-layer denoiser leaves the blotches the pass removes (`render/shadowchroma.go:10-31`). |
| S7 | Quattro mottle | Wide, edge-aware chroma smoothing at ISO ≥ 6400 | `render.MottleChroma` `camfread.go:420-422`; `render/mottle.go:21` | linear → linear | none | The per-layer denoiser leaves 8–32 px blotches; a wide low-chroma pass removes them without touching saturated objects (`render/mottle.go:1-10`). |
| S8 | Model exposure | Per-body brightness gain relative to the calibration group | `render.ModelExposure` `camfread.go:426-432`; `render/modelexposure.go:35` | linear → linear | none; **skipped for a layer source** (`irWhite != nil`) | Before the raw base so the base's measured neutral response sees the body's own exposure. The comment gives the IR reason (those looks model the layers themselves, `camfread.go:424-425`); a colour film takes the same branch and skips it — **no separate reason found** for that. |
| S9 | Upright / aspect | Give a medium or cropped readout its frame shape and rotation | `uprightRaw` `accurate.go:667-680` | linear → linear | none (photo metadata) | Every raw path needs the frame; a 4704×1568 medium file would otherwise be stretched (`accurate.go:664-666`). |
| S10 | Raw base (SPP neutral) | Map the developed raw onto SPP's neutral look: optional Monochrome layer block, 3×3 matrix + per-channel curves, DP2 JPEG balance, bright-chroma denoise, AdobeRGB handling | `rawCorrectWith` `accurate.go:412-454`; `RawCorrection.Apply` `render/rawbase.go:230`; `rawJPEGBalance` `accurate.go:533`; `render.BrightChromaDenoiseFor` `render/brightchroma.go:29`; `render.ApplySPPAdobeRGB` `render/adobergb.go` | linear sRGB → linear sRGB | none; **skipped entirely for a layer source** (`accurate.go:389-393`) | Presets start from SPP's neutral rendering (`render/render.go:536-537`), so the raw base must precede them. The bright-chroma pass is after the base because the base's per-channel curves leave that mottling (`accurate.go:443-446`). |
| S11 | Geometry | Straighten and crop at source resolution | `render.ApplyGeometry` `render/geometry.go:52`, called `main.go:730` | linear → linear | none (photo crop/turn) | Straighten/crop before the view downscale (`main.go:729`). |
| S12 | View downscale | Box-filter to the requested width | `Linear.Downscale` `render/render.go:396`, called `main.go:730` | linear → linear at view size | none (`w` query) | The view is rendered at the size it is shown; a box filter avoids aliasing. |
| S13 | Base | Build the Fill Light mask, the two surrounding maps and the frame mean once | `NewBase` `render/render.go:532`; `Base.fields` `render/render.go:513-529` | linear → Base (linear + mask/surrounds) | `FillLight` and `Exposure` use them; the mask is built for every Base | Fill Light needs a blurred brightness mask; computing it once per Base keeps it out of the per-render loop (`render/render.go:503-510`). |

**Skip S10:** a colour film or IR look never gets the raw base. The base maps
SPP's neutral output, not sensor layers, and it would destroy the layer
ratios the film model needs (`accurate.go:389-393`).

---

## 2. CPU render (`render.RenderOpt`)

Entry: `Render` `render/render.go:637` → `RenderOpt` `render/render.go:643` →
`renderIntoOpt` `render/render.go:715`.

| # | Stage | What it does | Function / file:line | Space in → out | `preset.Params` fields | Why here |
|---|-------|--------------|----------------------|----------------|------------------------|----------|
| R1 | Build the per-render op | Normalise colour adjust, exposure, saturation, layer filter, film tone, colour film, the measured model (WB/mode/tint/pre/post/Fill), measured saturation; bake the pre/post tone LUTs | `newPixOp` `render/render.go:847-987` | params → `pixOp` (no pixels) | all of them; the branch is chosen here | Once per render, and the LUTs are baked from the measured steps so the pixel loop is table lookups. |
| R2 | Prepare spatial parts | Density setup, SPP mono grain, film grain op, Fill Light fields | `densitySetupFor` `render/render.go:719`; `newMonoGrain` `render/render.go:723`; `newFilm` `render/render.go:728`; `Base.fields` `render/render.go:732-735` | Base metadata → ops | `Grain`/`Film`/`NoGrain`/`Halation`/`FilmWidth`/`MonoGrain*`/`ND`/`GND*`/`FillLight` | Grain fields and the Fill mask are spatial and image-sized, so they are built once before the pixel loop. |
| R3 | Halation | Add the light the film base reflects into the red-sensitive layer | `film.addHalo` `render/render.go:749`; `render/film.go:522` | linear → linear | `Halation`, `FilmWidth` | It is light in the emulsion, so it must be added in linear light before tone (`render/film.go:52-58`). |
| R4 | Undo camera styling | Undo the in-camera colour-mode matrix carried by a camera JPEG | `pixOp.unstyle` `render/render.go:1406-1408,1310-1320` | linear → linear | none (`Compensation.Undo` from the JPEG's CAMF) | The preset must start from SPP's neutral; the camera's mode is undone first (`render/render.go:536-537`). |
| R5 | Layer filter | Lens filter, IR look, camera colour mode and layer sliders, in layer space | `pixOp.layerFilter` `render/render.go:1409-1411,1300-1307`; built in `setLayerFilter` `render/render.go:1152-1271` / `setIRLook` `1275-1297` | linear → linear | `Filter`, `LayerBottom/Middle/Top`, `CamMode`, `Mono` | A lens filter sits in front of the film/sensor, so it acts before the film or the measured model (`render/render.go:1153-1158`). |
| R6a | Measured model | SPP's measured rendering: WB mapping, colour-mode mapping, colour adjust, measured pre-tone, Fill Light, measured post-tone, measured saturation; monochrome base tone when SPP's Mono tab is on | `pixOp.applyMeasured` `render/render.go:1341-1380`; selection `render/render.go:934-985` | linear → linear | `WhiteBalance`, `ColorMode`, `R/G/B`, `Exposure`, `FillLight`, `Highlight`, `Blackness`, `Contrast`, `Saturation`, `Mono*` | This is SPP's own model; it exists only when no colour film is set. WB must come before the tint and the tone curves because it is a light balance, not a look. |
| R6b | Film / formula model | Exposure, then either the colour film (layers → film curves/3D table → linear sRGB) or the formula tone chain (Fill, monochrome base, film tone, highlight/black/contrast, saturation, Portrait/Landscape) | `pixOp.apply` `render/render.go:1415-1499` | linear → linear | `Film`, `Paper`, `Grade`, `Developer`, `DevTime`, `Push`, `CCY/CCM/CCC/PrintExp`, `Exposure`, `Highlight`, `Blackness`, `Contrast`, `Saturation`, `Mono`, `ColorMode` | A colour film is the look: it must replace SPP's measured rendering, not be stacked on it (`render/render.go:933-934`). The film is exposed before the tone steps because the exposure is the camera's and the tone steps are the print's (`render/render.go:1442-1443`). |
| R7 | SPP Shading Effect | Radial darkening about the whole frame plus the measured −1/255 | `pixOp.monoShade` `render/render.go:751,1515-1538` | linear ↔ encoded (gain applied in encoded sRGB) | `MonoShadeRadius`, `MonoShadeAmount` (only SPP's Mono tab) | SPP prints the shading on the toned grey; the gain is multiplicative on the encoded value (`render/render.go:1507-1514`). |
| R8 | Film grain | Density noise: per-pixel and spatially resolved octaves, tone-weighted, shared colour component | `film.grain` `render/render.go:752-754`; `render/film.go:781` | linear → linear | `Grain`, `GrainSet`, `Film`, `FilmWidth`, `Push` | Grain is in the developed emulsion, so it follows tone; the density noise is strongest in the mid-tones (`render/film.go:37-50`). |
| R9 | SPP mono grain | SPP's own monochrome grain, in the encoded domain, with its measured mean and tone weight | `sg.apply` `render/render.go:755`; `render/monograin.go:234` | linear → linear (computed encoded) | `MonoGrainSize`, `MonoGrainRough` | SPP's mono grain is an encoded-domain print grain, and it is used only when SPP's Mono tab is on (`render/monograin.go:231-233`). |
| R10 | Mono toning | Add one of SPP's nine constant 8-bit sRGB offsets | `pixOp.applyMonoTone` `render/render.go:756,1543-1553` | linear ↔ encoded | `MonoTone` | Measured as a constant offset at every tone, i.e. a print toning after grain (`render/render.go:1540-1542`). |
| R11 | Density filters | ND gain and GND gradient | `dens.gain` `render/render.go:757-759`; `render/density.go:54` | linear → linear | `ND`, `NDMetered`, `GND`, `GNDAngle`, `GNDPos`, `GNDSoft` | **No reason found** in the code; it is simply the last per-pixel step of `renderIntoOpt`. |
| R12 | Encode | Linear → 8-bit sRGB, alpha 255 | `RenderOpt` `render/render.go:646-652` | linear → encoded 8-bit | none | The output format of the CPU preview. |
| R13 | SPP sharpening | SPP's fitted unsharp mask on the developed sRGB image | `sppSharpen8` `render/render.go:655-657`; `render/sppsharpen.go:424` | encoded 8-bit → encoded 8-bit | none (`b.Raw`, sensor, ISO) | SPP sharpens the image it developed in sRGB; measured against SPP exports (`render/sppsharpen.go:3-25`). |
| R14 | Sharpness slider | 3×3 cross unsharp mask (negative softens) | `sharpen` `render/render.go:658-660,1577` | encoded → encoded | `Sharpness` | The user's sharpening is on top of SPP's, after it. |
| R15 | Deferred grain | At a reduced view with sharpening, the grain held out of R6/R8 is added after R13/R14 with the export's retained gain | `film.addGrain8` `render/render.go:664-666`; `render/film.go:715` | encoded → encoded (dec/enc round-trip) | `Grain`/`Film`/`NoGrain` | The view's own SPP mask must not act on grain that the full-resolution export would not have sharpened (`render/render.go:661-663`). |
| R16 | Impulse clean | Remove isolated 1-pixel hot/dead dots after sharpening | `impulseClean8` `render/render.go:667-669`; `render/impulse.go:38` | encoded 8-bit → encoded 8-bit | none (ISO ≥ 800, DPM/Quattro) | Runs after sharpening so a dot's ring does not survive (`render/impulse.go:19`). |

`render.Render16` (`render/render.go:676-703`) is the same R1–R16 with a
16-bit sink: no 8-bit rounding, `impulseClean16`, used by TIFF, DNG and the
non-sRGB JPEG path.

---

## 3. GPU preview (`web/gpu.js`)

The GPU does not re-develop the raw. It fetches **(a)** the Base the CPU
would render from and **(b)** a recipe built from the same `pixOp`.

| # | Stage | What it does | Function / file:line | Space in → out | `preset.Params` fields | Why here |
|---|-------|--------------|----------------------|----------------|------------------------|----------|
| G1 | Base and source | `renderBase` picks the same source mode as the CPU (Max Detail, a layer source, a WB development, or the SPP-look raw/JPEG) and sends `GPUBase`: half-float RGB, the Fill mask/da/db, and the optional mono layer block | `renderBase` `main.go:891-978`; mode choice `main.go:913-938`; `baseAs` `main.go:718-736`; `GPUBase` `render/gpu.go:378-414` | source → Base textures | all source-affecting params: `Film`, `Filter`, `WhiteBalance`, `MaxDetail` | The Base is the expensive part and is shared with the CPU; the page reloads it when `X-Base-Source` changes (`web/index.html:2501-2507`). |
| G2 | Recipe | Build the same `pixOp` (`NewGPURecipe`), serialise its matrices, its baked LUTs, the Fill model, grain parameters and SPP-sharpening numbers; register the colour-film 3-D table | `NewGPURecipe` `render/gpu.go:237-345`; `gpuCorr` `render/gpu.go:36` | params + Base metadata → `GPURecipe` | all render params | It is generated by the CPU's own `newPixOp`, so the GPU executes the CPU model rather than a second copy (`render/gpu.go:1-9`). |
| G3 | PIX pass | One pixel through the same order as `pixOp`: undo, layer filter, measured (WB/mode/tint/pre/Fill/post/saturation) or film/formula, mono shade, film grain, mono grain, mono tone, ND/GND, encode | `PIX` shader `web/gpu.js:109-333`; order `web/gpu.js:214-332` | Base textures → encoded RGBA16F | all render params | Mirrors `renderInto`/`pixOp.apply`. |
| G4 | Horizontal blur | 7-tap Gaussian (σ 1) on the encoded image | `BLURH` `web/gpu.js:335-339` | encoded → encoded | none | First half of SPP's unsharp mask. |
| G5 | Vertical blur + USM | Vertical 7-tap, then SPP's unsharp mask with the 8-neighbour range gate | `USM` `web/gpu.js:341-358` | encoded → encoded | none | Mirrors `sppSharpen8` (`render/sppsharpen.go:3-25`). |
| G6 | Output pass | Sharpness slider's 3×3 cross, deferred film grain with the retained gain, flip to canvas row 0 | `OUT` `web/gpu.js:362-378` | encoded → canvas 8-bit | `Sharpness`, `Grain`/`Film` | Mirrors `RenderOpt` R14/R15 and the canvas orientation. |
| G7 | Read-back | Read the canvas for the histogram (test hook in `pixels`) | `pixels` `web/gpu.js:580-587`; histogram scheduling `web/index.html:2514-2516` | canvas 8-bit → JS array | none | The page needs the histogram, not a second render. |

For a colour film with a `[WB]` preset the server mode is
`rawlayers:film@wb:<name>` (`main.go:934-938`, `accurate.go:698-710`), so the
Base the GPU gets is already WB layers under that light; with no film it is
the SPP-look raw (or camera JPEG) Base. The recipe never carries a second
balance for the film path (`render/render.go:933-938`).

### 3.1 CPU vs GPU order differences

**No per-pixel order differs.** The shared steps are in the same order in
`renderIntoOpt`/`pixOp` and the PIX shader: undo → layer → measured/film →
mono shade → film grain → mono grain → mono tone → density → encode
(`render/render.go:749-760` and `web/gpu.js:221-332`). The differences are
omissions and representation, not reordering:

| Difference | CPU | GPU |
|------------|-----|-----|
| Halation | R3 (`render/render.go:749`) | **Absent**; the recipe sets `CPU = "halation"` (`render/gpu.go:268-270`) and the page falls back to the server (`web/index.html:2509`). |
| Impulse clean | R16 (`render/render.go:667-669`) | **Absent**; no equivalent pass in `web/gpu.js`. |
| Fast preview | `RenderOpt(sharp=false)` skips SPP sharpening and the Sharpness slider (`render/render.go:655,658`) | No `sharp` flag: the PIX/USM/OUT chain always runs (`web/gpu.js:565-576`); the GPU is not used for the fast path. |
| Base precision | `float32` linear pixels | Half floats in `GPUBase` (`render/gpu.go:347-373,402-404`), so the input is quantised. |
| Colour-film tables | Exact `p.interp`/trilinear in `colorFilm` (`render/colorfilm.go:425-473`) | Curve sampled over log H and a 3-D texture (`render/gpu.go:194-233`; `web/gpu.js:194-213`); same model, sampled. |
| SPP sharpening amount | `sppSharpen8` with `b.scale()`/`usmThresh` (`render/sppsharpen.go:424`) | `sppView`/`usmThresh` numbers in the recipe (`render/gpu.go:336-340`), executed by BLURH/USM. |

---

## 4. Export

Entry: `exportImageCtx` `main.go:1422-1550` (Publish has its own consumer,
`publishOne` `publish.go:277`).

| # | Stage | What it does | Function / file:line | Space in → out | `preset.Params` fields | Why here |
|---|-------|--------------|----------------------|----------------|------------------------|----------|
| E1 | Source / development | Pick the source and compensation exactly as the preview does (Max Detail, layer source including `film@wb:`, WB development, SPP-look raw, JPEG), at full resolution | `developSource` `exportsource.go:17-96`; Max Detail `21-28`; layer source `37,45-50`; raw `59-74`; JPEG `75-80`; compensation `85-94` | source → linear | same source params as G1 | The export must show the same look as the preview, so it reuses the preview's mode selection. |
| E2 | Geometry + Base | Full-resolution straighten/crop, then `NewBase` | `main.go:1471`; `render.ApplyGeometry` `render/geometry.go:52`; `render.NewBase` `render/render.go:532` | linear → Base | photo crop/turn | At full resolution `b.scale()` is 1, so SPP's sharpening is its full-size fit. |
| E3 | Render | JPEG sRGB: `Render` (8-bit); TIFF/DNG/non-sRGB JPEG: `Render16` | `main.go:1498` (TIFF), `1508` (DNG), `1516` (sRGB JPEG), `1518` (non-sRGB JPEG); `render.Render` `render/render.go:637`; `render.Render16` `render/render.go:676` | Base → encoded sRGB | all render params | Same R1–R16 pipeline as the preview; no view-scale shortcuts. |
| E4 | Colour-space conversion | 16-bit sRGB → destination RGB space, before 8-bit rounding where needed | `render.ConvertFromSRGB` `render/colourspace.go:80`; `To8` `render/colourspace.go:239` | encoded sRGB → destination encoded | none (export `space` argument) | Converting before rounding keeps gradients from banding (`main.go:1511-1521`). |
| E5 | Encode / write | TIFF16+ICC, DNG16, or JPEG q95+ICC | `render.WriteTIFF16ICC` `render/tiff16.go:18`; `render.WriteRenderedDNG` `render/dng.go:96`; `jpeg.Encode`/`render.JPEGWithICC` `main.go:1530-1533`, `render/colourspace.go:223` | encoded → file | none (format argument) | The file formats need their bit depth and embedded profile. |
| E6 | Publish (second consumer) | Render16, crop/resize, output sharpening, JPEG | `publishOne` `publish.go:297-314`; `render.OutputSharpen16` `render/sppsharpen.go:417` | Base → encoded → resized → encoded | same render params | Publish is an export at a destination size; it shares `developSource` and `Render16`. |

---

## 5. Where a stage is skipped because another one is on

These are the branch points where one model disables another. They are where
task-60's white-balance bug lived.

| What is skipped | What switches it off | Where |
|-----------------|----------------------|-------|
| The per-body raw base (`rawCorrectWith`) | A layer source: a colour film or an IR look | `accurate.go:389-393` |
| The per-body model-exposure gain | A layer source (`irWhite != nil`), i.e. a colour film or an IR look | `camfread.go:426-432` |
| The shadow-chroma fix | An IR look without `sceneWB` (a colour film sets `sceneWB`) | `camfread.go:408-413` |
| SPP's measured model — its WB mapping, colour-mode mapping, measured tone steps, measured Fill Light and measured saturation | A colour film (`op.cfilm != nil`) | `render/render.go:933-934`; the WB assignment that is skipped is `render/render.go:937` |
| The measured WB mapping when the base was already developed with that balance | `Compensation.WBDeveloped` (`rawwb:` or `film@wb:`) | `render/render.go:936-938`; `modeDevelopedWB` `accurate.go:725`; `main.go:975` |
| SPP's Monochrome tab (mixer, base gamma, high-ISO terms, shading, toning) | Any film (`p.Film != ""`) | `render/render.go:877`; shading/toning guards `1516,1544` |
| SPP's mono grain | A film, because the Mono tab is off (`monoNew` false) | `b.newMonoGrain(p, op.monoNew)` `render/render.go:723`; `newMonoGrain` returns nil when `on` is false (`render/monograin.go:165-168`) |
| The film / manual grain | SPP's mono grain is on | `render/render.go:720-727` (`fp.Grain = 0`) |
| The film / manual grain | `NoGrain`, or grain 0 and no halation | `render/film.go:146-154` |
| The layer source's own film development | Max Detail's raw development wins the source choice | `main.go:916-935` (Max Detail branch before `layersSource`) |
| The colour film's layer development | An IR look wins the source choice (`irLayers` is checked first) | `accurate.go:698-701`; this is the IR-through-film issue in §7 |
| SPP sharpening | The source is a camera JPEG (`!b.Raw`) | `render/render.go:655,689` |
| The impulse clean | ISO < 800 or a sensor other than DPM/Quattro | `render/render.go:667,698` |
| The deferred-grain path | The view is 1:1 or not sharpened (`!film.reduced` / `!sharp`) | `render/render.go:729-731,664` |
| GPU halation | Not implemented on the GPU; the recipe sends the render to the CPU | `render/gpu.go:268-270`; `web/index.html:2509` |
| The colour-film model | `Compensation.CC` is nil (no camera matrix) | `render/render.go:911-919`; the DNG/GenericFilm case uses an sRGB identity instead (`920-932`) |
| The `rawwb:` source | `mode != "raw"` — a film or IR source already chose its mode | `main.go:936-938` |

---

## 6. Where white balance sits (task 60)

A `[WB]` preset is only `WhiteBalance` (a camera code 2–7); it carries no
RGB values (`filmpresets.go:95-110`). On the CPU, the measured model applies
it through `md.wbFor` (`render/render.go:937,1342-1344`). A colour film
switches that model off (`render/render.go:934`), so before task 60 the
mapping was never reached and the film's as-shot Auto development was also
never changed.

The placement now is where a film physically needs it: in the light, before
the emulsion.

1. `layersSource` appends `@wb:<name>` to the colour film's source when
   `cameraWBName(p.WhiteBalance)` is non-empty (`accurate.go:698-710`).
2. `splitFilterWB` / `rawLinearFor` split that suffix (`accurate.go:349-357,
   362-379`), and `goDevelopFor` develops the raw with that balance
   (`camfread.go:220-227`). For a layer source `CC` is identity, so
   `CameraLinear`'s per-layer WB gains reach the film as balanced layers
   (`camfread.go:236-243`).
3. Both the preview (`renderBase`, `main.go:934-938,973-975`) and the export
   (`developSource`, `exportsource.go:37,45-50,91-95`) use that same mode, so
   the GPU receives the balanced Base through `/api/gpu/base` and does not
   need a second WB stage. `modeDevelopedWB` marks it done so the measured
   mapping cannot run twice (`accurate.go:725`, `render/render.go:936-938`).

The result is the same light change the camera would see: a Shade balance
warms the layers before a slide or negative, and the slide shows its cast
while the negative is balanced again in printing. §7's IR overlap is a
separate composition problem and is not changed here.

---

## 7. The IR-through-a-colour-film overlap (reported, not fixed)

`layersSource` checks `irLayers` first (`accurate.go:698-701`), so an IR
preset plus a colour film gets the IR look's WB layers from
`rawLinearFor` (`camfread.go:335-339` clips to the IR white). Then
`newPixOp` still builds the colour film (`render/render.go:911-919`), and
`layerCompensation` makes `CC` identity (`accurate.go:731-734`), so
`colorFilm.apply` (`render/colorfilm.go:425-434`) is fed the output of the
IR look's layer matrix (`render/render.go:1186-1189,1409-1411`) as if those
were camera layers. Two colour models are therefore composed in series in a
space neither is fitted for; the crop in `zz-out/t60/crops/ir-through-film.png`
shows the cyan, flattened result. The WB fix does not touch it: the `@wb:`
suffix is only added on the film branch, and the IR branch returns first.

---

## 7a. The colour slide's viewing model (task 67)

A slide is not a print: it is viewed by transmitted light from a light box
(or read by a scanner). The slide path (`newColorFilm`/`fromExposure` in
`render/colorfilm.go`) already divided the film's transmission by its D-min
white (the eye adapted to the clearest part of the slide), and scaled the
scene's mid-grey to 18% (the print convention, so a comparison with a print is
not an exposure comparison).

Task 67 added the other half of a viewer's black point: **no light box and no
scanner reaches zero light at the film's D-max**. The film's D-max is about
3.5, i.e. ~0.03% of the clear film's transmission; mapped straight to a
display that is pure black, the deepest tones lost their gradation and their
colour. `slideBlack` = 0.005 linear (half a percent of the display white, the
same 5‰ headroom the black-and-white scan uses, `bwScanBlack`) is now added to
each channel **after** the viewing gain, so it is a property of the viewing
system and not of the film (it is not scaled by the film's gain). It is one
constant for every slide.

The GPU gets it as `uCFBlack` in `web/gpu.js` (`colorFilm(...) + vec3(uCFBlack)`),
carried in the recipe's `GPUColorFilm.Black` (`render/gpu.go`); it is a scalar
uniform, not a texture unit, and `TestSlideGPUBlack67` pins GPU = CPU at 0
8-bit levels. The negative/print path has `black = 0`, so its bytes are
unchanged.

The colour model (the sensor→layer fit, the Status A curves and their fitted
read wavelengths, the dye spectra and the 5500 K viewing light) is **not**
changed: the sheets' own data does not produce the web's magenta-cool
direction on these films, and the task says to change nothing on colour when
that is the case. REPORT-67 has the wedge, the reading-wavelength scan and the
evidence.

### 7b. The per-layer curve registration (task 68)

The five task-67 slides print their three characteristic curves at one common
log H: at the ISO mid-grey the red-, green- and blue-sensitive layers have the
same density, so equal layer exposures make a neutral grey. Some of the sheets
added in task 68 draw each layer's curve at that layer's own effective
exposure; read on one common axis the three curves do not meet at the mid-grey,
and the model's grey carries a cast that changes with exposure.

`colorFilmData.Shift` (`shift` in `render/colorfilms.json`, decades R G B)
registers each layer's curve onto the common axis: `colorFilm.density` reads
`log H - shift[k]`. It is zero (the field absent) for the five task-67 films,
whose bytes are unchanged; for the task-68 films it is a small registration
(≤0.17 decade) chosen from the sheet's own three curves so that a neutral
exposure is neutral. It is a curve-basis correction, not a colour look:
`TestSlideNoShift67` pins the five at zero, `TestSlideWedge68` pins the
registered films' neutral wedge drift at |Δa*|, |Δb*| ≤ 3 over −4…+2 stops, and
`TestColorFilmSlides` keeps the strict absolute-neutrality check for the films
with no registration.

---

## 8. What is not modelled here

* Camera JPEG/TIFF sources skip S1–S10: they are decoded to linear sRGB
  (`FromImage`, `render/render.go:241`) and start at S13. A colour film on a
  non-raw source therefore has no raw layers to rebalance; its `[WB]` preset
  is not developed into a Base.
* DNG (another camera's raw) is developed by `dngDevelop` (`main.go:623-624`)
  and gets the GenericFilm path (`render/render.go:920-932`).
* Publish (`publish.go:277`) is a second export consumer and shares E1–E4.
