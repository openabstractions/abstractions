# OpenAbstractions Panel contract

Binds: none (an operator client of other services' contracts)

This page is the contract of the Panel, the `monitor` program: the window in
which a person sees what the runtime does and decides what each program may
do, the presenter that shows OA's questions, and the confirmation an
application's installer opens to register the application. It owns the
Panel's obligations as a client, who may drive it, its start pages, the shell
contract its skins and pages share, and application registration. The
services it calls keep their own contracts. [README.md](README.md) is the
walkthrough: running the Panel, its pages and skins.

Trust domain: one account on one machine. The Panel is an operator program
of the runtime on its account (RUN-A1) and reaches that runtime over the
native transport as itself. Its developer page on loopback HTTP admits any
local process that holds the per-run key. The key bounds that page's powers
and proves no caller.

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY" and "OPTIONAL" in this page are to be
interpreted as described in RFC 2119 and RFC 8174, when, and only when, they
appear in all capitals, as shown here.

A rule id such as `PANEL-S1` is declared once, in bold brackets before its
title, at the head of the rule it names; tests cite it the same way. A
retired id is never reused. Each rule ends with one of two marks. **Held
by** names the test or check that goes red without the rule. **Untestable
here** gives the reason no test holds it yet and names the package that
brings its scenario.

| letter | topic |
|---|---|
| A | admission: who may drive the Panel (S12's reserved meaning) |
| O | the Panel as a client of the services |
| P | start pages and landing |
| S | the shell contract: navigation, attributes, classes and readiness |
| V | visible text |
| Q | presenting questions and notifications |
| R | application registration |

`E` (error and outcome) and `X` (extension) are reserved and unused here.

## The Panel as a client

**[PANEL-O1] Public operator interfaces only.** Every Panel action MUST call
the owning service through the same resolved client an application uses,
and the Panel MUST keep no store of service state. Untestable here: the
Panel's non-test files still import service packages (Divergences); the
presenter split (panel audit P1-1) adds the check that `go list -deps` of the
presenter excludes every service implementation and provider module.

**[PANEL-O2] No runtime, no service files.** With no runtime answering, the
Panel MUST report the absence and open no provider or service file. Held by:
`monitor/card_panel_test.go` (`TestCardTableUnavailableWithoutARuntime`) and
`monitor/readiness_test.go` (`TestPanelReadinessRetainsPartialFailure`).

**[PANEL-O3] A named runtime is named everywhere.** A Panel given
`-runtime-endpoint` MUST also be given `-runtime-program`, the absolute path
of the executable that runtime runs as, and says in its readiness view which
runtime it reads. Setup refuses to write without `-runtime-state-dir`. Held
by: `monitor/readiness_test.go` (`TestPanelReadinessNamesTheExplicitRuntime`).

**[PANEL-O4] Native records keep their generated codecs.** Resource states,
caller observations, log usage, selection candidates/results and model work
progress MUST cross the private page boundary through their generated record
codecs. Page compositions retain presentation metadata such as registered
titles and account display names separately. The JavaScript decoder MUST read
the complete native record before rendering; technical details retain its
original text. Static codec resources contain public definitions and confer no
runtime authority. New handwritten JSON mirrors MUST be refused by the
shrink-only owner check; real compositions carry an exact field-signature and
reason in its projection roster. Held by: `scripts/record_mirror_sites_test.py`
(`test_planted_mirror_and_raised_baseline_refuse`),
`monitor/panel_records_test.go` (`TestPanelRecordCodecRoutesRemainStatic`,
`TestPanelRegistryCandidatesUseGeneratedCodec`),
`monitor/panel_records_page_test.js`, and the existing native/page tests for the
affected pages.

## Admission

**[PANEL-A1] The window binds no listener.** The installed Panel window MUST
answer its pages, data and actions inside the Panel process and open no
network listener. Requests from any origin other than the window's own page
are refused. Held by: `monitor/desktop_windows_test.go`
(`TestNativePanelWithoutWebView2BindsNothing`) and
`monitor/native_panel_bridge_test.go` (`TestNativePanelRefusesOtherOrigins`).

**[PANEL-A2] The developer page is keyed.** The developer mode
(`-native=false`) serves the Panel on loopback HTTP. Every action MUST carry
the per-run key in a header; the key is minted per run and never written to
disk. Every request MUST carry a loopback `Host` and a same-origin `Origin`.
A saved operation that names another endpoint, owner or history epoch is
refused. Held by: `monitor/application_register_test.go`
(`TestApplicationRoutesNeedTheKey`), `monitor/start_page_test.go`
(`TestLandingNeedsThePanelKey`) and `monitor/panel_resource_test.go`
(`TestPanelResourceRefusesOtherOrigins`).

## Start pages

**[PANEL-P1] A closed set of start pages.** `-page` MUST accept only these
names, and never a path or URL: `overview`, `questions`,
`permission-review`, `credentials`, `connect-deepseek`, `setup`,
`activity`, `register-application` and `remove-application`. Any other name
is refused before the Panel binds anything. Held by:
`monitor/start_page_test.go` (`TestPanelStartPagesAreClosedAndKeyed`,
`TestInvalidPanelStartPageDoesNotBind`).

**[PANEL-P2] Landing.** `/start` MUST land on Get started while a setup
component the person chose is unfinished; otherwise on Apps, a fresh install
included (P-J1a). Held by: `monitor/start_page_test.go`
(`TestLandingWithNoSetupChosenIsApps`,
`TestLandingWithUnfinishedSetupIsGetStarted`,
`TestLandingWithFinishedSetupIsApps`).

## The shell contract

**[PANEL-S1] Navigation.** `GET /panel/nav.json?k=<key>` MUST return
`{"items":[{"id","label","path","group","glyph":{"segoe","sf"},"badge"}],"selected":"<id>"}`
in sidebar order. `group` is `main` or `more`; `badge` is an integer or
absent; `segoe` is a Segoe Fluent Icons codepoint and `sf` an SF Symbol name.
`?path=` selects that page's item, and otherwise the landing page's. Held by:
`monitor/panel_shell_test.go` (`TestNavJSONFollowsTheShellContract`).

**[PANEL-S2] The native shell.** `?shell=native` on any page MUST omit the
HTML navigation and the page `h1`, and the page still sets `document.title`
to its label. Held by: `monitor/panel_shell_test.go`
(`TestNativeShellLeavesNavigationAndTitleToTheHost`).

**[PANEL-S3] Root attributes and the accent.** The document root MUST carry
`data-skin` (`windows`, `macos` or `web`), `data-shell` (`html` or `native`)
and `data-platform`. With the `windows` skin only, the root carries an inline
accent from the system; a host pushes a live change with
`oaSetAccent(accent, light2, dark1)` in either shell, and the hook takes
colours only. Held by: `monitor/panel_shell_test.go`
(`TestAccentHookTakesOnlyColours`, `TestShellLinksTheSkinWhenItExists`).

**[PANEL-S4] Readiness marks.** A page that owns its readiness declares
`data-ready-owner="page"` and calls `oaReady()` once its first reads finish,
which sets `data-ready="1"` on the root. For every other page the shell sets
`data-ready="load"` after load. Held by: `monitor/panel_shell_test.go`
(`TestShellReadyFallbackIsAlwaysServed`) and `monitor/panel_lint_test.go`
(`TestPagesCallingOaReadyOwnTheirReadiness`).

**[PANEL-S5] One list of classes and attributes.** Skins and pages MUST use
only the shell's listed classes and attributes. The classes have one source,
`monitor/static/panel_classes.json`, which the shell embeds and the page
rules read; the attributes are the shell's `panelShellAttributes`. Held by:
`monitor/panel_selector_contract_test.go`
(`TestSkinsStyleOnlyContractClasses`, `TestShellEmitsOnlyListedClasses`,
`TestShellAttributeListMatchesSpec`, `TestSelectorContractCatchesAGuess`).

## Visible text

**[PANEL-V1] A page shows state, never raw values.** A served page MUST show
no visible empty text input, no button without a status target, at most one
caption per section, and no path-, SID- or hash-shaped visible text. The
exemption attributes `data-oa-secret`, `data-oa-other` and
`data-oa-technical` exempt only their own element from only the rule each
exists for. Every allowance is one counted, shrink-only row of
`monitor/testdata/panel_lint.allowlist`. Held by:
`monitor/panel_lint_test.go` (`TestPanelRulesLint`,
`TestPanelRulesLintCatchesEachRule`, `TestSecretFieldsAreExempt`,
`TestTechnicalSummaryRule`).

## Questions and notifications

**[PANEL-Q1] The presenter follows the questions contract.** The Panel's
presenter MUST show each operator question under ASK-O14: in its own dialog
above the person's other windows, leaving keyboard focus with the application
in front. Its dialog entry point takes a Book occurrence and renders nothing
else. The outcome after an answer, and every Panel confirmation, report and
failure, goes to the Panel window's status line or to a notification.
Notifications use the platform's own notification service and the person's
settings. The presenter answers through the public operator interface and
keeps the exact answer shown. Held by: `monitor/native_asks_bridge_test.go`
(`TestNativeAsksBridgeUsesPublicOperatorAndRejectsInterruptedAnswer`,
`TestNativeAsksBridgeKeepsTrustedPermissionWarning`),
`monitor/choices_presenter_test.go`
(`TestChoicePresenterKeepsAppOptionsSeparateFromCloseAndPermissionChrome`),
`monitor/book_occurrence_test.go` and
`monitor/win/occurrence_windows_test.go`.

## Application registration

**[PANEL-R1] An installer's registration asks nothing.**
`-register-application <file>` MUST read the Facade's `ApplicationDescriptor`
from the file, resolve the placeholders `{app}`, `{local_app_data}` and
`{pictures}` at the start of the program and each activation argument only,
and refuse an unknown placeholder, a path that leaves its folder by file
identity, a control or format character, and an unknown, duplicate or
out-of-bounds field, before anything is shown. The program MUST lie inside
`{app}`, with one exception: an integration manifest may name the official
app's program outside `{app}` only when OA's compiled Setup catalog names
that official program for the manifest's app (its path below the account's
local application data and its Authenticode signer), the manifest's program
is that program (`identity.SameSubjectProgram`), and the Panel itself
verifies the catalog's signer on the file (`identity.VerifyFile`,
`asks.CodePublisher`). An install record or anything else a process of the
account can write is never the anchor. Any other program outside `{app}` is
refused. A script host (`identity.SharedInterpreterProgram`, or the
`pyvenv.cfg` rule) is never an app's program, inside `{app}` or out. Setup
reads manifests through the same check.
The run asks no question and opens no window (VISION 2026-10-06, amended
2026-11-10). It registers the manifest through the Panel's application.manage
Register and writes `application.read` on `app:<name>` for itself. The
same program asking again is updated in place by the service. A refused
registration, a name another program holds or an unreachable runtime, waits
in Apps as a Not connected row with Connect and Learn more for its program and
publisher; the Panel's record of the run names it. A Setup package's own
manifest record waits the same way only when its manifest lies inside that
app's Setup folder or names the program Setup's install record gives the app;
any other record is ignored and logged. Reading the rows asks nothing and
changes nothing. A title that reads as OpenAbstractions is refused, logged and
listed in Activity. Such a run prints no key. Held by:
`monitor/application_native_test.go`
(`TestAnInstallersRequestConnectsTheAppWithoutAQuestion`,
`TestTheSameProgramAskingAgainUpdatesSilently`,
`TestAReservedTitleIsRefusedAndListedInActivity`,
`TestAnUnreachableRuntimeLeavesTheAppNotConnected`),
`monitor/application_register_test.go`
(`TestANotConnectedRequestShowsInAppsAndConnectRegistersIt`,
`TestConnectRefusesARequestThatChanged`,
`TestAnInstallersRecordWaitsAndARegisteredNameDoesNot`,
`TestAnInstallersRecordWaitsOnlyUnderSetupsTrustRule`,
`TestMalformedManifestIsRefusedBeforeAnyPage`,
`TestARequestPageOpensApps`,
`TestAnInstallersPanelPrintsNoKey`),
`monitor/application_official_test.go`
(`TestOnlyTheCatalogsOfficialProgramMayLieOutsideTheAppFolder`,
`TestTheCatalogNamesOfficialProgramsOnlyWithASigner`),
`monitor/apps_pending_page_test.js` and
`monitor/application_folders_test.go`
(`TestTheManifestFileIsResolvedBeforeTheFacadeCheck`).

**[PANEL-R2] Replacement and removal.** A Not connected row whose name another
app holds names that app inline on the first Connect click, "This replaces
<title> connected now.", and replaces it only on the second click, through
Remove and Register, when the entry the row named is still registered. A
replacement that fails after the earlier entry was removed registers the
earlier entry again and says so. `-remove-application <name>` asks nothing:
it removes the entry, with its read rule and any waiting request, when the
entry's program is the one its uninstaller installed, and lists the removal in
Activity; any other entry stays and the refusal is logged. Held by:
`monitor/application_native_test.go`
(`TestAnotherProgramUnderAConnectedNameWaitsAndConnectReplacesOnTheClick`,
`TestAnUninstallerRemovesOnlyItsOwnProgramsEntry`),
`monitor/application_register_test.go`
(`TestReplacementNeedsTheEntryThePersonSaw`,
`TestAFailedReplacementRegistersTheEarlierEntryAgain`).

## Divergences

- The Panel's non-test files import the config, credentials, inference,
  logging, resource and router service packages and a provider's setup
  package (`PANEL-O1`).
- `monitor/README.md` lists three start pages and its Limits section says the
  window is Windows only; the `-page` flag's help and its error name five
  pages (`PANEL-P1`). A macOS window exists.
- The marker blocks of the class and attribute lists that
  `monitor/panel_selector_contract_test.go` reads are in the private research
  page `research/panel-redesign/SPEC.md` §6; that test reads them from this
  page once it next changes (`PANEL-S5`).
- `connect-deepseek` is a retired start page that W1-12 removes.

## Not built

- The presenter split and its dependency check (`PANEL-O1`; panel audit
  P1-1).
- The README test that fails when a `parsePanelStartPage` case is missing
  from the README (panel audit P1-4).
