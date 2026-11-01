# The measured gap against Sigma Photo Pro, by body

From `docs/spp-check.json`: mean absolute 8-bit error over every scored photo of that body
(`all` in `spp-check`; `area` is the same error on both images reduced 4×, so it
ignores fine detail). The app shows the same line in the photo info.

| body | mean /255 | area /255 | photos |
|---|---:|---:|---:|
| DP1 Merrill | 4.7 | 3.4 | 15 |
| DP2 | 8.8 | 8.6 | 8 |
| DP2 Merrill | 3.8 | 2.5 | 3 |
| DP2X | 4.4 | 3.7 | 18 |
| DP3 Merrill | 5.0 | 3.8 | 13 |
| SD1 Merrill | 3.6 | 2.8 | 2 |
| SD10 | 8.8 | 8.1 | 18 |
| SD14 | 8.5 | 7.9 | 24 |
| SD15 | 10.7 | 9.8 | 4 |
| SD9 | 8.7 | 8.2 | 12 |
| dp0 Quattro | 5.4 | 4.4 | 12 |
| dp1 Quattro | 6.7 | 5.2 | 11 |
| dp2 Quattro | 5.5 | 4.1 | 13 |
| dp3 Quattro | 4.6 | 3.4 | 6 |
| sd Quattro | 4.6 | 3.6 | 5 |
| sd Quattro H | 5.9 | 4.6 | 13 |

Regenerate with:

```
go run ./tools/sppgap -json docs/spp-check.json
```
