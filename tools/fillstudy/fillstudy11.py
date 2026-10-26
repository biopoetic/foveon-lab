"""SD1 + DP Merrill Fill Light (same sensor, both use render/model.json).

Per fill step: the shipped coefficients against a pooled leave-one-out refit
(fillrefit2's form, with the frame-mean term) on the SD1 photos alone and on
SD1 + DP Merrill, each scored per family. With -write, steps whose pooled
held-out error beats the SD1-only held-out refit on the SD1 and the shipped
coefficients on the DP Merrill are refitted on all photos into
refit_model.json (copy it over render/model.json to use it).
"""
import json, os, sys, numpy as np
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])
R = os.path.abspath('../../render')
SD1 = ['SDIM0031', 'SDIM0046', 'SDIM0067', 'SDIM0092', 'SDIM0137', 'SDIM0138', 'SDIM0048', 'SDIM0064']
DPM = ['DP1MhHOUSE', 'DP1MhSLI0100', 'DP1MhVFAB', 'DP1MINBI0100', 'DP1MLL01005', 'DP1MOUTBAP0', 'DP1MPINE',
       'DP2M1726', 'DP2M5265', 'sigma_dp3_merrill_01', 'sigma_dp3_merrill_04', 'sigma_dp3_merrill_07',
       'sigma_dp3_merrill_10', '_P3M9383']
STEPS = {'B14': 0.25, 'A16': 0.5, 'A17': 1, 'B15': 2, 'B16': -1}
WRITE = '-write' in sys.argv


def design4(x, da, db, dm):
    W = basis(x)
    return np.hstack([W, W * da[:, None], W * db[:, None], W * dm[:, None]])


def shipped4(step, x, da, db, dm):
    c = shipped(step['coef'], x, da, db)
    if step.get('coefMean'):
        c = c + (basis(x) @ np.array(step['coefMean'])) * dm
    return c


model = json.load(open(f'{R}/model.json'))
ps = [p for p in SD1 + DPM if os.path.exists(f'{E}/{p}_A00.tif')]
xs = {p: X(f'{E}/{p}_A00.tif') for p in ps}
F = {}
for p in ps:
    da, db = surr(xs[p], 'guided')
    F[p] = (da.ravel(), db.ravel(), np.full(xs[p].size, xs[p].mean()) - xs[p].ravel())


def err(p, pred, t):
    xr = xs[p].ravel()
    return np.abs(np.clip(xr + pred, 0, 1) - (xr + t)).mean() * 255


def fam(es, have):
    s = [e for p, e in zip(have, es) if p in SD1]
    d = [e for p, e in zip(have, es) if p in DPM]
    return (np.mean(s) if s else np.nan), (np.mean(d) if d else np.nan)


for st, val in STEPS.items():
    have = [p for p in ps if os.path.exists(f'{E}/{p}_{st}.tif')]
    ts = {p: (X(f'{E}/{p}_{st}.tif') - xs[p]).ravel() for p in have}
    step = [s for s in model['fill'] if s['value'] == val][0]
    e_ship = [err(p, shipped4(step, xs[p].ravel(), *F[p]), ts[p]) for p in have]
    # Each photo's normal equations (every 7th pixel), so a leave-one-out fit
    # is a sum, not a stack of 22 photos' design matrices.
    NE = {}
    for p in have:
        A = design4(xs[p].ravel(), *F[p])[::7]
        NE[p] = (A.T @ A, A.T @ ts[p][::7])
    res = {}
    for name, pool in (('SD1 only', [p for p in have if p in SD1]), ('SD1+DPM', have)):
        es = []
        for p in have:
            tr = [q for q in pool if q != p]
            c = np.linalg.lstsq(sum(NE[q][0] for q in tr), sum(NE[q][1] for q in tr), rcond=None)[0]
            es.append(err(p, design4(xs[p].ravel(), *F[p]) @ c, ts[p]))
        res[name] = fam(es, have)
    sh = fam(e_ship, have)
    nd = sum(p in DPM for p in have)
    print(f'{st} ({len(have) - nd} SD1 + {nd} DPM)  SD1/DPM  shipped {sh[0]:5.2f}/{sh[1]:5.2f}   '
          + '   '.join(f'{k} {v[0]:5.2f}/{v[1]:5.2f}' for k, v in res.items()), flush=True)
    # The shipped coefficients were fitted on these SD1 photos (in-sample), so
    # on SD1 the fair bar is the SD1-only held-out refit; on the DP Merrill,
    # never fitted, it is the shipped one.
    pooled = res['SD1+DPM']
    if WRITE and pooled[0] < res['SD1 only'][0] and (np.isnan(sh[1]) or pooled[1] < sh[1]):
        c = np.linalg.lstsq(sum(NE[q][0] for q in have), sum(NE[q][1] for q in have), rcond=None)[0]
        step['coef'] = np.stack([c[:NB], c[NB:2 * NB], c[2 * NB:3 * NB]], 1).tolist()
        step['coefMean'] = c[3 * NB:].tolist()
        print(f'    {st}: refitted on all {len(have)} photos')
if WRITE:
    json.dump(model, open('refit_model.json', 'w'), indent=1)
