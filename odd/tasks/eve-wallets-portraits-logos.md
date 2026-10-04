# eve-wallets-portraits-logos

## Objective
Show character portraits and corporation logos in the page.

## Decision (user)
Load the images directly from the EVE image server by allowing it in the CSP (`img-src 'self' https://images.evetech.net`); no server-side proxy.

## Verified
`https://images.evetech.net/characters/{id}/portrait?size=64` returns image/jpeg and `/corporations/{id}/logo?size=64` returns image/png, both 200, cache-control max-age=3600.

## Scope
- internal/web/web.go: CSP becomes `default-src 'self'; img-src 'self' https://images.evetech.net` (nothing else loosened), and its test in web_test.go.
- Front end: corporation logo in each corporation tab and in its panel title; character portraits next to each character wallet in the Characters tab (picker and latest table).

## Constraints
- URLs built only from numeric owner ids (validate with Number.isSafeInteger, otherwise no image). Decorative images: alt="" , fixed width/height (no layout shift), loading="lazy", decoding="async".
- An image that fails to load is removed via addEventListener("error") (no inline handlers, no inline style).
- Request size=64 for a ~32px display; no new third-party scripts or styles.
- Referrer-Policy no-referrer stays.

## Tasks
- [x] T1 CSP img-src + test.
- [x] T2 Logos in corp tabs/titles and portraits in the Characters tab, with error fallback, styles and UI tests; go vet and go test pass.

## Acceptance
- Only images.evetech.net is added to the CSP, and only for img-src.
- Broken or missing images leave no broken-image icon.
