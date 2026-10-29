# sensfit — rebuild a spectral-sensitivity curve from graph fragments

Kodak's 2016 colour-negative sheets draw the spectral-sensitivity graph in
two different ways. E-4046/E-4050/E-4051/E-4040/E-7023 draw one vector path
per layer. E-7022 (Gold 200) draws each curve as a run of ~4 pt open fills:
a filled wedge per segment plus its stroke, about 65 pieces per sheet.

`sensfit` reconstructs a curve from either form:

1. take every point of every fragment in a layer;
2. map page coordinates to wavelength (x) and log sensitivity (y) from the
   plot frame;
3. bin by x and keep the **upper** y in each bin (the top of a filled band,
   never its closing edge);
4. join the bins, interpolating gaps inside the drawn range.

A clean one-path curve is a one-fragment input, so the same code reads both.

## Validation

`testdata/ektar_columns.json` redraws Ektar 100's stored clean curves as the
same 4 pt fills Gold 200 uses; the known curves are the values already in
`render/colorfilms.json`. `testdata/portra400_fragments.json` cuts Portra
400's own vector path into 4 pt pieces and compares with the uncut path.

| curve | RMS (log units) | peak error |
|---|---|---|
| Ektar 100 B / G / R | 0.0027 / 0.0041 / 0.0053 | 0 / 0 / 0 nm |
| Portra 400 B / G / R | 0.0110 / 0.0077 / 0.0114 | 0 / 1 / 0 nm |

The pass bar is 0.05 RMS and 5 nm.

`testdata/gold200_segments.json` is the real fragmented E-7022 page (three
runs of segment fills). Reconstructed peaks: B 469, G 549, R 654 nm.

## Use

```sh
go run ./tools/sensfit -step 1 -out curves.json fragments.json
go run ./tools/sensfit -step 1 -validate testdata/ektar_columns.json
```

Input JSON:

```json
{
  "x": {"px": [74.19, 274.65], "val": [250, 750]},
  "y": {"px": [283.96, 435.44], "val": [0, 3]},
  "layers": {
    "B": {"fragments": [[[x, y], ...], ...]},
    "G": {"fragments": [...]},
    "R": {"fragments": [...]}
  },
  "known": {"B": [[nm, logS], ...]}
}
```

`px` is the plotted frame in page coordinates (PDF points, y up), `val` the
axis values at its left/bottom and right/top. `known` is optional and only
used for the validation report.
