# Internationalization (i18n)

neXal@home (macOS) and neXal Home (iOS) share one workflow. English is the source; the
English text is the key. Each language is one file per app:

```
macos/Resources/<code>.lproj/Localizable.strings
../nexal-ios/Resources/<code>.lproj/Localizable.strings
```

`languages.json` lists the launch languages (20 plus English). `targets.json` says where each
app's sources and strings live. The iOS target is skipped when that repo is not checked out
next to this one.

## For developers

* Write English UI text in SwiftUI literals (`Text("Connected")`, `Label`, `Button`, `Toggle`,
  `.help`, ...). They localize automatically.
* For anything that is not a SwiftUI literal (alerts, computed `String`, error text) use
  `L10n.tr("English text %@", value)` or `String(localized: "...")`. Never glue translated
  fragments into a sentence; give the whole sentence a key with placeholders.
* Interpolated literals become placeholders: a `String` is `%@`, an integer `%lld`. The extractor
  guesses integers from the expression (`.count`, `Int(...)`, ...). If it guesses wrong the string
  silently stays English; write `L10n.tr` with an explicit `%lld`/`%@` instead.
* After changing UI text run `python3 i18n/scripts/l10n.py extract`. It rewrites `en.lproj` and adds
  new keys to every other language marked `TODO-TRANSLATE` (shown in English until translated).
* `python3 i18n/scripts/l10n.py check` is the gate (also in CI): valid syntax, same keys as English,
  same placeholders, no empty values, every language listed in `Info.plist`.
  `check --strict` also fails on untranslated strings; `status` shows progress per language.
* Add a language: `python3 i18n/scripts/l10n.py add-language xx --name "Name" --native "Native"`,
  add `<string>xx</string>` to `CFBundleLocalizations` in each Info.plist.
* Dates, numbers, sizes and relative times must use system formatters (`Date.formatted`,
  `ByteCountFormatter`), never hand-built English. Use `.leading`/`.trailing`, never left/right, so
  Arabic and Hebrew mirror correctly.

## Not covered yet

* Text produced outside the apps: connector (Go) messages shown in the Mac UI, coordinator
  responses and emails, the website, App Store listing. Those need their own pass.
* Strings the extractor cannot see (a `String` built elsewhere and passed to `Text(variable)`).
  Search for `Text(` with a variable argument when auditing; move the literal to `L10n.tr`.
* Plural rules: use separate full-sentence keys for one/many until a `.stringsdict` is added.

## Translating

See `AGENT-BRIEF.md` (instructions for one translator per language) and `GLOSSARY.md`.
