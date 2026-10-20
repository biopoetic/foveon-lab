"""Scene adaptation: shared shape, per-photo strengths. For each photo the
pooled model (fitted on the OTHER photos) is split into its brightness-only
part G and its surround part L; then d = alpha*G + beta*L with alpha, beta
fitted on that photo alone. How much of the per-photo gap do two numbers
close, and what image property predicts them?"""
import sys, numpy as np
exec(open('fillstudy8.py').read().split("for fam,(ps,mf) in FAM.items():")[0])
STEP=sys.argv[1] if len(sys.argv)>1 else 'A17'
for fam,(ps,mf) in FAM.items():
    rows={}
    for p in ps:
        x=X(f'{E}/{p}_A00.tif'); t=X(f'{E}/{p}_{STEP}.tif')-x
        da,db=[d.ravel() for d in surr(x,'guided')]; rows[p]=(x.ravel(),t.ravel(),da,db)
    print(f'{fam} {STEP}: {"photo":9s} {"shared":>6s} {"+2 scalars":>10s} {"alpha":>6s} {"beta":>6s} | mean x  p10 x  local contrast  shadow share')
    for p in ps:
        A=np.vstack([design(rows[q][0],rows[q][2],rows[q][3])[::7] for q in ps if q!=p]); b=np.concatenate([rows[q][1][::7] for q in ps if q!=p])
        c,*_=np.linalg.lstsq(A,b,rcond=None)
        x,t,da,db=rows[p]; W=basis(x)
        G=W@c[:NB]; L=(W@c[NB:2*NB])*da+(W@c[2*NB:])*db
        e_sh=np.abs(np.clip(x+G+L,0,1)-(x+t)).mean()*255
        M=np.stack([G,L],1); ab,*_=np.linalg.lstsq(M,t,rcond=None)
        e_2=np.abs(np.clip(x+M@ab,0,1)-(x+t)).mean()*255
        lc=np.abs(da).mean()
        print(f'{"":9s} {p:9s} {e_sh:6.2f} {e_2:10.2f} {ab[0]:6.2f} {ab[1]:6.2f} | {x.mean():6.3f} {np.percentile(x,10):6.3f} {lc:14.4f} {np.mean(x<0.25):12.2f}')
