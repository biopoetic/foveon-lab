"""SD1 Fill Light with the frame-mean term (coefMean).

For each fill step: the shipped coefficients and a pooled leave-one-out refit
with the frame-mean term, in the same metric (fillstudy8). A step is refitted
only when the held-out refit beats the shipped coefficients, which for most
of these photos are in-sample. The refit (on all photos) is written into
refit_model.json; copy it over render/model.json to use it.
"""
import json, os, numpy as np
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])
R = os.path.abspath('../../render')
PS = ['SDIM0031', 'SDIM0046', 'SDIM0067', 'SDIM0092', 'SDIM0137', 'SDIM0138', 'SDIM0048', 'SDIM0064']
STEPS = {'B14': 0.25, 'A16': 0.5, 'A17': 1, 'B15': 2, 'B16': -1}


def design4(x, da, db, dm):
    W = basis(x)
    return np.hstack([W, W * da[:, None], W * db[:, None], W * dm[:, None]])


model = json.load(open(f'{R}/model.json'))
xs = {p: X(f'{E}/{p}_A00.tif') for p in PS}
F = {}
for p in PS:
    da, db = surr(xs[p], 'guided')
    F[p] = (da.ravel(), db.ravel(), np.full(xs[p].size, xs[p].mean()) - xs[p].ravel())

for st, val in STEPS.items():
    have = [p for p in PS if os.path.exists(f'{E}/{p}_{st}.tif')]
    ts = {p: (X(f'{E}/{p}_{st}.tif') - xs[p]).ravel() for p in have}
    step = [s for s in model['fill'] if s['value'] == val][0]
    e_ship, e_loo = [], []
    for p in have:
        xr = xs[p].ravel()
        e_ship.append(np.abs(np.clip(xr + shipped(step['coef'], xr, F[p][0], F[p][1]), 0, 1) - (xr + ts[p])).mean() * 255)
        A = np.vstack([design4(xs[q].ravel(), *F[q])[::7] for q in have if q != p])
        b = np.concatenate([ts[q][::7] for q in have if q != p])
        c, *_ = np.linalg.lstsq(A, b, rcond=None)
        e_loo.append(np.abs(np.clip(xr + design4(xr, *F[p]) @ c, 0, 1) - (xr + ts[p])).mean() * 255)
    win = np.mean(e_loo) < np.mean(e_ship)
    print(f'{st} ({len(have)} photos): shipped {np.mean(e_ship):5.2f}   held-out refit with mean {np.mean(e_loo):5.2f}   '
          f'-> {"refit" if win else "keep shipped"}', flush=True)
    if win:
        A = np.vstack([design4(xs[q].ravel(), *F[q])[::3] for q in have])
        b = np.concatenate([ts[q][::3] for q in have])
        c, *_ = np.linalg.lstsq(A, b, rcond=None)
        step['coef'] = np.stack([c[:NB], c[NB:2 * NB], c[2 * NB:3 * NB]], 1).tolist()
        step['coefMean'] = c[3 * NB:].tolist()
json.dump(model, open('refit_model.json', 'w'), indent=1)
