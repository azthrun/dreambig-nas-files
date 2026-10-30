# One Go container with server-side rendering

One process serves pages, file operations and Uploads from a single container, written in Go with `html/template`, embedded `tusd` and minimal JavaScript. The UI and API share one release and one set of data, so splitting them would only add a hop that large file streams must be proxied through, and more memory use on a DS220+. Go gives a single static binary, streaming I/O and direct reuse of file-api's tus approach. WebAssembly is not used because nothing here needs it; JavaScript is limited to what SSR cannot do, mainly resumable Uploads and their progress.

## Considered Options

- **Separate UI and API containers, or a worker and database as well**: rejected; no independent scaling or release need.
- **TypeScript (Hono or SvelteKit)**: rejected; matches `dreambig-nas-mcp` but uses more memory and a larger image.
- **Rust (Axum + Askama)**: rejected; smallest footprint, but slower to build and no tus reuse.
- **.NET Blazor**: rejected; heaviest option.
