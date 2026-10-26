![Classy Cash - Nowoczesne zarządzanie finansami dla skrbnika klasowego](assets/images/classy-cash-banner.png)

[![Build and QA](https://github.com/mleczakm/classy-cash/actions/workflows/build-and-qa.yml/badge.svg)](https://github.com/mleczakm/classy-cash/actions/workflows/build-and-qa.yml)
![Test coverage](https://raw.githubusercontent.com/mleczakm/classy-cash/coverage-master/docs/coverage.svg)

**Classy Cash** to nowoczesna aplikacja webowa stworzona z myślą o skarbnikach szkolnych. Jej zadaniem jest automatyzacja zbierania składek, monitorowanie wpłat oraz ułatwienie rozliczeń z rodzicami i uczniami.

## Tests and code quality

Start PostgreSQL before running database-backed tests:

```bash
docker compose up -d db
```

| Goal | Command |
| --- | --- |
| All tests | `docker compose run --rm php composer tests` |
| Unit tests | `docker compose run --rm php bin/phpunit --group unit` |
| Unit and functional coverage | `docker compose run --rm -e XDEBUG_MODE=coverage php composer coverage` |
| Smoke tests | `docker compose run --rm php composer tests:smoke` |
| Functional tests | `docker compose run --rm php composer tests:functional` |
| Full quality gate | `docker compose run --rm php composer qa` |

CI publishes Clover, Cobertura, and HTML reports as the `test-coverage` artifact and updates the coverage badge above. As in Kiddo, pull requests must cover at least 50% of executable lines added or changed.

## 🚀 Szybki start (Docker)

Aplikacja jest dostarczana jako obraz kontenerowy, co zapewnia powtarzalność środowiska. Do poprawnego działania wymagana jest baza danych **PostgreSQL**.

### Zmienne środowiskowe
Przed uruchomieniem upewnij się, że przekazałeś niezbędne parametry połączenia:

* `DATABASE_URL`: DSN do bazy PostgreSQL (np. `postgresql://user:password@db:5432/classy_cash?serverVersion=16&charset=utf8`)
* `APP_SECRET`: Unikalny ciąg znaków dla bezpieczeństwa sesji Symfony.
* `MAILER_DSN`: Konfiguracja wysyłki wiadomości e-mail (wspierane wszystkie formaty DSN zgodne z Symfony Mailer).

### Przykładowe uruchomienie
```bash
docker run -d \
  --name classy-cash \
  -e DATABASE_URL="postgresql://db_user:db_pass@host:5432/db_name" \
  -e MAILER_DSN="smtp://user:pass@smtp.example.com:587" \
  -p 8080:80 \
  mleczakm/classy-cash:latest
```

---

## 🛠️ Pierwsza konfiguracja (Onboarding)

Aplikacja została zaprojektowana tak, aby proces wdrożenia był jak najprostszy. Po pierwszym uruchomieniu zostaniesz przeprowadzony przez dwa kluczowe etapy:

### 1. Tworzenie Administratora
Przy pierwszym wejściu na stronę główną, system automatycznie przekieruje Cię do ekranu **rejestracji pierwszego użytkownika**. To konto otrzyma pełne uprawnienia administracyjne.

### 2. Panel Konfiguracyjny
Po pierwszym zalogowaniu się administratora, wyświetlony zostanie ekran konfiguracji wstępnej, gdzie należy ustawić:

* **Dane Płatności:**
    * Numer telefonu do otrzymywania przelewów na telefon **BLIK**.
    * **Numer rachunku bankowego** do przyjmowania standardowych przelewów tradycyjnych.
* **Automatyczne potwierdzanie płatności:**
    * Konfiguracja automatycznego księgowania (aktualnie wspierany jedynie **Alior Bank** poprzez parsowanie powiadomień e-mail na koncie **Gmail**).
    * Wymagane podanie użytkownika i hasła (zalecane hasło aplikacji) do konta Gmail.
* **Konfiguracja Mailera:**
    * Ustawienie parametrów **DSN** dla powiadomień wychodzących (system wspiera wszystkie formaty obsługiwane przez Symfony).

---

## ✨ Funkcje systemu

* ✅ **Centralny rejestr składek** – przejrzysta lista wpłat bez papierowych zeszytów.
* ✅ **Płatności mobilne** – obsługa przelewów na telefon BLIK.
* ✅ **Automatyzacja (Alior + Gmail)** – oszczędność czasu dzięki automatycznemu rozpoznawaniu wpłat z potwierdzeń mailowych.
* ✅ **Powiadomienia** – systemowa wysyłka informacji do rodziców/uczniów.

---

## 🏗️ Technologia

Projekt oparty jest o framework **Symfony** i konteneryzację **Docker**.

> **Ważne:** Aplikacja wspiera wyłącznie bazę danych **PostgreSQL**.

---

## 🚀 Production Deployment

Aplikacja jest automatycznie wdrażana na serwer Mikrus przy użyciu GitHub Actions. Proces deployment jest uruchamiany przy każdym pushu tagu do repozytorium.

### Wymagane GitHub Secrets

Aby wdrożenie działało poprawnie, w ustawieniach repozytorium GitHub należy skonfigurować następujące sekrety:

**SSH i Mikrus:**
- `SSH_PRIVATE_KEY` - Klucz prywatny SSH do autoryzacji na serwerze Mikrus
- `MIKRUS_SSH_HOST` - Host serwera Mikrus (np. `karol115.mikrus.xyz`)
- `MIKRUS_SSH_PORT` - Port SSH (domyślnie `10115` dla Mikrus)
- `MIKRUS_IPV6` - Adres IPv6 serwera Mikrus

**Aplikacja:**
- `DOTENV` - Zmienne środowiskowe dla aplikacji w formacie .env (np. `DATABASE_URL=..., APP_SECRET=..., MAILER_DSN=...`)

**Cytrus:**
- `CYTRUS_IPV4` - Adres IPv4 usługi Cytrus (np. `135.181.95.85`)
- `CYTRUS_API_TOKEN` - Token API Cytrus

**Cloudflare:**
- `CLOUDFLARE_API_TOKEN` - Token API Cloudflare z uprawnieniami do edycji DNS
- `CLOUDFLARE_ZONE_ID` - ID strefy Cloudflare dla domeny

### Proces wdrożenia

1. **Build** - Obraz Docker jest budowany i pushowany do GitHub Container Registry (GHCR)
2. **Deploy** - Ansible wdraża aplikację na serwer Mikrus:
   - Pobiera najnowszy obraz z GHCR
   - Zatrzymuje i usuwa stary kontener
   - Uruchamia nowy kontener z odpowiednią konfiguracją
   - Weryfikuje domenę w Cytrus
   - Aktualizuje rekord DNS w Cloudflare

### Konfiguracja domeny

Przed wdrożeniem upewnij się, że domena jest skonfigurowana w panelu Cytrus. Ansible automatycznie zweryfikuje, czy domena znajduje się na liście domen dostępnych w Cytrus.

---

### Tryb przerwy technicznej

Przed planowanymi pracami na bazie lub infrastrukturze uruchom workflow **Enable Maintenance Mode**
(zakładka Actions, opcjonalnie z tekstem „Wracamy: …”). Podmienia on kontener `classy-cash` na statyczną
zaślepkę nginx w stylu aplikacji, na tym samym adresie i porcie, odpowiadającą na każde żądanie kodem `503`
z `Retry-After`.

Nie potrzebuje budowania obrazu, logowania do GHCR, `.env` ani bazy. Wykonuje ten sam krok Cytrus +
Cloudflare co zwykły deploy (bez nich strona i tak byłaby niedostępna), więc wymaga tych samych sekretów
routingu. Każdy zwykły deploy zapisuje obraz nginx w cache na hoście.

Wyjście z trybu przerwy: uruchom ponownie ostatni run **Production Build and Deploy** (zwykły deploy zastępuje
zaślepkę). W trakcie przerwy nie wypychaj nowych tagów, bo każdy tag wdraża aplikację ponownie.

## 📄 Licencja

Oprogramowanie jest własnościowe (proprietary); wszelkie prawa zastrzeżone. Repozytorium jest prywatne. Szczegóły w pliku LICENSE.

---
*Stworzone, aby ułatwić życie każdemu skarbnikowi.* 🎓
