# The measured gap against Sigma Photo Pro, by body

From `docs/spp-check.json`: mean absolute 8-bit error over every scored photo of that body
(`all` in `spp-check`; `area` is the same error on both images reduced 4×, so it
ignores fine detail). The app shows the same line in the photo info.

| body | mean /255 | area /255 | photos |
|---|---:|---:|---:|
| DP1 Merrill | 4.8 | 3.4 | 15 |
| DP1S | 18.0 | 18.2 | 1 |
| DP2 | 8.1 | 7.9 | 9 |
| DP2 Merrill | 17.4 | 16.5 | 4 |
| DP2X | 4.4 | 3.7 | 18 |
| DP3 Merrill | 5.1 | 3.9 | 13 |
| SD1 Merrill | 4.0 | 2.9 | 2 |
| SD10 | 8.9 | 8.2 | 20 |
| SD14 | 8.8 | 8.3 | 25 |
| SD15 | 10.7 | 9.8 | 4 |
| SD9 | 8.7 | 8.2 | 12 |
| dp0 Quattro | 5.7 | 4.4 | 12 |
| dp1 Quattro | 6.9 | 5.3 | 11 |
| dp2 Quattro | 5.8 | 4.2 | 13 |
| dp3 Quattro | 4.7 | 3.5 | 6 |
| sd Quattro | 5.1 | 3.8 | 5 |
| sd Quattro H | 6.1 | 4.5 | 13 |

Regenerate with:

```
go run ./tools/sppgap -json docs/spp-check.json
```
