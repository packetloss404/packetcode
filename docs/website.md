# PacketCode website

The public product website is hosted on [GitHub Pages](https://packetloss404.github.io/packetcode/).
Its source is `site/`; the terminal application and release installers remain
the source of truth for product behavior and download names.

## Build and preview

The website uses plain HTML, CSS, and JavaScript. Its build needs Node.js 20 or
newer (CI uses Node.js 24) and adds no runtime framework or package dependencies:

```bash
npm run build:site
npm run preview:site
```

Open `http://127.0.0.1:4177/packetcode/`. Set `SITE_PORT` to select another port.
The preview intentionally includes the `/packetcode/` prefix used by GitHub
project Pages, so relative links are checked under their real deployment path.

`scripts/build-site.mjs` produces `dist/site/`. It copies the website, original
app screenshots, documentation, and root reference documents needed by the
offline HTML manual's links. It generates `.nojekyll` and leaves other files
in `dist/`, including compiled release artifacts, intact. Generated output is
ignored by Git and is rebuilt for every deployment.

Use relative URLs for internal page links and assets. Downloads point to the
repository's latest release assets; the website does not host executable files
or implement a separate installer.

## App screenshots

The gallery and README use the same real executable captures in `docs/images/`.
They show a Codex conversation, a pending file-edit approval, and completed
background reviews in an isolated example project. The
[capture notes](images/README.md) record the app version, release checksum,
capture method, and regeneration tools. Click a gallery image to open the
enlarged viewer, zoom with + / −, fit the image to the screen, or open the
complete original. Arrow keys pan and Escape closes the viewer; closing returns
keyboard focus to the selected screenshot. The wider gallery previews focus on
the app inside each capture. The website build copies the original image files
without alteration.

When replacing captures, update each gallery link's `data-screenshot-crop`
bounds so both the preview and enlarged viewer focus on the current app area.

## Browser checks

Install the repository's pinned browser-test dependencies, then check the
actual static build in Chromium:

```bash
npm ci --ignore-scripts
npx playwright install chromium
npm run build:site
npm run test:site
```

The tests cover desktop and mobile layouts, navigation under `/packetcode/`,
installation tabs, clipboard commands, workspace screenshot tabs, the six
download targets, and the getting-started guide and offline manual. Failure
traces and screenshots are saved under `test-results/site/`, with the browser
report in `playwright-report/site/`. The existing offline-manual suite remains
available separately through `npm run test:browser`.

## GitHub Pages deployment

In the repository's **Settings → Pages**, select **GitHub Actions** as the build
source. This is a one-time repository setting; keep the `github-pages`
environment's deployment rules restricted to `main`.

The [Website workflow](../.github/workflows/pages.yml) validates relevant pull
requests and builds/deploys after website or documentation changes reach
`main`. It can also be run manually from `main`. Browser checks must pass before
an artifact is uploaded. The build job has read-only repository access; only
the deployment job receives `pages: write` and `id-token: write`. No custom
deployment token, publishing branch, or paid hosting service is required.

GitHub's [custom Pages workflow guide](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)
describes the upload/deployment actions, artifact format, required permissions,
and `github-pages` environment. For API-based setup, see the
[Pages REST API](https://docs.github.com/en/rest/pages/pages#create-a-github-pages-site).
