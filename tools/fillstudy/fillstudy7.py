import sys, numpy as np
exec(open('fillstudy5.py').read().split("V=[")[0])
FAM={'SD1':['SDIM0031','SDIM0046','SDIM0067','SDIM0092','SDIM0137','SDIM0138','SDIM0048','SDIM0064'],
     'SD15':['SDIM1021','SDIM1151','SDIM1200','SDIM1008','SDIM1101']}
combos=[(4,16,64,160),(16,160),(8,96),(16,64),(32,160),(8,160),(4,64)]
D={}
for fam,ps in FAM.items():
    for p in ps:
        for st in ('A16','A17'):
            D[(p,st)]=(Y(f'{E}/{p}_A00.tif'),Y(f'{E}/{p}_{st}.tif'))
blur_cache={}
def feats(p,x,sig):
    return [ (blur_cache.setdefault((p,s),gaussian_filter(x,s))-x).ravel() for s in sig]
print('held-out mean (LOO within family)')
for sig in combos:
    out=[]
    for fam,ps in FAM.items():
        for st in ('A16','A17'):
            rows={}
            for p in ps:
                y0,y1=D[(p,st)]; x=enc(y0)
                A=design(x.ravel(),feats(p,x,sig)); rows[p]=(A,(enc(y1)-x).ravel(),x,y1)
            errs=[]
            for p in ps:
                A=np.vstack([rows[q][0][::7] for q in ps if q!=p]); t=np.concatenate([rows[q][1][::7] for q in ps if q!=p])
                c,*_=np.linalg.lstsq(A,t,rcond=None); Ap,tp,x,y1=rows[p]
                errs.append(np.abs(np.clip(x.ravel()+Ap@c,0,1)-enc(y1).ravel()).mean()*255)
            out.append(f'{fam} {st} {np.mean(errs):5.2f}')
    print(f'{str(sig):18s} '+'  '.join(out))
