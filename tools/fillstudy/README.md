# Fill Light study (October 2026)

Black-box study of SPP's Fill Light against its own exports
(`Desktop/foveon-calibration/exports`, A00 vs the fill steps). Python with
numpy/scipy; each script prints its own table.

- `fillstudy5.py` — which formulation explains Fill Light, fitted per photo
  (in-sample upper bounds), with a smooth brightness basis.
- `fillstudy6.py`, `fillstudy7.py` — leave-one-out within each camera family:
  shared coefficients, Gaussian surround scales.
- `fillstudy8.py` — same metric for the shipped coefficients, a pooled
  leave-one-out refit with the app's own surround (replicated), and a
  Gaussian surround.
- `fillrefit.py` — the pooled refit written into copies of the model files.
- `fillstudy11.py` - SD1 + DP Merrill pooled, held out per family; `-write`
  refits the steps where pooling wins into `refit_model.json`.
- `fillstudy12.py` - the Quattro counterpart: its own fill on 10-11 photos,
  pooled leave-one-out with and without the frame-mean term; `-write` refits
  every step whose held-out error beats the shipped coefficients into
  `refit_model_Quattro.json`.

Results and the decisions taken from them: `docs/accuracy-roadmap.md`
(Fill Light) and `docs/HOW_SPP_WORKS.md` section 5.
