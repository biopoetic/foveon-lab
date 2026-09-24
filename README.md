# Foveon Lab

Lokalna aplikacija za isprobavanje Sigma Photo Pro (SPP) preseta na SD1 / SD15 X3F slikama.

```
go build -o foveon-lab.exe .
foveon-lab.exe            # skenira Desktop, preseti iz "Desktop\foveon pack", otvara preglednik
foveon-lab.exe -photos "D:\Foto;E:\Sigma" -presets "D:\preseti" -port 8777
```

## Što radi

- Pronalazi `*.X3F` (i `SDIM*.JPG`) do 4 razine ispod zadanih mapa.
- Čita **JPEG ugrađen u X3F** (SD1: 4704×3136, SD15: 2640×1760) — bez dekodiranja Foveon RAW-a.
- Učitava sve SPP XML presete (decimalni zarez, identične kopije spojene), filtrira po aparatu/grupi/datoteci.
- Mreža: ista slika kroz sve filtrirane presete. Klik → detalj s klizačima, ←/→ kroz presete, `O` za original.
- **Spremi kao preset** → `<mapa preseta>\FoveonLab_<SD1|SD15>.xml` (isti naziv se zamjenjuje, ne duplicira).
  SPP spaja uvezene XML-ove, pa ponovni uvoz iste datoteke u SPP duplicira presete.
- **Izvezi JPEG** → `<mapa slike>\foveon-lab\<slika>_<preset>.jpg` u punoj rezoluciji pregleda.

## Ograničenja (važno)

- Obrada je **emulacija** (`render/`): Sigmini algoritmi (X3 Fill Light, color modes, highlight) nisu javni.
  Smjer i relativna jačina preseta su vjerni, pikseli nisu identični SPP-u.
- Polazna slika je JPEG iz aparata — već ima aparatov color mode (često *Vivid*). Opcija
  "Kompenziraj JPEG aparata" to poništava (`render.CompensationFor`, iz X3F PROP `CM_DESC`/`SATU_DESC`/`CONT_DESC`).
- Color mode kodovi: 4 = Standard, 6 = Portrait, 7 = Landscape (izvedeno iz dokumentacije packa); ostali nepoznati.
- WhiteBalance / ColorTemp se čuvaju i zapisuju, ali se ne emuliraju.

## Struktura

- `x3f/` — X3F kontejner: direktorij sekcija, ugrađeni JPEG-ovi, PROP metapodaci
- `preset/` — SPP XML čitanje/pisanje
- `render/` — linearni float pipeline koji emulira SPP klizače
- `web/index.html` — sučelje (ugrađeno u exe)
