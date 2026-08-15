# dwbridge-kit — the pinned-by-proof UE4SS bundle

`ue4ss-1c1a1497.tar.gz` is the exact UE4SS build the whole Phase 4 stack was
proven against, cleaned of logs and crash dumps but otherwise byte-identical:

- **UE4SS v3.0.1 Beta #0, git SHA `1c1a1497`** (from its own startup banner)
- Proven injecting into the Dragonwilds **Windows** dedicated server (UE 5.6.1)
  under Wine on 2026-08-09 (`tools/ue4ss-wine-shim/README.md`), and running the
  dwbridge save channel in production since 2026-08-12.
- sha256: `50e5af4130c0d7dbc9cbe7898f6db4bfd661dc7743ec6e22f06d9680d0d69cc7`
- UE4SS is MIT-licensed (the `LICENSE` file rides inside the tarball), so
  committing and redistributing it in the Wine image is fine.

Why a committed binary instead of a download: this is a *nightly* — the
project's rolling release assets are replaced in place, so no URL + checksum
pair can promise these bytes tomorrow. The tarball in git is the only pin
that can't rot. Expect to replace it deliberately (new tarball, new proof)
when the game updates past what this build can signature-scan; do not bump it
casually.

What the tarball deliberately does **not** contain, because the Wine image
build overlays fresher copies from the repo:

- `ue4ss/Mods/dwbridge/` — the mod itself comes from `tools/dwbridge/Scripts`
  (its `dwbridge : 1` line is already present in the tarball's `mods.txt`).
- `version.dll` — built from `tools/ue4ss-wine-shim` sources by mingw during
  the image build.

The assembled kit lands at `/opt/dwbridge-kit` in `wkagent:*-wine`, laid out
exactly as it must appear next to the server exe
(`RSDragonwilds/Binaries/Win64/`). The agent's `POST /v1/bridge/install`
copies it there when the operator clicks "Install mod support" — only ever
when `ue4ss/` is absent, never as a silent overwrite.
