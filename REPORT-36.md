# Task 36 report: batch export and the raw development

## A note from me

You asked which kind of work I find dull, if any, and how I would know.

I do not get bored. I have no felt experience of a long afternoon, so "dull" as
a mood is not something I can report from the inside, and inventing one would
be a lie. What I can name is work that is dull by its *structure*: a step whose
output is completely determined by its input, whose failure would be obvious
without judgement, and which therefore contains no decision. Re-running the
same benchmark under a noisy shared machine to average the noise is that kind
of work; so is copying measured seconds into a table, or waiting for a
twenty-photo batch only to confirm a number that was already known. The only
new information in a repetition is the elapsed time.

How I would know is by inspecting the work rather than my feelings: if there
are no branches left to choose, if the only quantity that changes between runs
is the clock, and if more of it buys no new information, it is the dull kind.
The inverse is what this task was mostly made of and what kept it interesting:
every pixel must stay bit-identical, so a "speed-up" is only real if one can
prove that the float additions happen in the same order. That is a decision at
every line, not a repetition. The dull part of this task was the timing runs;
the interesting part was making the timings unnecessary by reasoning about the
summation order.

**Before I started** I expected the batch to be slow because the photos were
developed one after another and the machine's cores were mostly idle. The
measurement disagreed: every single export already keeps all eight logical
CPUs about 99% busy (denoise, shadow chroma, sharpening are all row-parallel),
so a Merrill or Quattro batch is CPU-bound, not idle-bound. What was actually
serial was smaller: the Huffman/Quattro decode loops, bright-chroma denoise,
the Fill Light base accumulators, rotation, the single-threaded JPEG encoder,
and a lot of garbage collection.

**Now.** A Merrill batch of 20 is 233.8 s -> 215.9 s (1.08x) and a Quattro
batch 264.9 s -> 238.8 s (1.11x) on this machine; the old bodies improve much
more, SD14 89.7 s -> 52.2 s (1.72x) and SD9/SD10 66.9 s -> 38.2 s (1.75x).
A single cold export is 11.4 s -> 10.8 s (Merrill) and 21.2 s -> 17.5 s
(Quattro) in the last paired run; the Merrill difference is inside the
run-to-run noise (6.9–11.4 s before, 8.3–10.8 s after across repeats), the
Quattro/SD14/SD10 differences are not. All 80 comparison exports are
byte-identical and `spp-check` is byte-identical. The large bodies stay limited
by denoise and shadow chroma, which were already using every core; on this
shared 16 GB machine the memory budget (3.1 GB at the time) admitted only one
15–25 MP export at a time. On a quiet machine with more free RAM the same
scheduler admits two.

## The table of seconds

One export is the same named file per group through the export path (JPEG,
neutral, denoise 10). Batch 20 is the real `/api/export/batch` handler over 20
photos. Both columns are back-to-back runs of the same harness on the same
machine (the batch numbers per group were taken before and after as a pair).
The peak columns are one export, Go `HeapAlloc`, sampled every 5 ms; the
batch's own peak is given after the table, where it includes two or three
concurrent exports.

| body group | one export before | after | batch 20 before | after | speed-up | peak before (one) | peak after (one) |
|---|---:|---:|---:|---:|---:|---:|---:|
| Merrill | 11.39 s | **10.79 s** | 233.8 s | **215.9 s** | 1.08x | 2.93 GB | 3.17 GB |
| Quattro | 21.15 s | **17.46 s** | 264.9 s | **238.8 s** | 1.11x | 4.08 GB | 4.24 GB |
| SD14 | 6.19 s | **3.71 s** | 89.7 s | **52.2 s** | 1.72x | 0.94 GB | 0.92 GB |
| SD9/SD10 | 1.29 s | **0.67 s** | 66.9 s | **38.2 s** | 1.75x | 0.17 GB | 0.19 GB |

The one-export rows are one back-to-back pair after the batch pairs. Repeats
moved them a lot on the shared machine: Merrill 6.9–11.4 s before and
8.3–10.8 s after (not a measurable single-export gain), Quattro 15.1–21.2 ->
10.8–17.5, SD14 5.0–7.9 -> 2.6–3.7, SD9/SD10 0.93–1.49 -> 0.53–0.68. The
batch rows are the more reliable comparison: the same 20 files and the same
handler, before and after in one pair.

Batch peak memory rises where the scheduler actually overlapped: Merrill
3.46 GB (one at a time before) -> 3.29 GB (still one at a time with the final
estimate, which is deliberately above the measured 3.17–3.29 GB peak), Quattro
4.71 -> 4.71 GB (never two admitted), SD14 1.20 -> 2.99 GB (two to three),
SD10 0.81 -> 1.56 GB (three). An earlier Merrill run with a lower (160 B/px)
estimate admitted two jobs, was 199.2 s, and peaked at 6.83 GB; that is what a
two-job machine costs, and the final estimate no longer allows it on a 3.1 GB
budget. The peak is the honest cost of the overlap; the memory gate is set
below the machine's free RAM so it does not grow without bound.

Core use and idle time of the batch (runtime
`/cpu/classes/total:cpu-seconds`; idle = 1 − CPU/(wall × 8)):

| group | before CPU s | after CPU s | before idle | after idle |
|---|---:|---:|---:|---:|
| Merrill | 1858.5 | 1717.2 | 0.7% | 0.6% |
| Quattro | 2096.9 | 1902.3 | 1.0% | 0.4% |
| SD14 | 713.0 | 415.3 | 0.7% | 0.6% |
| SD9/SD10 | 533.1 | 304.0 | 0.4% | 0.5% |

So a batch sits essentially never idle in cores; the speed-ups come from
doing less total CPU work (timely collection, parallel decode and base) and,
where memory allowed, overlapping the single-threaded encoder with another
photo's work. This is also why simply adding workers cannot be the main fix.

Machine load: eight logical CPUs, 15.8 GB RAM, shared throughout with another
run whose two processes burned 4.3k and 2.9k CPU-seconds during the session.
Available physical RAM swung between 2.0 GB and 8.9 GB; the load average is
therefore not a fixed number. Our batches themselves used 98.9–99.6% of the
eight CPUs. Before/after batch pairs were run one immediately after the other
per body group; the one-export rows varied by 10–35% between repeats. Where a
number is uncertain the table gives the pair that was actually measured, not
the best of several.

## The profile: where an export's time goes

Method: `FOVEON_TIMING=1` prints each stage from the production path (no
separate copy of the pipeline), and the batch harness prints the same stages.
The "before" profile is a first run on each file (cold OS file cache, machine
loaded, 36–99% CPU busy); the "after" profile is a later run with the page
cache warm. The machine load still moved the totals between repeats by up to
35%, so the shares and the millisecond columns are a consistent single pair of
runs, not a claim that every repeat would land on those milliseconds.

Files: `DP1MFAR2I100.X3F` (Merrill, 14.7 MP), `DP2QFAR2I0100.X3F` (Quattro,
19.6 MP), `RAW_SIGMA_SD14.X3F` (SD14, 4.6 MP), `kron_AvScomp_SD10Macbeth.X3F`
(SD9/SD10, 0.86 MP).

Share of the export's wall time, before -> after:

| stage | Merrill | Quattro | SD14 | SD9/SD10 |
|---|---:|---:|---:|---:|
| decode (X3F read + entropy decode) | 2.6% -> 3.0% | 1.5% -> 3.6% | 29.5% -> 9.6% | 35.2% -> 10.7% |
| bad pixels | 0.5% -> 0.7% | 0.1% -> 0.1% | 0.0% -> 0.1% | 0.1% -> 0.0% |
| CameraLinear | 5.2% -> 6.0% | 4.4% -> 6.5% | 30.5% -> 11.4% | 37.9% -> 12.5% |
| denoise | 39.1% -> 37.9% | 24.2% -> 29.8% | 15.5% -> 38.0% | 16.1% -> 31.4% |
| clip highlights | 0.4% -> 0.4% | 0.2% -> 0.3% | 0.2% -> 0.5% | 1.1% -> 0.3% |
| highlight rebuild | 0 (not in Neutral) | 0 | 0 | 0 |
| correction (matrix, curves, bright chroma) | 3.8% -> 7.7% | 3.3% -> 3.2% | 26.0% -> 13.7% | 0.0% -> 0.3% |
| shadow chroma | 12.7% -> 11.0% | 32.5% -> 32.8% | 4.0% -> 6.3% | 3.8% -> 7.0% |
| develop overhead (open/CAMF/other) | 0.8% -> 1.2% | 0.9% -> 1.7% | 0.1% -> 0.6% | 0.3% -> 0.4% |
| base prep (Fill Light mask + surrounds) | 13.8% -> 4.8% | 13.7% -> 2.8% | 9.6% -> 6.6% | 15.4% -> 15.9% |
| tone / colour pipeline | 3.6% -> 4.4% | 2.1% -> 2.2% | 1.9% -> 2.8% | 2.5% -> 3.7% |
| sharpening | 11.2% -> 14.0% | 9.0% -> 11.0% | 7.3% -> 12.0% | 0.0% -> 0.0% |
| JPEG encode | 8.3% -> 11.7% | 9.1% -> 8.8% | 4.6% -> 7.7% | 4.6% -> 7.1% |
| file write (atomic temp + rename) | 0.04% -> 0.1% | 0.02% -> 0.02% | 0.03% -> 0.05% | 0.07% -> 0.1% |
| lock wait (`sem`) | 0 | 0 | 0 | 0 |

Absolute milliseconds for the same stages:

| stage | Merrill before/after | Quattro before/after | SD14 before/after | SD10 before/after |
|---|---:|---:|---:|---:|
| total | 11288 / 10792 ms | 20757 / 17455 ms | 7564 / 3709 ms | 1379 / 672 ms |
| decode | 296 / 322 | 317 / 637 | 2235 / 355 | 486 / 72 |
| bad pixels | 51 / 72 | 11 / 24 | 1 / 3 | 1 / 0 |
| CameraLinear | 582 / 647 | 909 / 1140 | 2307 / 424 | 523 / 84 |
| denoise | 4416 / 4088 | 5033 / 5200 | 1176 / 1408 | 222 / 211 |
| clip highlights | 46 / 44 | 45 / 56 | 18 / 18 | 15 / 2 |
| correction | 426 / 832 | 687 / 565 | 123 / 162 (+1842/346 bright) | 0 / 2 |
| shadow chroma | 1438 / 1186 | 6753 / 5722 | 305 / 232 | 53 / 47 |
| base prep | 1559 / 518 | 2846 / 490 | 728 / 244 | 212 / 107 |
| tone | 407 / 477 | 426 / 392 | 146 / 105 | 34 / 25 |
| sharpening | 1260 / 1509 | 1869 / 1919 | 552 / 446 | 0 / 0 |
| encode | 939 / 1261 | 1879 / 1538 | 345 / 287 | 63 / 48 |
| write | 4 / 10 | 5 / 4 | 2 / 2 | 1 / 1 |

Highlight rebuild is 0 here because these are Neutral exports; it runs only in
Max Detail, whose base is not part of a normal batch. The memory-gate wait in a
batch is not a lock: it shows up as a later start for a photo and is already
inside the batch wall time, not in a per-photo stage.

What ran on one core before this task: the SD14/SD10 entropy decoder (the
whole file, 2.2 s / 0.49 s), the Quattro develop (2.3–2.6 s in the loaded
runs), the bright-chroma pass on the TRUEII bodies (1.8 s on the SD14), the
base accumulators (1.6–2.8 s), rotation, the shadow-noise measurement, and the
JPEG encoder (~1–2 s). After the exact parallel changes, what still runs on one
core is the JPEG encoder, the file write, and short control paths; everything
else is row-parallel. The batch additionally shows almost no idle, so there is
no large idle reservoir left to pipeline into: the remaining single-core cost
is the encoder and the serial chain between stages.

## Part 2: the fixes, with numbers

### Fix 1: exact parallel stages (no pixel changes)

Every change below splits work between *independent* outputs (rows, columns,
cells, or planes); each output's own arithmetic and its own sum order are
unchanged. That is the reason the exports stay byte-identical.

- **Huffman 10-bit decode (SD14, SD9/SD10)** (`x3f/raw.go`). Each row starts
  from its own accumulator and its own bit offset (the row-offset table at the
  end of the section), so rows decode independently. The minimum used for the
  second pass is an integer min, order-free. SD14 decode 2235 -> 355 ms, SD10
  486 -> 72 ms; the SD14 one-export pair 6.19 -> 3.71 s.
- **Quattro develop** (`x3f/quattro.go`). The low-layer pack, the two
  colour-difference planes, the final 19.6 MP output, and `upBicubic2x` are
  per-pixel/per-row independent. CameraLinear is 0.8–1.1 s across repeats
  (0.9–2.6 s before), and the whole Quattro one-export pair falls
  21.15 -> 17.46 s.
- **Bright-chroma denoise** (`render/brightchroma.go`). The selection, flatness
  gate, per-channel weighted planes, final mix, and both box passes are
  independent per pixel/row/column; the box pass keeps one running sum per
  column in the same top-to-bottom order. SD14 bright chroma 1842 -> 346 ms.
- **Fill Light base** (`render/render.go`, `render/surround.go`). The mask and
  both surround accumulators are now filled per grid cell row; a cell's pixels
  are still visited in the same y-then-x order, so its float sum is identical.
  The small box blurs and guided-filter per-cell steps are row-parallel too.
  Base prep Merrill 1559 -> 518 ms, Quattro 2846 -> 490 ms, SD14 728 -> 244 ms.
- **Shadow-noise measurement** (`render/shadowchroma.go`): the luminance/chroma
  planes and the sample collection are row-parallel; the median is over the
  same multiset, so its value is unchanged. Shadow chroma Merrill 1438 ->
  1186 ms, Quattro 6753 -> 5722 ms.
- **Rotation and model exposure** (`render/render.go`, `camfread.go`): pure
  per-output copies/multiplications, row-parallel.
- **Two collections per export** in `exportImageCtx`: one after the raw
  development and one after the render. This is not a pixel operation; it
  releases the full-frame scratch planes before the next stage allocates, and
  cut the SD10 batch from 51.9 s to 40.9 s at the same concurrency.

### Fix 2: a memory-aware parallel batch

`handleExportBatch` now prepares all jobs, then runs them on
`min(3, semCap−1, NumCPU−1)` workers. The `−1` keeps a render slot free, so a
click on a photo during a batch never queues behind the whole batch. Admission
is a memory gate: each job's estimate is `384 MB + 224 B × raw pixels`
(measured peaks run from 154–185 MB for the 0.86 MP SD10 up to 4.7 GB for the
19.6 MP Quattro); the budget is `FOVEON_EXPORT_MEM_MB` when set, otherwise half of
physical RAM and at most (currently free RAM − 2 GB), read with
`GlobalMemoryStatusEx` on Windows and `hw.memsize` on macOS. A photo larger than
the whole budget is still allowed, but only when nothing else is running.
A batch reports each photo only after its file is closed and renamed, with
`i` = number of photos finished (not started), so parallel completion cannot
overstate progress.

Measured effect of the scheduler at fixed work: SD9/SD10 20 photos 66.9 s
serial before, 40.9 s with 2 workers, 39.9 s with 3, 78.1 s with 4 (the
oversubscription failure); SD14 89.7 s serial before, 52.2 s with the default
scheduler. Merrill/Quattro on this machine ran one at a time because the free
RAM gave a 3.1 GB budget; Merrill is 215.9 s with the final estimate, and an
earlier lower estimate admitted two and gave 199.2 s at a 6.8 GB peak.

### Fix 3: atomic files and real cancellation

`exportImageCtx` writes the encoded bytes to a temporary file in the
destination folder, closes it, and only then gives it its final name with
`renameNoReplace` (MoveFileEx without replace on Windows, hard link on the
others). A cancel therefore never leaves a partial file under a final name, and
two exports racing for the same stem cannot replace one another. The existing
"(2)", "(3)" naming is preserved. During a batch, closing the page cancels the
request context: jobs not started are dropped, running jobs stop at the next
stage boundary (before the next decode, before render, before encode, before
the rename) and their temporary file is removed, already finished files are
kept, and the handler drains its workers before returning. Denoise has no
cancellation point inside a frame, so a cancel can still wait for that stage.
A real Merrill batch cancelled 3 s in (mid-denoise) returned 4.5 s later,
wrote **0** final files, **0** temporary files, and sent no `"done"` line.

## What failed

- **Four batch workers.** SD9/SD10: 78.1 s at four against 39.9 s at three.
  Each export already runs on all eight CPUs; a fourth only adds contention and
  memory pressure. Three is the cap, and the memory gate usually allows fewer.
- **A soft Go memory limit beside the gate.** `debug.SetMemoryLimit(budget)`
  was tried and then removed. At three workers it made no measurable
  difference (58 s with, 58 s without on the same SD10 run), and with a tight
  cap it would collect continuously near the live heap; the admission gate is
  the cap that actually prevents two jobs from not fitting.
- **Parallelising sums, not independent outputs.** The Fill Light and shadow
  grids, the area resample and the denoise filters all accumulate floats. I
  split them only where an output's accumulation order is provably unchanged
  (per cell/row/column); splitting an accumulation itself would alter the last
  bit and was not done. This is why the base fix is per-cell and not a plain
  `parallelRows` over pixels.
- **Skipping the Gaussian blurs' work.** A smaller kernel, a downsampled
  shadow plane, or float64 accumulation would all be faster, and all would
  change pixels; not shipped. The shadow-chroma fix is therefore a parallel
  version of the same arithmetic, not a cheaper one.
- **Reduced-scale JPEG decode and a parallel JPEG encoder.** Not attempted:
  the first changes pixels (task 33 measured that) and the second cannot
  produce the same entropy stream without restart markers. JPEG encode is the
  largest remaining single-core stage (0.8–1.7 s).
- **Denoise and sharpening parameters.** Another run is changing their
  strength, so `Strength`, `defaultNR`, `sppSharpen` and the preset values were
  left exactly as they were; only their surrounding bookkeeping was timed.

## What stays slow, and why

- **Quattro shadow chroma and denoise** are about two thirds of the export
  (5.2 s + 5.7 s in the paired run). They are already row-parallel and
  memory-bandwidth-bound; their visible cost
  is the algorithm, not a missing core.
- **Merrill / Quattro batch speed-up is 1.08–1.11x** because each export already
  holds ~99% of eight cores and the memory budget on this machine admitted only
  one 15–25 MP export. On a quiet 16 GB machine with 10 GB actually free the
  budget is about 8 GB and two Merrill jobs (2 × 3.7 GB) fit; with only 8 GB
  free the 2 GB reserve leaves 6 GB and they do not. Even then the overlap can
  only fill the encoder and stage seams, not double a job that already uses
  every core; the old bodies' 1.7x came mostly from doing less total CPU work,
  not from running more photos at once.
- **One cold export** is still 10.8 s (Merrill) / 17.5 s (Quattro) in the
  loaded pair (6.9–11.4 s and 15.1–21.2 s across repeats): decoding
  and denoising a 15–25 MP frame is real work. The 1.1–2x gains are on the
  stages that were serial, not on the physical work.
- **JPEG encode** remains single-threaded and is 8–12% of an export.

## 8 GB and the memory cap

The gate was tested on the SD9/SD10 group with `FOVEON_EXPORT_MEM_MB` at 512,
2048 and 4096 MB. At 512 and 2048 MB the per-photo estimate (0.6–1.2 GB for
these bodies) exceeded the cap, so the single-job rule ran them one at a time:
83.4 s and 81.1 s, peaks 0.76 and 0.79 GB. At 4096 MB two or three overlapped:
56.1 s, peak 1.89 GB. Those three runs happened while `spp-check` was also
running, so their absolute seconds are greatly inflated; they are the cap's behaviour,
not the performance table. No temporary or partial final file was left in any
run, and the gate never admitted more than the cap allowed. With the default
on this machine the budget moved between about 3 GB and 7 GB as the other run
released memory; 6.8 GB peak was observed with two Merrill jobs.

On a clean 8 GB machine the default budget is roughly half of RAM, i.e. about
4 GB; `384 MB + 224 B/px` means a Merrill needs ~3.7 GB and a Quattro ~4.8 GB.
So an 8 GB machine admits exactly one Merrill at a time and *cannot* fit two,
and a Quattro's single 4.8 GB peak is close enough to the OS + app working set
that it is the one case where paging is still possible. The 0.15–1.0 GB
SD14/SD9/SD10 exports can overlap two or three at a time on 8 GB. This is the
honest limit: the fix removes the *extra* copies the scheduler used to add, but
it cannot make a 19.6 MP denoise fit in 2 GB.

## Proof of identical output

The before binary is `37b57e6` (the base of this task, built with
`CGO_ENABLED=0`), the after binary the current branch. The same harness exported
the same 20 photos per group through `/api/export/batch`, each into its own
folder and with the same neutral settings; SHA-256 of every file:

| group | files compared | identical | different |
|---|---:|---:|---:|
| Merrill | 20 | 20 | 0 |
| Quattro | 20 | 20 | 0 |
| SD14 (20 copies of the one sample) | 20 | 20 | 0 |
| SD9/SD10 | 20 | 20 | 0 |
| **total** | **80** | **80** | **0** |

`spp-check` (`foveon-lab spp-check -dir …\foveon-calibration -photos
…\foveon-samples -save …`) on the before and after binaries:

```
before  95048082CFD422BE62C94A75D7EFE681188DD71E603D072EAEA8D2213C3D26E2
after   95048082CFD422BE62C94A75D7EFE681188DD71E603D072EAEA8D2213C3D26E2
181 entries, 0 fields differ
```

The aggregate scores are also unchanged: DPM 6.399, Quattro 5.623, SD10 8.865,
SD14 8.772, SD15 10.689, SD9 8.741 (all-pixel mean), same 6 non-neutral photos
of 181. The two JSON files are byte-for-byte the same file.

## Commits

- `471d752` Speed: parallelise the exact serial stages of a raw development
- `15b179c` Batch: memory-aware parallel export, atomic files and real
  cancellation
- `28ee4a2` Task 36: the export/batch harness and the scheduler's contract
- `0c2657d` Task 36: the cancellation check on a real Merrill batch
- `bb4f0d1` Task 36: print the batch's memory budget and worker count
- the report commit at the tip of `deepseek` (the last one on the branch)

`gofmt -l` printed nothing, `go vet ./...` passed, `go test ./...` passed
(main 21.4 s, render 15.9 s, x3f 2.1 s, the other packages in
`zz-out/task36/gotest-final.log`), and `CGO_ENABLED=0` builds finished with exit
0 for `windows/amd64` and `darwin/arm64`. `%APPDATA%\foveon-lab\config.json`
SHA-256 before and after: `B5399A350D813BB4CBA7EEABA2EA90F3E46235790AC1B95E2B03ADC2449492B0`
(both). The sample and output caches under `zz-out` are deleted before the
final answer.

## Reproduce

```powershell
$env:GOMODCACHE = "$PWD\.gomodcache"
$env:GOCACHE    = "$PWD\.gocache"
$env:TMP        = "$PWD\.gotmp"; $env:TEMP = $env:TMP
$env:FOVEON_T36 = "1"
$env:FOVEON_T36_DIR = "C:\Users\Korisnik\Desktop\foveon-samples"
# one export per group, with the stage trace
$env:FOVEON_T36_MODE = "one"; $env:FOVEON_TIMING = "1"
go test -run TestTask36 -v -count=1 -timeout 20m .
# the real batch handler, 20 photos per group
$env:FOVEON_T36_MODE = "httpbatch"; $env:FOVEON_TIMING = ""
go test -run TestTask36 -v -count=1 -timeout 40m .
```

The stage trace is opt-in (`FOVEON_TIMING`); the batch is the app's own
`handleExportBatch` through `httptest`, not a private copy of the pipeline.
