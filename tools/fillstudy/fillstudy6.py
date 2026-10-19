import sys, numpy as np
exec(open('fillstudy5.py').read().split("V=[")[0])
FAM={'SD1':['SDIM0031','SDIM0046','SDIM0067','SDIM0092','SDIM0137','SDIM0138','SDIM0048','SDIM0064'],
     'SD15':['SDIM1021','SDIM1151','SDIM1200','SDIM1008','SDIM1101']}
STEP=sys.argv[1] if len(sys.argv)>1 else 'A17'
V=[('global curve',lambda x: []),
   ('gauss 4,16,64,160',lambda x: [gaussian_filter(x,s)-x for s in (4,16,64,160)]),
   ('guided+gauss+detail',lambda x: [guided(x,r,e)-x for r,e in ((32,.03),(96,.003))]+[gaussian_filter(x,s)-x for s in (64,160)]+[x-gaussian_filter(x,2)])]
for fam,photos in FAM.items():
    D={}
    for p in photos:
        try: D[p]=(Y(f'{E}/{p}_A00.tif'),Y(f'{E}/{p}_{STEP}.tif'))
        except FileNotFoundError: pass
    for name,fn in V:
        rows={}
        for p,(y0,y1) in D.items():
            x=enc(y0); A=design(x.ravel(),[f.ravel() for f in fn(x)]); t=(enc(y1)-x).ravel()
            rows[p]=(A,t,x,y1)
        errs=[]
        for p in D:
            A=np.vstack([rows[q][0][::7] for q in D if q!=p]); t=np.concatenate([rows[q][1][::7] for q in D if q!=p])
            c,*_=np.linalg.lstsq(A,t,rcond=None)
            Ap,tp,x,y1=rows[p]
            errs.append(np.abs(np.clip(x.ravel()+Ap@c,0,1)-enc(y1).ravel()).mean()*255)
        print(f'{STEP} {fam:4s} {name:22s} held out: '+' '.join(f'{e:5.2f}' for e in errs)+f'   mean {np.mean(errs):5.2f}')
