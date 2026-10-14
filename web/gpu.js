// The GPU preview: renders the photo with WebGL2 from the Base (sent once,
// half floats: /api/gpu/base) and a recipe of the settings (/api/gpu/recipe,
// render/gpu.go). The recipe is the state of the Go render's own pixOp —
// its matrices and its per-brightness functions sampled into tables — so
// this file executes the Go model step by step (render.go renderInto,
// pixOp.apply / applyMeasured, film.go grain, sppsharpen.go, sharpen),
// and the export stays the Go render. Halation is not here yet: the recipe
// asks for the CPU then.
"use strict";
const GPU = (() => {
  const N = 2048;          // render.GPULutN
  const FILM_N = 4096;     // render.filmToneN
  let gl = null, canvas = null, progs = null, quad = null;
  let base = null;         // {key, w, h, hdr, rgb, aux}
  let fbs = [];            // ping-pong targets {tex, fb}
  let luts = null, film = null;

  const VS = `#version 300 es
in vec2 p; void main(){ gl_Position = vec4(p, 0., 1.); }`;
  const HEAD = `#version 300 es
precision highp float; precision highp int; precision highp sampler2D;
out vec4 o;
float enc(float x){ x = clamp(x, 0., 1.); return x <= 0.0031308 ? x * 12.92 : 1.055 * pow(x, 1. / 2.4) - 0.055; }
float dec(float x){ x = clamp(x, 0., 1.); return x <= 0.04045 ? x / 12.92 : pow((x + 0.055) / 1.055, 2.4); }
`;
  // Pass 1: one pixel through pixOp (+ grain), written as 8-bit values.
  const PIX = HEAD + `
uniform sampler2D uRGB, uAux, uLut, uFilm;
uniform int uW, uH, uFlipOut;
uniform bool uUndo, uUndoGamma, uMeasured, uMono, uFilmTone, uWB, uWBTaste, uMM, uMMTaste, uFillABCK, uFillLocal, uGrainMono;
uniform int uLayerMode;   // 0 none, 1 matrix, 2 mono weights
uniform vec3 uUndoM[3], uLayerM[3], uLayerMono, uWBM[3], uMMM[3], uGain, uTint;
uniform float uMaskScale, uFill, uHL, uBlk, uCT, uSatF, uSatMeasure, uWBSat, uMMSat, uFilmScale, uGrain;
uniform int uMode;
uniform float uLocal[64];
const vec3 LUMA = vec3(0.2126, 0.7152, 0.0722);
// LUT rows (render/gpu.go): 0-1 pre x/c, 2-3 post x/c, 4-7 fill A/B/C/K,
// 8 local-fill chroma, 9-11 WB curves, 12 WB taste tone, 13-15 mode curves, 16 mode taste tone.
float lut(int row, float x){
  float f = clamp(x, 0., 1.) * ${N}.;
  int i = min(int(f), ${N} - 1);
  float a = texelFetch(uLut, ivec2(i, row), 0).r, b = texelFetch(uLut, ivec2(i + 1, row), 0).r;
  return a + (b - a) * (f - float(i));
}
vec3 mul(vec3 m[3], vec3 v){ return vec3(dot(m[0], v), dot(m[1], v), dot(m[2], v)); }
vec3 corr(vec3 c, vec3 m[3], bool taste, float sat, int row){
  if (taste){
    float y = dot(LUMA, c);
    if (y <= 1e-6) return vec3(0.);
    float y2 = lut(row + 3, enc(y)), k = y2 / y;
    return y2 + sat * (c * k - y2);
  }
  vec3 v = mul(m, c);
  return vec3(lut(row, enc(v.r)), lut(row + 1, enc(v.g)), lut(row + 2, enc(v.b)));
}
float filmTone(float y){
  if (y <= 0.) return 0.;
  float x = sqrt(sqrt(y)) * uFilmScale;
  if (x >= ${FILM_N}.) return texelFetch(uFilm, ivec2(${FILM_N}, 0), 0).r;
  int i = int(x);
  float a = texelFetch(uFilm, ivec2(i, 0), 0).r, b = texelFetch(uFilm, ivec2(i + 1, 0), 0).r;
  return a + (b - a) * (x - float(i));
}
float hueWeight(float h, float c, float half_){ float d = abs(h - c); if (d > 180.) d = 360. - d; return max(0., 1. - d / half_); }
float bilerp8(float s, float p){
  float fs = clamp(s * 8. - 0.5, 0., 7.), fp = clamp(p * 8. - 0.5, 0., 7.);
  int s0 = min(int(fs), 6), p0 = min(int(fp), 6);
  float ts = fs - float(s0), tp = fp - float(p0);
  float a = uLocal[s0 * 8 + p0] + (uLocal[s0 * 8 + p0 + 1] - uLocal[s0 * 8 + p0]) * tp;
  float b = uLocal[(s0 + 1) * 8 + p0] + (uLocal[(s0 + 1) * 8 + p0 + 1] - uLocal[(s0 + 1) * 8 + p0]) * tp;
  return a + (b - a) * ts;
}
uint hash32(uint x){ x ^= x >> 16; x *= 0x7feb352du; x ^= x >> 15; x *= 0x846ca68bu; x ^= x >> 16; return x; }
float gauss(uint i, uint c){
  uint a = hash32(i * 3u + c + 0x9e3779b9u), b = hash32(a ^ 0x85ebca6bu);
  float s = float(a & 0xffffu) + float(a >> 16) + float(b & 0xffffu) + float(b >> 16);
  return (s / 65535. - 2.) * 1.7320508;
}
float grain1(float v, float n){
  if (v <= 0.) return v;
  float u = min(1., enc(v)), amp = 2. * sqrt(u * (1. - u));
  return v * exp(-2.302585 * uGrain * amp * n);
}
void main(){
  ivec2 q = ivec2(gl_FragCoord.xy);            // image row q.y (row 0 = top)
  vec3 c = texelFetch(uRGB, q, 0).rgb;
  vec3 aux = texelFetch(uAux, q, 0).rgb;      // mask, da, db
  if (uUndo){
    if (uUndoGamma) c = vec3(enc(max(0., c.r)), enc(max(0., c.g)), enc(max(0., c.b)));
    c = mul(uUndoM, c);
    if (uUndoGamma) c = vec3(dec(max(0., c.r)), dec(max(0., c.g)), dec(max(0., c.b)));
  }
  if (uLayerMode == 1) c = mul(uLayerM, c);
  else if (uLayerMode == 2) c = vec3(max(0., dot(uLayerMono, c)));
  if (uMeasured){
    if (uWB) c = corr(c, uWBM, uWBTaste, uWBSat, 9);
    if (uMM) c = corr(c, uMMM, uMMTaste, uMMSat, 13);
    c *= uTint;
    float Y = dot(LUMA, c);
    vec3 ch = c - Y;
    float x = enc(Y);
    float chroma = lut(1, x); x = lut(0, x);
    if (uFillABCK){
      float k = lut(7, x);
      x = clamp(x + lut(4, x) + lut(5, x) * aux.g + lut(6, x) * aux.b, 0., 1.);
      chroma *= max(0., k);
    } else if (uFillLocal){
      float k = lut(8, x);
      x = clamp(x + bilerp8(aux.r, x), 0., 1.);
      chroma *= max(0., k);
    }
    chroma *= lut(3, x); x = lut(2, x);
    float Y2 = dec(x);
    c = uMono ? vec3(Y2) : Y2 + ch * (chroma * uSatMeasure);
  } else {
    c *= uGain;
    if (uFill != 0.){
      float m = min(1., aux.r * uMaskScale), wt = (1. - m) * (1. - m);
      c *= exp2(uFill * wt);
    }
    float Y = dot(LUMA, c);
    if (uFilmTone && Y > 1e-6){ float Yf = filmTone(Y); c *= Yf / Y; Y = Yf; }
    if (Y > 1e-6){
      float Y2 = Y;
      if (uHL < 0.) Y2 = Y / (1. - uHL * Y * Y); else if (uHL > 0.) Y2 = Y * (1. + uHL * Y * Y);
      float v = enc(Y2);
      if (uBlk != 0.) v = max(0., (v - uBlk) / (1. - uBlk));
      if (uCT != 0.){ v = clamp(v, 0., 1.); float s = v * v * (3. - 2. * v); v += uCT * (s - v); }
      float Y3 = dec(v);
      c *= Y3 / Y; Y = Y3;
    }
    float f = uSatF;
    if (uMode == 6 || uMode == 7){
      float hx = c.r - 0.5 * (c.g + c.b), hy = 0.8660254 * (c.g - c.b);
      float hue = degrees(atan(hy, hx)); if (hue < 0.) hue += 360.;
      if (uMode == 6) f *= 0.86 + 0.12 * hueWeight(hue, 30., 35.);
      else f *= 1.05 + 0.15 * max(hueWeight(hue, 110., 60.), hueWeight(hue, 215., 45.));
    }
    if (uMono) c = vec3(Y); else if (f != 1.) c = Y + (c - Y) * f;
  }
  if (uGrain != 0.){
    uint i = uint(q.y * uW + q.x);
    if (uGrainMono){ float n = gauss(i, 0u); c = vec3(grain1(c.r, n), grain1(c.g, n), grain1(c.b, n)); }
    else c = vec3(grain1(c.r, gauss(i, 0u)), grain1(c.g, gauss(i, 1u)), grain1(c.b, gauss(i, 2u)));
  }
  // uint8(enc(v)*255 + 0.5)
  o = vec4(floor(vec3(enc(c.r), enc(c.g), enc(c.b)) * 255. + 0.5), 255.);
}`;
  // Pass 2: horizontal 7-tap Gaussian (sigma 1) of the 8-bit image.
  const BLURH = HEAD + `
uniform sampler2D uSrc; uniform int uW; uniform float uK[7];
void main(){ ivec2 q = ivec2(gl_FragCoord.xy); vec3 s = vec3(0.);
  for (int j = -3; j <= 3; j++) s += uK[j + 3] * texelFetch(uSrc, ivec2(clamp(q.x + j, 0, uW - 1), q.y), 0).rgb;
  o = vec4(s, 255.); }`;
  // Pass 3: vertical blur and the unsharp mask (sppsharpen.go), to 8 bits.
  const USM = HEAD + `
uniform sampler2D uSrc, uBlur; uniform int uH; uniform float uK[7], uA;
void main(){ ivec2 q = ivec2(gl_FragCoord.xy); vec3 s = vec3(0.);
  for (int j = -3; j <= 3; j++) s += uK[j + 3] * texelFetch(uBlur, ivec2(q.x, clamp(q.y + j, 0, uH - 1)), 0).rgb;
  vec3 v = texelFetch(uSrc, q, 0).rgb;
  o = vec4(floor(clamp(v + uA * (v - s), 0., 255.) + 0.5), 255.); }`;
  // Pass 4: the Sharpness slider's 3x3 cross (render.go sharpen), edges
  // untouched, then out to the canvas (flipped: canvas row 0 is the top).
  const OUT = HEAD + `
uniform sampler2D uSrc; uniform int uW, uH; uniform float uA;
void main(){ ivec2 q = ivec2(int(gl_FragCoord.x), uH - 1 - int(gl_FragCoord.y));
  vec3 v = texelFetch(uSrc, q, 0).rgb;
  if (uA != 0. && q.x > 0 && q.y > 0 && q.x < uW - 1 && q.y < uH - 1){
    vec3 n = (texelFetch(uSrc, q + ivec2(-1, 0), 0).rgb + texelFetch(uSrc, q + ivec2(1, 0), 0).rgb +
              texelFetch(uSrc, q + ivec2(0, -1), 0).rgb + texelFetch(uSrc, q + ivec2(0, 1), 0).rgb) / 4.;
    v = floor(clamp(v + uA * (v - n), 0., 255.) + 0.5);
  }
  o = vec4(v / 255., 1.); }`;

  function compile(fs){
    const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s)); return s; };
    const p = gl.createProgram();
    gl.attachShader(p, sh(gl.VERTEX_SHADER, VS)); gl.attachShader(p, sh(gl.FRAGMENT_SHADER, fs));
    gl.bindAttribLocation(p, 0, "p"); gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p));
    const u = {}; const n = gl.getProgramParameter(p, gl.ACTIVE_UNIFORMS);
    for (let i = 0; i < n; i++){ const name = gl.getActiveUniform(p, i).name.replace(/\[0\]$/, ""); u[name] = gl.getUniformLocation(p, name); }
    return {p, u};
  }
  function init(cv){
    canvas = cv;
    gl = cv.getContext("webgl2", {antialias: false, depth: false, premultipliedAlpha: false, preserveDrawingBuffer: true});
    if (!gl) return false;
    if (!gl.getExtension("EXT_color_buffer_float")) return false;
    progs = {pix: compile(PIX), blurh: compile(BLURH), usm: compile(USM), out: compile(OUT)};
    quad = gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER, quad);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0); gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    return true;
  }
  function tex(w, h, ifmt, fmt, type, data){
    const t = gl.createTexture(); gl.bindTexture(gl.TEXTURE_2D, t);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    gl.texImage2D(gl.TEXTURE_2D, 0, ifmt, w, h, 0, fmt, type, data);
    for (const k of [gl.TEXTURE_MIN_FILTER, gl.TEXTURE_MAG_FILTER]) gl.texParameteri(gl.TEXTURE_2D, k, gl.NEAREST);
    for (const k of [gl.TEXTURE_WRAP_S, gl.TEXTURE_WRAP_T]) gl.texParameteri(gl.TEXTURE_2D, k, gl.CLAMP_TO_EDGE);
    return t;
  }
  // loadBase fetches and uploads the Base of a render URL's query (key: when
  // it changes, the Base is fetched again).
  async function loadBase(query, key){
    if (base && base.key === key) return base;
    const buf = await (await fetch("/api/gpu/base?" + query)).arrayBuffer();
    const dv = new DataView(buf), hl = dv.getUint32(0, true);
    const hdr = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, hl)));
    let off = 4 + hl; off += off % 2;
    const w = hdr.w, h = hdr.h, n = w * h;
    const rgb = new Uint16Array(buf, off, n * 3), aux = new Uint16Array(n * 3), a = new Uint16Array(buf, off + n * 6, n * 3);
    for (let j = 0; j < n; j++){ aux[j * 3] = a[j]; aux[j * 3 + 1] = a[n + j]; aux[j * 3 + 2] = a[2 * n + j]; }
    if (base){ gl.deleteTexture(base.rgb); gl.deleteTexture(base.aux); }
    base = {key, w, h, hdr,
      rgb: tex(w, h, gl.RGB16F, gl.RGB, gl.HALF_FLOAT, rgb),
      aux: tex(w, h, gl.RGB16F, gl.RGB, gl.HALF_FLOAT, aux)};
    for (const f of fbs){ gl.deleteTexture(f.tex); gl.deleteFramebuffer(f.fb); }
    fbs = [0, 1, 2].map(() => { const t = tex(w, h, gl.RGBA16F, gl.RGBA, gl.HALF_FLOAT, null);
      const fb = gl.createFramebuffer(); gl.bindFramebuffer(gl.FRAMEBUFFER, fb);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, t, 0); return {tex: t, fb}; });
    canvas.width = w; canvas.height = h;
    return base;
  }
  function uploadLuts(r){
    const rows = 17, W = N + 1, d = new Float32Array(rows * W);
    const put = (row, a, from = 0) => { if (a) d.set(a.slice(from, from + W), row * W); };
    put(0, r.pre, 0); put(1, r.pre, W); put(2, r.post, 0); put(3, r.post, W);
    if (r.fillABCK) for (let k = 0; k < 4; k++) put(4 + k, r.fillABCK, k * W);
    if (r.fillLocal) put(8, r.fillLocal, 64);
    for (const [c, row] of [[r.wb, 9], [r.modeMap, 13]]){
      if (!c) continue;
      if (c.tone) put(row + 3, c.tone); else for (let k = 0; k < 3; k++) put(row + k, c.curves, k * W);
    }
    if (luts) gl.deleteTexture(luts);
    luts = tex(W, rows, gl.R32F, gl.RED, gl.FLOAT, d);
    if (film) { gl.deleteTexture(film); film = null; }
    if (r.filmTone) film = tex(r.filmTone.length, 1, gl.R32F, gl.RED, gl.FLOAT, new Float32Array(r.filmTone));
  }
  const rows3 = m => m ? [m.slice(0, 3), m.slice(3, 6), m.slice(6, 9)].flat() : [1, 0, 0, 0, 1, 0, 0, 0, 1];
  const K7 = (() => { const k = []; let s = 0; for (let i = -3; i <= 3; i++){ k.push(Math.exp(-i * i / 2)); s += Math.exp(-i * i / 2); } return k.map(v => v / s); })();
  function pass(prog, target){
    gl.useProgram(prog.p);
    gl.bindFramebuffer(gl.FRAMEBUFFER, target ? target.fb : null);
    gl.viewport(0, 0, base.w, base.h);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }
  function bind(prog, name, unit, t){ gl.activeTexture(gl.TEXTURE0 + unit); gl.bindTexture(gl.TEXTURE_2D, t); gl.uniform1i(prog.u[name], unit); }
  // draw renders the Base with recipe r onto the canvas.
  function draw(r){
    if (!base) return false;
    uploadLuts(r);
    const P = progs.pix, u = P.u; gl.useProgram(P.p);
    bind(P, "uRGB", 0, base.rgb); bind(P, "uAux", 1, base.aux); bind(P, "uLut", 2, luts); bind(P, "uFilm", 3, film || luts);
    gl.uniform1i(u.uW, base.w); gl.uniform1i(u.uH, base.h);
    gl.uniform1i(u.uUndo, r.undo ? 1 : 0); gl.uniform1i(u.uUndoGamma, r.undoGamma ? 1 : 0);
    gl.uniform3fv(u.uUndoM, rows3(r.undo));
    gl.uniform1i(u.uLayerMode, r.layer ? 1 : r.layerMono ? 2 : 0);
    gl.uniform3fv(u.uLayerM, rows3(r.layer)); gl.uniform3fv(u.uLayerMono, r.layerMono || [0, 0, 0]);
    gl.uniform1i(u.uMeasured, r.measured ? 1 : 0); gl.uniform1i(u.uMono, r.mono ? 1 : 0);
    gl.uniform3fv(u.uGain, r.gain); gl.uniform1f(u.uMaskScale, r.maskScale); gl.uniform1f(u.uFill, r.fill);
    gl.uniform1f(u.uHL, r.hl); gl.uniform1f(u.uBlk, r.blk); gl.uniform1f(u.uCT, r.ct); gl.uniform1f(u.uSatF, r.satF); gl.uniform1i(u.uMode, r.mode);
    gl.uniform1i(u.uFilmTone, r.filmTone ? 1 : 0); gl.uniform1f(u.uFilmScale, r.filmToneScale || 0);
    for (const [c, on, taste, m, sat] of [[r.wb, "uWB", "uWBTaste", "uWBM", "uWBSat"], [r.modeMap, "uMM", "uMMTaste", "uMMM", "uMMSat"]]){
      gl.uniform1i(u[on], c ? 1 : 0); gl.uniform1i(u[taste], c && c.tone ? 1 : 0);
      gl.uniform3fv(u[m], rows3(c && c.matrix)); gl.uniform1f(u[sat], c ? c.sat : 0);
    }
    gl.uniform3fv(u.uTint, r.tint); gl.uniform1f(u.uSatMeasure, r.satMeasure);
    gl.uniform1i(u.uFillABCK, r.fillABCK ? 1 : 0); gl.uniform1i(u.uFillLocal, r.fillLocal ? 1 : 0);
    gl.uniform1fv(u.uLocal, r.fillLocal ? r.fillLocal.slice(0, 64) : new Float32Array(64));
    gl.uniform1f(u.uGrain, r.grainSigma || 0); gl.uniform1i(u.uGrainMono, r.grainMono ? 1 : 0);
    pass(P, fbs[0]);
    let cur = fbs[0];
    if (r.sppSharpen){
      const H = progs.blurh; gl.useProgram(H.p); bind(H, "uSrc", 0, cur.tex); gl.uniform1i(H.u.uW, base.w); gl.uniform1fv(H.u.uK, K7); pass(H, fbs[1]);
      const U = progs.usm; gl.useProgram(U.p); bind(U, "uSrc", 0, cur.tex); bind(U, "uBlur", 1, fbs[1].tex);
      gl.uniform1i(U.u.uH, base.h); gl.uniform1fv(U.u.uK, K7); gl.uniform1f(U.u.uA, r.sppSharpen); pass(U, fbs[2]);
      cur = fbs[2];
    }
    const O = progs.out; gl.useProgram(O.p); bind(O, "uSrc", 0, cur.tex);
    gl.uniform1i(O.u.uW, base.w); gl.uniform1i(O.u.uH, base.h); gl.uniform1f(O.u.uA, r.sharpen || 0);
    pass(O, null);
    return true;
  }
  // pixels reads the canvas back (RGBA, row 0 = top) — for the tests.
  function pixels(){
    const d = new Uint8Array(base.w * base.h * 4);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.readPixels(0, 0, base.w, base.h, gl.RGBA, gl.UNSIGNED_BYTE, d);
    const out = new Uint8Array(d.length), row = base.w * 4;
    for (let y = 0; y < base.h; y++) out.set(d.subarray((base.h - 1 - y) * row, (base.h - y) * row), y * row);
    return out;
  }
  return {init, loadBase, draw, pixels, get base(){ return base; }};
})();
