# Demo

Self-hosted asciinema recording of `genestack-cli` — no asciinema.org needed.

- `demo.cast` — asciicast **v2**, idle-trimmed (~22 min runtime). 31 MB on disk
  but gzips to ~0.65 MB, which is what the browser actually downloads (GitHub
  serves it gzip-compressed) and what git stores.
- `demo.html` — embeds [asciinema-player](https://github.com/asciinema/asciinema-player)
  from a CDN and plays `demo.cast`.

## View it

Serve the folder over HTTP (the player fetches `demo.cast`, so `file://` won't work):

```bash
cd docs/demo && python3 -m http.server 8000
# open http://localhost:8000/demo.html
```

Or publish via **GitHub Pages**: Settings → Pages → *Deploy from a branch* →
branch `main`, folder **`/docs`** (Pages only allows `/` or `/docs`, not
arbitrary folders). Then share
`https://<user>.github.io/genestack-cli/demo/demo.html`.

## Regenerate

```bash
# trim idle gaps to 2s, then downgrade v3 -> v2 for player compatibility
asciinema convert <raw>.cast -f asciicast-v2 demo.cast
```
