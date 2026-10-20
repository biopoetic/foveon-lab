"""Does a much wider third surround (or the image mean) let the model tell a
big bright region (SPP: little lift) from a bright patch? Leave-one-out,
same metric as fillstudy8."""
import sys, numpy as np
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])
STEP=sys.argv[1] if len(sys.argv)>1 else 'A17'
def design3(x,feats):
    W=basis(x); return np.hstack([W]+[W*f[:,None] for f in feats])
VARIANTS={
 'two surrounds (shipped form)': lambda x: [g-x for g in (guided(x,GW/80),guided(x,GW/20))],
 '+ third at 1/5 width':          lambda x: [g-x for g in (guided(x,GW/80),guided(x,GW/20),guided(x,GW/5))],
 '+ third Gaussian 1/5':          lambda x: [g-x for g in (guided(x,GW/80),guided(x,GW/20),gaussian_filter(x,GW/5,mode='nearest'))],
 '+ image mean':                  lambda x: [g-x for g in (guided(x,GW/80),guided(x,GW/20))]+[np.full_like(x,x.mean())-x],
}
for fam,(ps,mf) in FAM.items():
    xs={p:X(f'{E}/{p}_A00.tif') for p in ps}; ts={p:(X(f'{E}/{p}_{STEP}.tif')-xs[p]).ravel() for p in ps}
    for name,fn in VARIANTS.items():
        F={p:[f.ravel() for f in fn(xs[p])] for p in ps}
        errs=[]
        for p in ps:
            A=np.vstack([design3(xs[q].ravel(),F[q])[::7] for q in ps if q!=p]); b=np.concatenate([ts[q][::7] for q in ps if q!=p])
            c,*_=np.linalg.lstsq(A,b,rcond=None)
            xr=xs[p].ravel(); errs.append(np.abs(np.clip(xr+design3(xr,F[p])@c,0,1)-(xr+ts[p])).mean()*255)
        print(f'{STEP} {fam:4s} {name:30s} mean {np.mean(errs):5.2f} | '+' '.join(f'{e:5.1f}' for e in errs))
