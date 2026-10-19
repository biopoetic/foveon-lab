"""Pooled refit of the Fill Light coefficients (all photos per family),
written into copies of the model files for testing."""
import json, numpy as np, os
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])
# Only the steps with held-out evidence of a gain (fillstudy8.py); B14/B15/B16 have
# no unseen photos, and there the shipped coefficients win in-sample.
STEPS={'A16':0.5,'A17':1}
out={}
for fam,(ps,mf) in FAM.items():
    model=json.load(open(f'{R}/{mf}'))
    xs={p:X(f'{E}/{p}_A00.tif') for p in ps}
    S={p:[d.ravel() for d in surr(xs[p],'guided')] for p in ps}
    for st,val in STEPS.items():
        have=[p for p in ps if os.path.exists(f'{E}/{p}_{st}.tif')]
        if len(have)<3:
            print(f'{fam} {st}: {len(have)} photos - keeping the shipped coefficients'); continue
        A=np.vstack([design(xs[p].ravel(),*S[p])[::3] for p in have])
        b=np.concatenate([(X(f'{E}/{p}_{st}.tif')-xs[p]).ravel()[::3] for p in have])
        c,*_=np.linalg.lstsq(A,b,rcond=None)
        coef=np.stack([c[:NB],c[NB:2*NB],c[2*NB:]],1)
        for s in model['fill']:
            if s['value']==val: s['coef']=coef.tolist()
        print(f'{fam} {st}: refitted on {len(have)} photos: {" ".join(have)}')
    json.dump(model,open(f'refit_{mf}','w'),indent=1)  # copy over render/{mf} to use
