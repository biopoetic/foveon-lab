# What the formerly task-numbered tests guard

Task 88 renamed the twenty test files that were named after the task that wrote
them. No test logic, tolerance or data changed; only the file names, the test
and helper identifiers, and the doc comments. Every test now carries a
three-line contract: what it guards, what a failure means, and when it may be
changed.

## Files

| old file | new file |
| --- | --- |
| `task33_measure_test.go` | `browse_latency_test.go` |
| `task36_measure_test.go` | `export_timing_test.go` |
| `task60_test.go` | `film_wb_source_test.go` |
| `render/task60_gpu_test.go` | `render/film_wb_gpu_test.go` |
| `render/task61_gpu_test.go` | `render/color_scan_gpu_test.go` |
| `render/task61_test.go` | `render/color_scan_test.go` |
| `render/task63_test.go` | `render/color_scan_generic_test.go` |
| `render/task64_test.go` | `render/color_negative_speed_test.go` |
| `render/task65_test.go` | `render/bw_scan_test.go` |
| `render/task66_test.go` | `render/bw_scan_highlight_test.go` |
| `render/task67_test.go` | `render/slide_viewing_test.go` |
| `render/task68_test.go` | `render/slide_sheets_test.go` |
| `render/task69_test.go` | `render/slide_fuji_test.go` |
| `render/task71_test.go` | `render/eir_continuity_test.go` |
| `render/task72_bytes_test.go` | `render/nonslide_bytes_pin_test.go` |
| `render/task72_test.go` | `render/status_a_test.go` |
| `render/task73_test.go` | `render/slide_registration_test.go` |
| `render/task74_test.go` | `render/slide_dye_neutral_test.go` |
| `render/task78_gpu_test.go` | `render/film_tone_gpu_test.go` |
| `render/task78_test.go` | `render/bw_film_curves_test.go` |

## Tests

Old and new names are the same where only the file or the helpers were
task-named. "Guards" is the contract in the test's doc comment.

| old name | new name | what it guards |
| --- | --- | --- |
| `TestTask33` | `TestBrowseLatency` | First-open timings: folder thumbnails, a cold next photo, the preset grid and the changed-tile comparison (manual harness). |
| `TestTask36` | `TestExportTiming` | Export time and memory, per-body batch scheduling and cancellation on real samples (manual harness). |
| `TestFilmWBSourceSelection` | `TestFilmWBSourceSelection` | Which layer source a preset selects for film, `film@wb`, IR or normal raw. |
| `TestFilmWBModeAndSplit` | `TestFilmWBModeAndSplit` | `splitFilterWB` and `modeDevelopedWB`, the film/WB mode-string split. |
| `TestFilmWBGPURecipeMirrorsCPU` | `TestFilmWBGPURecipeMirrorsCPU` | GPU = CPU (one 8-bit level) for a `[WB]` preset under a colour film. |
| `TestColorScanGPURecipeMirrorsCPU` | `TestColorScanGPURecipeMirrorsCPU` | GPU = CPU (one 8-bit level) for a colour-negative scan. |
| `TestColorPapersFromSheets` | `TestColorPapersFromSheets` | The colour-paper library against the digitised E-7020/E-7021/AF3-212E sheets. |
| `TestColorScan` | `TestColorScan` | The colour-negative scan: own D-min removed, 18% mid-grey, flatter than the print. |
| `TestColorScanLegacyEmptyPaper` | `TestColorScanLegacyEmptyPaper` | Old edits with paper `""` still print on the first colour paper. |
| `TestColorScanGenericWedge63` | `TestColorScanGenericWedge` | The generic colour scan's neutral wedge from 2% grey to white. |
| `TestColorScanGenericProfile63` | `TestColorScanGenericProfile` | The one fixed generic scan matrix and its neutral row sums. |
| `TestColorScanGenericColours63` | `TestColorScanGenericColours` | Scene hue and chroma through the generic scan (skin, foliage, sky). |
| `TestColorScanGenericFilmsDiffer63` | `TestColorScanGenericFilmsDiffer` | The films keep distinct looks under the generic scan. |
| `TestColorNegativeProSheetAxis64` | `TestColorNegativeProSheetAxis` | The Fuji Pro speed-point registration and every negative's mid-grey gap. |
| `TestBWScanWedge65` | `TestBWScanWedge` | The B&W scan wedge: 18% grey, base+fog headroom, no clipped black. |
| `TestBWScanGenericUnchanged65` | `TestBWScanGenericUnchanged` | A B&W film without a curve still gets no film tone. |
| `TestBWPrintPathUnchanged65` | `TestBWPrintPathUnchanged` | The B&W print LUT still matches the old formula entry by entry. |
| `TestBWScanGPURecipe65` | `TestBWScanGPURecipe` | GPU = CPU for the B&W scan through the measured pre/post tables. |
| `TestBWScanShoulder66` | `TestBWScanShoulder` | The B&W scan shoulder: identity at the knee, slope 1, monotone, below 254. |
| `TestBWScanHighlight66` | `TestBWScanHighlight` | Every B&W film's highlight wedge and 8-bit ceiling. |
| `TestBWScanGPUHighlights66` | `TestBWScanGPUHighlights` | GPU = CPU in the highlights, including scene white and the shoulder. |
| `TestSlideBlackPoint67` | `TestSlideViewingBlack` | The slide viewing black, 18% mid-grey and neutral deep shadow. |
| `TestSlideWedge67` | `TestSlideViewingWedge` | A slide's neutral wedge drift from its own balance. |
| `TestSlideFilmsDiffer67` | `TestSlideFilmsDiffer` | The five original slides remain distinct films. |
| `TestSlideGPUBlack67` | `TestSlideViewingGPU` | GPU = CPU for a slide with the viewing black added after the gain. |
| `TestSlideData68` | `TestSlideSheetData` | Each sheet-derived slide has three curve, sensitivity and dye sets. |
| `TestSlideNoShift67` | `TestSlideSheetsNoShift` | The five task-67 slides keep no axis shift. |
| `TestSlideWedge68` | `TestSlideSheetWedge` | The new slides' wedge, registration tolerance and mid-grey neutrality. |
| `TestSlideBlack68` | `TestSlideSheetBlack` | The new slides' viewing black and neutral deep shadow. |
| `TestSlideGPU68` | `TestSlideSheetGPU` | GPU = CPU for three sheet-derived slides. |
| `TestSlideData69` | `TestSlideFujiData` | The last slide films' curve sets, coverage and sampling gaps. |
| `TestSlideWedge69` | `TestSlideFujiWedge` | The last slides' wedge drift and Status A read wavelengths (EIR exempt). |
| `TestSlideBlack69` | `TestSlideFujiBlack` | The last slides' viewing black and deep-shadow neutrality (EIR cast exempt). |
| `TestSlideGPU69` | `TestSlideFujiGPU` | GPU = CPU for Velvia 50, Provia 100F and EIR's estimated IR layer. |
| `TestEIRApprox` | `TestEIRApprox` | EIR's approximate flag, its IR estimate and the vegetation/sky mapping. |
| `TestEIRContinuous` | `TestEIRContinuous` | `irEstimate` is continuous through the old 0.04 threshold and its branches. |
| `TestSlideGPU71` | `TestSlideGPUEIR` | GPU = CPU for EIR and Sensia 200 after its re-digitisation. |
| `TestT72NonSlideBytes` | `TestNonSlideBytesPinned` | The rendered bytes of every non-modelled-slide path against v0.4.108. |
| `TestStatusATable72` | `TestStatusATable` | The ISO 5-3 Status A table against the printed values and edge slopes. |
| `TestStatusAIntegrated72` | `TestStatusAIntegrated` | The integrated Status A inverse on the kept films (round trip). |
| `TestT73Registration` | `TestSlideRegistrationPinned` | The read split and one mid-grey registration per kept film. |
| `TestT73FallbackList` | `TestSlideReadSplitPinned` | The exact Status A split: four kept films, EIR and the rest fitted. |
| `TestT73GPU` | `TestSlideRegisteredGPU` | GPU = CPU for the four films on the registered read. |
| `TestDyeNeutral74` | `TestDyeNeutral` | The sheet's dye-neutral check on every Kodak/Agfa film and the six failures. |
| `TestDyeNeutral74Level` | `TestDyeNeutralLevel` | The sheet's visual-density claim (1.0 +/- 0.05, Kodak) on the same films. |
| `TestDyeNeutral74Model` | `TestDyeNeutralModel` | The split between model-only and sheet residuals under the dye-only white. |
| `TestT74GPU` | `TestSlideDyeNeutralGPU` | GPU = CPU for Kodachrome 200, EPY and E100G after the re-trace. |
| `TestT74SlideBytes` | `TestSlideBytesPinned` | Every modelled slide's rendered bytes against the pinned hashes. |
| `TestT78GPUFilmTone` | `TestGPUFilmToneMirror` | GPU = CPU for the B&W film-tone LUT. |
| `TestT78NewFilmCurves` | `TestNewFilmCurves` | The new B&W curves' speed point, contrast index and printed ends. |
| `TestT78NoCurveStaysGeneric` | `TestNoCurveStaysGeneric` | A film without a curve stays generic; an added curve is found by name. |

## Helpers and tables

These were renamed with the tests. They are not tests themselves.

| old | new |
| --- | --- |
| `t33sum`, `t33photos`, `t33preview`, `t33serverTiming`, `t33timing`, `t33stage`, `t33clearThumbCache` | `sumStageTime`, `browsePhotos`, `timePreview`, `serverRenderTimings`, `serverTimingPart`, `stageTime`, `clearRedirectedThumbCache` |
| `t36mem`, `t36startMem`, `t36cpu`, `t36dir`, `t36photos`, `t36groups`, `t36server`, `t36one`, `t36batch`, `t36batchHTTP`, `t36cancel`, `t36median` | `exportMem`, `startExportMem`, `processCPUSeconds`, `exportSamplesDir`, `exportPhotos`, `exportBodyGroups`, `exportServer`, `measureOneExport`, `measureBatchExport`, `measureHTTPBatchExport`, `measureCancelExport`, `medianDuration` |
| `t60Mat3`, `t60Clamp`, `t60GPUCurve`, `t60GPUFilm` | `multiplyMat3`, `clampRange`, `gpuFilmCurve`, `gpuFilmPass` |
| `task61Luma` | `filmLuma` |
| `t63Lab`, `t63Hue`, `t63HueDiff`, `t63Films` | `labWithL`, `hueDegrees`, `hueDistance`, `genericScanFilms` |
| `task64CurveDensity`, `task64SpeedPoint` | `curveDensityAt`, `speedPointLogH` |
| `t65Films`, `t65L`, `t65GPULut` | `bwScanFilms`, `cieLightness`, `gpuLutSample` |
| `t67Slides`, `t67Lab`, `t67NaturalLayers` | `viewingSlideFilms`, `slideLab`, `naturalLayers` |
| `t68Films`, `t69Films` | `sheetSlideFilms`, `fujiSlideFilms` |
| `t71MaxStep` | `maxStep` |
| `t72NonSlideHash` | `nonSlideHash` |
| `t73StatusAFilms`, `t73Tolerance`, `t73IsStatusA`, `t73MidLab` | `statusASlideFilms`, `registrationTolerance`, `isStatusAFilm`, `slideMidLab` |
| `t74Check`, `t74Temp`, `t74Planck`, `t74DyeEps`, `t74XYZ`, `t74SlideRGB`, `t74LabXYZ`, `t74DminAmounts`, `t74Want`, `t74Wants`, `t74Within2`, `t74SlideHashes` | `dyeNeutralResult`, `sheetIlluminant`, `planckRadiance`, `dyeExtinctions`, `dyeXYZ`, `slideRGB`, `labFromXYZ`, `dminAmounts`, `dyeNeutralWant`, `dyeNeutralWants`, `withinTwo`, `slideHashes` |
| `t78GPUFilmTone`, `t78CI`, `t78xAtDens` | `gpuFilmTone`, `contrastIndex`, `logHAtDensity` |
