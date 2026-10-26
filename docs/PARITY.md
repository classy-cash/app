# Parity with the PHP application

The PHP/Symfony application in this tree is the behavioural reference. This page maps its features and
tests to the Go rewrite and lists every **deliberate** difference. "Tested" names Go tests that run in CI
(`go test -race ./...`); browser-level visual review was done by hand against a seeded instance (desktop and
375 px mobile) and is not automated.

## Feature matrix

| Area | PHP source | Go implementation | Tested by |
| --- | --- | --- | --- |
| First-owner setup | `Onboarding` live component | `/onboarding`, `store.Onboard` (claim token in prod) | `TestOnboardingClaimsInstanceOnlyOnce`, `TestProductionOnboardingNeedsClaimToken`, `TestOnboardOnlyOnce` |
| Login (magic link + password), logout | `LoginUser`, `SendLoginNotificationHandler` | `web/auth.go` | `TestMagicLinkIsTwoStepAndSingleUse`, `TestPasswordLoginAndThrottle`, `TestSessionRevokedOnPasswordChange` |
| Password reset (treasurer/anyone) | stub, disabled | e-mailed single-use link, revokes sessions | `TestPasswordResetFlowRevokesSessions`, `TestAuthTokensAreSingleUseAndExpire` |
| Parent registration flag | `RegisterUser`, `parent_registration` | `/register`, setting `parent_registration_enabled` | `TestParentRegistrationFlagAndSettings` |
| Profile | `profile.html.twig` | `/profile` (name, phone, password) | `TestEveryTreasurerPageRenders` |
| Roles: admin / treasurer / parent | `assertTreasurer`, `ClassRole` | `store.RoleIn/CanManage`, guards in `web/server.go` | `TestPermissionsAcrossRoles`, `TestPermissionsAndClassIsolation` |
| Classes, memberships, one treasurer per class | entities + unique index | `classes`, `memberships`, partial unique index | `TestPermissionsAndClassIsolation` |
| Students, parents, merge | `TreasurerController::mergeStudents` | `store/students.go` | `TestMergeStudents`, `TestPermissionsAcrossRoles` (class isolation) |
| Contributions and per-student obligations | `contributions()` | `CreateContribution`, `ContributionViews` | `TestFastPaymentBundlesAndIsStable`, `TestCancelObligation` |
| Payment codes, "Zapłać BLIKIEM" | `FastPaymentModalComponent` | `CreateFastPayment`, `/parent/pay` JSON, dialog + status polling | `TestFastPaymentBundlesAndIsStable`, `TestParentFastPaymentJSON` |
| Partial/complete settlement, over-/underpayment | `Payment::isPaid`, workflows | `attachTransfer`, `settlePayment` | `TestPartialThenCompletePayment`, `TestMatchByCodeSettlesAndBooksCash` |
| Manual income / expenses | `manualTransactions()` | `ManualIncome`, `ManualExpense` | `TestLedgerRunningBalanceAndManualMoney`, `TestPartialCashLeavesObligationOpen` |
| Payment states pending/paid/failed/refunded/expired | `workflow.yaml` | `Transition`, `ExpirePending` | `TestPaymentStateMachine` |
| Transfers, Alior parsing | `AliorMailParser`, `TransferMoneyParser` | `internal/alior`, `internal/money` | `alior_test`, `money_test` (legacy fixtures) |
| Code matching incl. split codes, 0/O | `TransferPaymentMatcher::matchByPaymentCode` | `internal/matcher.Candidates`, `matchTransfer` | `TestLegacyMatchingTable`, `TestCandidates*`, `TestMatchByCodeSettlesAndBooksCash` |
| Historical matching, ranking, hints | `matchByHistoricalData`, `buildMatchHints` | `historicalMatch`, `Suggestions` | `TestHistoricalMatchWhenSingleCandidate`, `TestHistoricalMatchRefusesAmbiguity`, `TestSuggestionsRankHistoricalStudentFirst` |
| Manual assign / reassign / unassigned | `AssignPaymentModalComponent`, `assignTransfer()` | `AssignTransfer`, `AssignTransferToObligation`, `UnassignTransfer` | `TestManualAssignReassignUnassign`, `TestAssignTransferToObligationWithoutPayment` |
| Late/unmatched retry (12 h) | `TriggerMatchPaymentForTransferForPastTransfers` | `RematchRecent` every minute | `TestDuplicateLookingTransferGoesToReview` |
| Unmatched-transfer admin e-mail | `TransferNotMatchedHandler` | `notifyUnmatched` (once per transfer) | `TestPartialThenCompletePayment` (mail rows), `mail_test` |
| Settlement confirmation e-mails | `StudentPaymentSettlementSubscriber`, `SendSettlementConfirmationEmailHandler` | `queueSettlementMail` | `TestMatchByCodeSettlesAndBooksCash` |
| Dashboard cards, recent operations, balance | `*Component` live components | `Summary`, `LedgerPage`, auto-refresh + "load more" in `app.js` | `TestLedgerRunningBalanceAndManualMoney`, `TestEveryTreasurerPageRenders` |
| Settings | `SettingsType`, `SettingsDtoMapper` | `/treasurer/settings` | `TestEveryTreasurerPageRenders` |
| Bank notifications | IMAP polling (disabled in prod since 2026-09-22) | signed webhook, quarantine, replay | `bankmail/handler_test.go`, `TestWebhookEndToEndThroughMux` |
| Maintenance mode | static page deploy | out of scope of the app (deployment layer) | – |

PHP tests that exercised Swoole workers, Doctrine connection resetting, Messenger middleware, IMAP provider
and resource sampling have no counterpart: those mechanisms do not exist in the Go design.

## Deliberate behavioural differences

1. **Cash ledger is rebuilt, not patched.** One income per `paid` payment (dated when it was paid, valued at the
   money actually received), one outflow per expense; "Suma w kasie" is the ledger balance. PHP computed the card
   differently from the ledger, counted pending general payments as income and could book one entry per payment
   from a single obligation.
2. **Manual cash is booked as paid immediately** (PHP created a `pending` payment that never completed).
3. **A payment is `paid` at the actual time** (PHP back-dated `paid_at` to creation).
4. **Manual assignment always settles** the payment (PHP's guard depended on `ROLE_ADMIN`). A transfer whose
   amount differs is still flagged "Niedopłata/Nadpłata" and the treasurer can mail the parent.
5. **Duplicate protection beyond the transport id**: a same-account/sender/title/amount transfer within 24 h is
   held for review instead of being silently matched or dropped.
6. **Quarantine instead of loss**: unrecognised or spoofed-sender bank mail is stored, shown to the treasurer
   and acknowledged to the Worker.
7. **Multiple classes** with a per-class treasurer, payee data (BLIK, account) and cash (PHP used
   `findOneBy([])` and one global class).
8. **Merged/deleted students are hidden** from rosters; PHP listed them.
9. **Contribution progress is derived** from obligations (PHP kept counters that drifted).
10. **Shared "pay now" is stable**: reopening returns the same code; selecting a subset splits the payment and
    removes orphaned empty ones.
11. **Accounts**: remember-me is 90 days (was 1 year); password reset works; unknown addresses on the login form
    get no "no account" e-mail (no enumeration); link logins need a confirmation click so mail scanners cannot burn
    them; `/logout` is POST-only.
12. **Onboarding** replaces the Gmail credential step with the bank-notification address and a claim code.
13. **Payment codes** are unique application-wide, 4 characters (longer only when the space fills), and retired
    once paid — as before; matching ignores `failed`/`refunded` payments.
14. **Money** is integer grosze, PLN only.
15. **Styling** is hand-written CSS on the brand tokens (no Tailwind/Node build), with local fonts, visible
    focus rings, status colours always paired with a label/icon, responsive tables and no inline styles/scripts.

## Not implemented (existed only as data fields or unwired templates in PHP)

* Expense attachments (the PHP entity had a path column but no upload UI).
* In-app notification centre (`Notification` entity without UI) and daily reminder e-mails (templates inherited
  from another project, never scheduled).
* The Gmail/IMAP import, Sentry/Swoole/Messenger plumbing, feature-flag bundle (replaced by one setting).
* The duplicate "Budżet Klasy (Trójka)" link on the parent dashboard (it pointed at the treasurer dashboard).
* Student *code* "CC-4B-JK" shown on the PHP student list was decorative and is not generated.

## Known gaps / unverified

* No automated browser/visual regression suite; screens were reviewed manually on a seeded instance.
* The webhook has been verified against Ceramiza's *contract* and unit/HTTP tests here, **not** against a real
  Alior message through the real Cloudflare Worker (that needs the infrastructure owned by Codex).
* SMTP/relay delivery is covered with an HTTP test double only.
