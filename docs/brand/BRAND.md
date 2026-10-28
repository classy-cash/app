# Classy Cash

## Identity

**Classy Cash** helps school treasurers organise contributions, record incoming payments and show parents what remains to pay. Parents pay the treasurer directly. The application does not hold or transfer money.

Primary language: Polish. Tone: calm, clear and practical. Explain actions in everyday terms; keep hosting internals out of the parent experience.

- Display name: **Classy Cash**.
- GitHub organisation: `classy-cash`.
- Technical identifier/domain prefix: `classycash`.
- Main line: **Składki klasowe pod kontrolą.**
- Supporting line: **Wpłaty, składki i rozliczenia klasy w jednym miejscu.**
- Payment explanation: **Pieniądze trafiają bezpośrednio do skarbnika.**
- Website actions: **Zobacz demo** and **Uruchom na swoim serwerze**.
- Demo: `https://skarbiec.mleczki.pl`.
- Product website target: `https://classy-cash.github.io/`.

## Existing visual identity

Extend the design of the original application rather than introduce a different identity. The symbol is `assets/images/favicon.svg`; palette and typography live in `assets/brand/tokens.css` and the application stylesheet (`internal/web/static/app.css`).

| Role | Color | Application |
| --- | --- | --- |
| Primary | `#1a2a52` | Navigation, primary buttons, logo |
| Text | `#15203d` | Main body/headings |
| Amber | `#e8b441` | Accent, highlighted actions, symbol |
| Emerald | `#2aa57f` | Positive accents and charts |
| Ivory | `#f7f6f0` | Page background |
| White | `#ffffff` | Cards/forms |
| Border | `#e2e6ee` | Structural boundaries |
| Muted | `#6b7796` | Secondary/decorative information; check text contrast |
| Danger | `#d65a4a` | Error accents; pair with a readable text label |

Use Inter for controls and body text, Playfair Display for selected product/page headings, and a system/Georgia fallback. Package licensed font files locally in the website/app implementation where appropriate. SVG wordmark sources contain live text with a Georgia fallback; render final exports with the approved fonts installed and verify them before publishing.

Keep amber buttons paired with navy text. Use a label/icon alongside each status color. Check WCAG contrast at the actual size and background; brand accent colors are not automatically suitable for small text or white labels.

## Assets

Sources are in `assets/brand/`:

- `icon.svg`: square symbol for avatars and icons.
- `logo.svg`: navy wordmark for light backgrounds.
- `logo-on-dark.svg`: ivory wordmark for dark backgrounds.
- `social-card.svg`: 1200 × 630 product preview source.
- `tokens.json` and `tokens.css`: shared palette, type and layout values.
- `site.webmanifest`: standalone-app metadata and icon references.

Retain at least one quarter of the symbol's height as clear space. Do not stretch, add shadows to, or recolor individual logo strokes. Use the icon at 24 px or larger; inspect the existing simplified favicon at smaller sizes. Keep full logo artwork at least 180 px wide on normal-density screens.

Publication exports include `icon-SIZE.png` at 16/32 (favicon), 180 (Apple), 192/512 (manifest), 512/1024 (GitHub avatar); `logo.png` and `logo-on-dark.png`; `social-card.png` (1200 × 630); and `repository-cover.png` (1280 × 640). Regenerate them with `sh scripts/export-brand-assets.sh` (requires `rsvg-convert`). Existing `assets/images/` contains favicon/touch assets to compare and reuse. SVG text uses locally installed fonts with the documented fallbacks; review final typography when applying the assets to the public pages.

## Layout and components

- Comfortable content width: 1120 px; 16–24 px mobile page gutters.
- Spacing scale: 4, 8, 12, 16, 24, 32, 48, 64, 96 px.
- Cards: white, 14 px corners, subtle border/shadow. Inputs: 10 px corners and visible focus ring.
- Prefer one clear primary action per view. Show the consequence of a payment-record change before saving it.
- Tables: readable amounts with currency, clear dates, keyboard access and a mobile layout that preserves labels.
- Notifications: state what happened and the next useful action. Avoid raw provider/SMTP/SSH messages in customer screens.
- Shared demo: persistent banner and next-reset countdown; explain that the dataset is shared and resets every 15 minutes.

## Application across surfaces

Apply the same identity to the organisation avatar/profile, product site, repo README/cover, Go app and login, setup wizard, help pages, demo banner and transactional emails. Publish only fictional demo screenshots; mark demo images as examples. Hide deployment credentials and use no external tracking scripts in the credential step.

Organisation profile copy is staged in `github-profile.md`. The application repository (`classy-cash/app`) is public and MIT-licensed, so the brand kit in `assets/brand/` ships with it; the organisation profile and the product site (`classy-cash.github.io`) live in their own repositories and are published from there. Adding or changing files in this kit does not publish anything by itself.
