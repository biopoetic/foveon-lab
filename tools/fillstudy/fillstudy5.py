import numpy as np
from scipy.ndimage import uniform_filter, gaussian_filter
from PIL import Image
E='C:/Users/Korisnik/Desktop/foveon-calibration/exports'
PH=['SDIM0031','SDIM0046','SDIM0092','SDIM0064','SDIM1021','SDIM1008']
def lin(v): return np.where(v<=0.04045,v/12.92,((v+0.055)/1.055)**2.4)
def enc(v): return np.where(v<=0.0031308,v*12.92,1.055*np.power(np.clip(v,0,None),1/2.4)-0.055)
def Y(p):
    im=Image.open(p).convert('RGB'); im=im.resize((im.width//4,im.height//4),Image.BOX)
    a=lin(np.asarray(im,float)/255); return 0.2126*a[...,0]+0.7152*a[...,1]+0.0722*a[...,2]
def guided(I, r, eps):
    m=lambda a: uniform_filter(a, 2*r+1, mode='reflect')
    mi=m(I); vi=m(I*I)-mi*mi; a=vi/(vi+eps); b=mi-a*mi
    return m(a)*I+m(b)
def hats(x, nk=9):
    # Piecewise-linear basis: each knot's weight, so coefficients vary smoothly with x.
    f=np.clip(x,0,1)*(nk-1); i=np.minimum(f.astype(int),nk-2); t=f-i
    W=np.zeros((x.size,nk)); r=np.arange(x.size); W[r,i]=1-t; W[r,i+1]=t; return W
def design(x, feats):
    W=hats(x); cols=[W]+[W*f[:,None] for f in feats]; return np.hstack(cols)
def fit(y0,y1,featfn):
    x=enc(y0); feats=featfn(x); A=design(x.ravel(),[f.ravel() for f in feats])
    t=(enc(y1)-x).ravel(); c,*_=np.linalg.lstsq(A,t,rcond=None)
    res=(np.clip(x.ravel()+A@c,0,1)-enc(y1).ravel()).reshape(x.shape)
    return np.abs(res).mean()*255, res
V=[('global curve only', lambda x: []),
   ('gauss 4,16,64,160', lambda x: [gaussian_filter(x,s)-x for s in (4,16,64,160)]),
   ('guided r32,96 eps .03,.003', lambda x: [guided(x,r,e)-x for r,e in ((32,.03),(96,.03),(32,.003),(96,.003))]),
   ('gauss 16,64,160 + detail(gauss 2)', lambda x: [gaussian_filter(x,s)-x for s in (16,64,160)]+[x-gaussian_filter(x,2)]),
   ('guided + gauss + detail', lambda x: [guided(x,r,e)-x for r,e in ((32,.03),(96,.003))]+[gaussian_filter(x,s)-x for s in (64,160)]+[x-gaussian_filter(x,2)])]
data={p:(Y(f'{E}/{p}_A00.tif'),Y(f'{E}/{p}_A17.tif')) for p in PH}
print(f"{'variant (per-photo fit, smooth basis)':38s} "+' '.join(f'{p[-4:]:>6s}' for p in PH)+'   mean')
for name,fn in V:
    out=[fit(*data[p],fn) for p in PH]
    print(f'{name:38s} '+' '.join(f'{e:6.2f}' for e,_ in out)+f'  {np.mean([e for e,_ in out]):5.2f}')
    if name.startswith('guided + gauss'):
        res=out[0][1]; Image.fromarray((np.clip(0.5+res*4,0,1)*255).astype(np.uint8)).save('resid2_SDIM0031.png')
