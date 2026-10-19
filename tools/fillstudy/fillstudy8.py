"""Same metric for: the shipped fill coefficients, a pooled leave-one-out
refit with the app's own surround, and a Gaussian surround."""
import json, numpy as np
from PIL import Image
from scipy.ndimage import gaussian_filter
E='C:/Users/Korisnik/Desktop/foveon-calibration/exports'
R='C:/Users/Korisnik/PycharmProjects/foveon-lab/render'
FAM={'SD1':(['SDIM0031','SDIM0046','SDIM0067','SDIM0092','SDIM0137','SDIM0138','SDIM0048','SDIM0064'],'model.json'),
     'SD15':(['SDIM1021','SDIM1151','SDIM1200','SDIM1008','SDIM1101'],'model_SD15.json')}
NB=16; GW=1200; FR=(1/80,1/20); EPS=0.08
def lin(v): return np.where(v<=0.04045,v/12.92,((v+0.055)/1.055)**2.4)
def enc(v): return np.where(v<=0.0031308,v*12.92,1.055*np.power(np.clip(v,0,None),1/2.4)-0.055)
def X(p):
    im=Image.open(p).convert('RGB'); h=round(im.height*GW/im.width); im=im.resize((GW,h),Image.BOX)
    a=lin(np.asarray(im,float)/255); return enc(0.2126*a[...,0]+0.7152*a[...,1]+0.0722*a[...,2])
def guided(v,s):
    g=lambda a: gaussian_filter(a,s,mode='nearest')
    m=g(v); m2=g(v*v); vr=np.maximum(0,m2-m*m); a=vr/(vr+EPS); b=m-a*m
    return g(a)*v+g(b)
def surr(x,kind):
    return [(guided(x,f*GW) if kind=='guided' else gaussian_filter(x,f*GW,mode='nearest'))-x for f in FR]
def basis(x):
    f=np.clip(x*NB-0.5,0,NB-1); b0=np.minimum(f.astype(int),NB-2); t=f-b0
    W=np.zeros((x.size,NB)); r=np.arange(x.size); W[r,b0]=1-t; W[r,b0+1]=t; return W
def design(x,da,db):
    W=basis(x); return np.hstack([W,W*da[:,None],W*db[:,None]])
def shipped(coef,x,da,db):
    c=np.array(coef)  # [16][3]
    return basis(x)@c[:,0]+(basis(x)@c[:,1])*da+(basis(x)@c[:,2])*db
for fam,(ps,mf) in FAM.items():
    model=json.load(open(f'{R}/{mf}'))
    for st,val in (('A16',0.5),('A17',1)):
        coef=[s['coef'] for s in model['fill'] if s['value']==val][0]
        rows={}
        for p in ps:
            x=X(f'{E}/{p}_A00.tif'); t=X(f'{E}/{p}_{st}.tif')-x
            rows[p]=(x,t,{k:[d.ravel() for d in surr(x,k)] for k in ('guided','gauss')})
        res={'shipped':[], 'refit guided':[], 'refit gauss':[]}
        for p in ps:
            x,t,S=rows[p]; xr=x.ravel(); tr=t.ravel()
            res['shipped'].append(np.abs(np.clip(xr+shipped(coef,xr,*S['guided']),0,1)-(xr+tr)).mean()*255)
            for k,name in (('guided','refit guided'),('gauss','refit gauss')):
                A=np.vstack([design(rows[q][0].ravel(),*rows[q][2][k])[::7] for q in ps if q!=p])
                b=np.concatenate([rows[q][1].ravel()[::7] for q in ps if q!=p])
                c,*_=np.linalg.lstsq(A,b,rcond=None)
                res[name].append(np.abs(np.clip(xr+design(xr,*S[k])@c,0,1)-(xr+tr)).mean()*255)
        print(f'{fam} {st}: '+'   '.join(f"{n} {np.mean(v):5.2f}" for n,v in res.items())+'   | per photo shipped: '+' '.join(f'{v:.1f}' for v in res['shipped']))
