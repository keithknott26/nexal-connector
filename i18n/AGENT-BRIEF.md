# Brief for a translation agent (one agent per language)

You translate neXal's two apps into ONE target language. Your language code is given to you
(for example `es`, `pt-BR`, `zh-Hans`). The English source is `en.lproj/Localizable.strings`.

## Files you own (edit only these)

* `macos/Resources/<code>.lproj/Localizable.strings`
* `../nexal-ios/Resources/<code>.lproj/Localizable.strings`

If a file does not exist yet, create both with
`python3 i18n/scripts/l10n.py add-language <code>` (run it once; it does not touch other languages).
Do not edit any other language, any Swift file, `languages.json` or the scripts. Other agents are
working on the other languages in parallel; staying inside your two files means no merge conflicts.

## How to translate

1. Entries look like:
   ```
   /* TODO-TRANSLATE */
   "Update available: neXal@home %@" = "Update available: neXal@home %@";
   ```
   The key (left) never changes. Replace only the value (right) with a natural translation, then
   delete the `/* TODO-TRANSLATE */` line above it. Untouched entries stay English.
2. Preserve every placeholder exactly: `%@`, `%lld`, `%1$@`, `%2$lld`, `%%`. You may reorder them; when you
   reorder, switch to positional form (`%2$@ ... %1$lld`) so each still means the same value.
3. Preserve `\n`, `\"` and `\\` escapes, trailing punctuation, and the `…` character.
4. Follow `GLOSSARY.md`: never translate the listed names and technical terms, use Apple's own
   target-language wording for Apple features, and keep one rendering for each recurring term.
5. Keep length close to English. These are labels in narrow rows and buttons.
6. Use the `/* File.swift:123 */` comment in `en.lproj` for context (where the string appears). When a
   string is ambiguous, read that source line.
7. For right-to-left languages (`ar`, `he`) translate normally; the layout mirrors automatically. Do not
   add direction marks except where needed to keep a name or number readable.
8. Do not translate or "fix" the English. If an English string is wrong or ambiguous, leave a one-line
   note in your final report (do not edit it).

## Finish

* `python3 i18n/scripts/l10n.py check` must print `localization OK` (no errors). Warnings about
  remaining `TODO-TRANSLATE` entries mean you are not done.
* `python3 i18n/scripts/l10n.py status` should show your language at 100%.
* Commit only your two files by name with message `i18n(<code>): <Language> translation`. Do not push.
* Final report: count translated, any English strings you found unclear, any term you had to pick a
  rendering for (so a reviewer can check it).
