# Classy Cash

**Składki klasowe pod kontrolą.** Aplikacja pomaga skarbnikom klasowym organizować składki, zapisywać
wpłaty i pokazywać rodzicom, ile zostało do zapłaty. Rodzice płacą bezpośrednio skarbnikowi — aplikacja
**nie przelewa ani nie przechowuje pieniędzy**, tylko zapisuje i dopasowuje wpłaty (BLIK, przelew, gotówka).

> Oprogramowanie na licencji [MIT](LICENSE) © 2026 Michał Mleczko.
> Aplikacja jest dobrowolnie wspierana: zalogowani użytkownicy bez zaległych składek widzą dyskretny link
> „postaw autorowi kawę” ([suppi.pl/mleczakm](https://suppi.pl/mleczakm)). Nic nie jest wymagane;
> `CLASSYCASH_SUPPORT_URL=off` ukrywa link, inny adres https go zmienia. Przycisk **Sponsor** na GitHubie
> pochodzi z [.github/FUNDING.yml](.github/FUNDING.yml).

Classy Cash is one static Go binary with SQLite and no other services; it runs in 256 MB. It replaced the
original Symfony/PHP application, whose sources were removed from the tree (the last commit that contains them
is `22f832b`); what was ported and what deliberately differs is in [docs/PARITY.md](docs/PARITY.md).

## Quick start

```bash
make test                      # go vet + tests
make run                       # http://127.0.0.1:8080 (dev: log-only mail, no claim code needed)
```

Open `/onboarding` to claim the instance (administrator + class). Production needs
`CLASSYCASH_SESSION_SECRET` and a `CLASSYCASH_CLAIM_TOKEN`; see
[deploy/env.example](deploy/env.example).

```bash
make build                     # bin/classycash (static, CGO-free)
bin/classycash config-check    # validate the environment
bin/classycash backup out.db   # consistent, verified SQLite backup
make release                  # dist/*.tar.gz + SHA256SUMS (linux amd64/arm64)
make docker                    # 36 MB Alpine image
```

## Documentation

* [docs/INTEGRATION.md](docs/INTEGRATION.md) — configuration, health/readiness, the signed bank-mail webhook,
  the mail relay API, demo-mode contract, backups and release artifacts.
* [docs/PARITY.md](docs/PARITY.md) — what was ported from the PHP application, how it is tested, deliberate differences.
* [docs/brand/BRAND.md](docs/brand/BRAND.md) — brand guide; [docs/go-rewrite-plan.md](docs/go-rewrite-plan.md) — archived
  planning document (26 Oct 2026, superseded by the documentation above).

## Layout

```
cmd/classycash     server + operator CLI
internal/store     persistence and accounting rules (payments, ledger, matching, ingestion)
internal/web       HTTP handlers, templates, CSS/JS (embedded)
internal/bankmail  signed webhook          internal/mail   templates, relay/capture senders
internal/jobs      mail dispatch, expiry   internal/auth   argon2id, tokens, CSRF, signing
internal/{money,alior,matcher}  pure domain logic ported from PHP
migrations/        versioned SQL           assets/         brand kit and bundled fonts
```
