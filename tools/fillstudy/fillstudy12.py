"""Quattro Fill Light: the shipped coefficients against pooled leave-one-out
refits with and without the frame-mean term.

The Quattro fill was measured on one or two photos (render/model_Quattro.json),
so there is no held-out number for it.  This is the Quattro counterpart of
fillstudy11.py, which did the same for SD1 + DP Merrill.  The SD1/DPM
coefficients are not a prior: different sensor.

With -write, a step whose pooled held-out error beats the shipped coefficients
on the same photos is refitted on all of them into refit_model_Quattro.json
(copy it over render/model_Quattro.json to use it).
"""
import json, os, sys, numpy as np
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])

R = os.path.abspath('../../render')
# Every Quattro photo with any fill export (new batch first, then the older
# SDIM4388 / _SDI0263 exports).
Q = ['sigma_dp0_quattro_05', 'sigma_dp0_quattro_15',
     'sigma_dp1_quattro_03', 'sigma_dp1_quattro_09',
     'DP2QhHOUSE_P1', 'DP2QOUTBAP0', 'sigma_dp3_quattro_08',
     'sigma_sd_quattro_13', 'sigma_sd_quattro_h_03',
     'SDIM4388', '_SDI0263']
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


model = json.load(open(f'{R}/model_Quattro.json'))
ps = [p for p in Q if os.path.exists(f'{E}/{p}_A00.tif')]
xs = {p: X(f'{E}/{p}_A00.tif') for p in ps}
F = {}
for p in ps:
    da, db = surr(xs[p], 'guided')
    F[p] = (da.ravel(), db.ravel(), np.full(xs[p].size, xs[p].mean()) - xs[p].ravel())


def err(p, pred, t):
    xr = xs[p].ravel()
    return np.abs(np.clip(xr + pred, 0, 1) - (xr + t)).mean() * 255


def readable(p):
    try:
        Image.open(p).load()
        return True
    except OSError:
        return False


for st, val in STEPS.items():
    have = [p for p in ps if os.path.exists(f'{E}/{p}_{st}.tif') and readable(f'{E}/{p}_{st}.tif')]
    missing = [p for p in ps if os.path.exists(f'{E}/{p}_{st}.tif') and p not in have]
    if missing:
        print(f'{st}: skipping unreadable/truncated exports: ' + ', '.join(missing), flush=True)
    if not have:
        print(f'{st}: no exports')
        continue
    ts = {p: (X(f'{E}/{p}_{st}.tif') - xs[p]).ravel() for p in have}
    step = [s for s in model['fill'] if s['value'] == val][0]
    e_ship = [err(p, shipped4(step, xs[p].ravel(), *F[p]), ts[p]) for p in have]
    NE = {}
    for p in have:
        A = design4(xs[p].ravel(), *F[p])[::7]
        NE[p] = (A.T @ A, A.T @ ts[p][::7])
    res = {}
    per = {}
    for name, terms in (('3-term', 3), ('4-term', 4)):
        es = []
        for p in have:
            tr = [q for q in have if q != p]
            A = sum(NE[q][0] for q in tr)
            b = sum(NE[q][1] for q in tr)
            # Drop the frame-mean block for the 3-term fit.
            if terms == 3:
                n = 3 * NB
                A = A[:n, :n]
                b = b[:n]
            c = np.linalg.lstsq(A, b, rcond=None)[0]
            xr = xs[p].ravel()
            if terms == 3:
                pred = design(xr, F[p][0], F[p][1]) @ c
            else:
                pred = design4(xr, *F[p]) @ c
            es.append(err(p, pred, ts[p]))
        res[name] = float(np.mean(es))
        per[name] = es
    sh = float(np.mean(e_ship))
    print(f'{st} ({len(have)} photos: ' + ', '.join(have) + ')  shipped %.3f  LOO 3-term %.3f  LOO 4-term %.3f'
          % (sh, res['3-term'], res['4-term']), flush=True)
    for i, p in enumerate(have):
        print(f'    {p:28s} shipped {e_ship[i]:5.2f}  LOO 3-term {per["3-term"][i]:5.2f}  LOO 4-term {per["4-term"][i]:5.2f}')
    # Refit on all photos when the pooled held-out fit is better than shipped.
    best = min(res.values())
    if WRITE and best < sh:
        n = 4 * NB if res['4-term'] < res['3-term'] else 3 * NB
        A = sum(NE[q][0] for q in have)
        b = sum(NE[q][1] for q in have)
        c = np.linalg.lstsq(A[:n, :n], b[:n], rcond=None)[0]
        step['coef'] = np.stack([c[:NB], c[NB:2 * NB], c[2 * NB:3 * NB]], 1).tolist()
        if n == 4 * NB:
            step['coefMean'] = c[3 * NB:].tolist()
        else:
            step.pop('coefMean', None)
        print(f'    {st}: refitted on all {len(have)} photos, {n // NB}-term')
if WRITE:
    json.dump(model, open('refit_model_Quattro.json', 'w'), indent=1)
