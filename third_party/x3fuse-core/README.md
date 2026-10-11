# x3fuse-core (third-party, modified)

Foveon Lab ships `x3f_extract` from [x3fuse-core](https://github.com/sagwaco/x3fuse-core) to decode
**SD9 / SD10** raw data at full resolution (their X3F files carry no full-size preview).

- Upstream: https://github.com/sagwaco/x3fuse-core — release **v0.1.6**, commit `00d326f48f68eed2efc0a97e25e553c8a8b28442`
- License: Apache License 2.0 (`LICENSE`), attribution in `NOTICE` (includes the BSD notice of the
  Kalpanika x3f project it is derived from).
- **Modified by Foveon Lab:** `fix-type2-camf-key.patch` — the type-2 CAMF decryption key (SD9/SD10/
  SD14 files) must wrap at 32 bits like the C original; upstream kept it in a `u64`, which made every
  decode of those files fail with "unknown CAMF entry signature". Reported upstream as
  [sagwaco/x3fuse-core#26](https://github.com/sagwaco/x3fuse-core/issues/26). Output for other cameras
  is byte-identical to the upstream v0.1.6 binary.

`bin/` holds the locally built, stripped binary (not tracked in git; the release tool packs it). To
rebuild on Windows (GNU toolchain, no Visual Studio needed):

```
git clone --branch v0.1.6 https://github.com/sagwaco/x3fuse-core && cd x3fuse-core
git apply ../fix-type2-camf-key.patch
# rustup (x86_64-pc-windows-gnu), `pip install libclang`, then:
set LIBCLANG_PATH=<site-packages>\clang\native
set BINDGEN_EXTRA_CLANG_ARGS=--target=x86_64-w64-mingw32 -I<mingw64>\x86_64-w64-mingw32\include -I<mingw64>\lib\gcc\x86_64-w64-mingw32\<ver>\include
cargo build --release -p x3f-cli
strip target\release\x3f_extract.exe
```

Once upstream ships the fix, the official binaries can replace this build.
