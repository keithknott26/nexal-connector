#!/usr/bin/env python3
"""neXal localization tooling (macOS app + iOS app).

Source language is English and the English text IS the key, which is how SwiftUI
(`Text("Connected")`), `String(localized:)`, `NSLocalizedString` and `L10n.tr` all look
strings up. Each language lives in `<target>/Resources/<code>.lproj/Localizable.strings`.

  l10n.py extract              regenerate en.lproj from the Swift sources, then sync
  l10n.py sync                 add keys missing from other languages (marked TODO-TRANSLATE)
  l10n.py check                validate every language; exit 1 on errors (CI gate)
  l10n.py status               per-language translation progress
  l10n.py add-language CODE    scaffold a language (e.g. es, pt-BR, zh-Hans)

Targets are listed in i18n/targets.json; a target whose folders do not exist (for example
the iOS repo is not checked out next to this one) is skipped.
"""
import argparse
import json
import os
import re
import sys
from collections import Counter

HERE = os.path.dirname(os.path.abspath(__file__))
I18N = os.path.dirname(HERE)
ROOT = os.path.dirname(I18N)
TODO = "TODO-TRANSLATE"

# Call sites whose first argument is user-visible text. A literal must follow the "(" directly,
# so `Text(verbatim: ...)` and `Text(someVariable)` are never picked up.
CALLS = [
    "Text", "Label", "Button", "Toggle", "Section", "GroupBox", "ProgressView", "Picker",
    "TextField", "SecureField", "Menu", "Link", "DisclosureGroup", "LabeledContent", "Stepper",
    "TextEditor", "Tab", "ShareLink", "NavigationLink",
    ".navigationTitle", ".help", ".accessibilityLabel", ".accessibilityHint", ".alert",
    ".confirmationDialog", ".badge", ".navigationBarTitle", ".placeholder", "L10n.tr",
]
CALL_RE = re.compile(r"(?<![A-Za-z0-9_])(" + "|".join(re.escape(c) for c in CALLS) + r")\(\s*\"(?!\"\")")
LOCALIZED_RE = re.compile(r"(?:String\(localized:|NSLocalizedString\()\s*\"(?!\"\")")
SKIP_DIRS = {"ThirdParty", "Tests", "build", ".build", "DerivedData"}
INT_HINT = re.compile(r"(\.count\b|\bInt\(|\.rounded\(|Port\b|port\b|Ms\b|Milliseconds\b|Percent\b)")
SPEC_RE = re.compile(r"%(?:\d+\$)?(?:l{0,2}[dDuU]|[@fFeEgGsScC]|l{1,2}[xX])")


# ---------- .strings parsing / writing ----------

class StringsError(Exception):
    pass


def parse_strings(text, path="<memory>"):
    """Returns [(key, value, comment)] in file order. Raises StringsError on bad syntax."""
    i, n, out, comment = 0, len(text), [], ""

    def err(msg):
        line = text.count("\n", 0, i) + 1
        raise StringsError(f"{path}:{line}: {msg}")

    def quoted():
        nonlocal i
        i += 1
        buf = []
        while i < n:
            c = text[i]
            if c == "\\":
                i += 1
                if i >= n:
                    err("dangling backslash")
                e = text[i]
                if e == "u" or e == "U":
                    hexd = text[i + 1:i + 5]
                    if not re.fullmatch(r"[0-9a-fA-F]{4}", hexd):
                        err("bad \\u escape")
                    buf.append(chr(int(hexd, 16)))
                    i += 4
                else:
                    buf.append({"n": "\n", "t": "\t", "r": "\r", '"': '"', "\\": "\\", "'": "'"}.get(e, "\\" + e))
            elif c == '"':
                i += 1
                return "".join(buf)
            else:
                buf.append(c)
            i += 1
        err("unterminated string")

    while i < n:
        c = text[i]
        if c.isspace():
            i += 1
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            if end < 0:
                err("unterminated comment")
            comment = text[i + 2:end].strip()
            i = end + 2
        elif text.startswith("//", i):
            end = text.find("\n", i)
            i = n if end < 0 else end
        elif c == '"':
            key = quoted()
            while i < n and text[i].isspace():
                i += 1
            if i >= n or text[i] != "=":
                err(f'expected "=" after key {key!r}')
            i += 1
            while i < n and text[i].isspace():
                i += 1
            if i >= n or text[i] != '"':
                err(f"expected a quoted value for {key!r}")
            value = quoted()
            while i < n and text[i].isspace():
                i += 1
            if i >= n or text[i] != ";":
                err(f'expected ";" after value of {key!r}')
            i += 1
            out.append((key, value, comment))
            comment = ""
        else:
            err(f"unexpected character {c!r}")
    return out


def esc(s):
    return s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n").replace("\t", "\\t")


def write_strings(path, entries):
    """entries: [(key, value, comment)]; written sorted by key for stable diffs."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    lines = []
    for key, value, comment in sorted(entries, key=lambda e: e[0]):
        if comment:
            lines.append("/* " + comment.replace("*/", "* /") + " */")
        lines.append(f'"{esc(key)}" = "{esc(value)}";')
        lines.append("")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))


def read_strings(path):
    with open(path, encoding="utf-8") as f:
        return parse_strings(f.read(), path)


def specifiers(s):
    return Counter(SPEC_RE.findall(s.replace("%%", "")))


# ---------- Swift extraction ----------

def parse_swift_literal(src, start):
    """src[start] is the opening quote. Returns (text with %@/%lld placeholders, end_index) or None."""
    i, n, buf = start + 1, len(src), []
    while i < n:
        c = src[i]
        if c == "\\":
            i += 1
            if i >= n:
                return None
            e = src[i]
            if e == "(":
                depth, j, expr = 1, i + 1, []
                while j < n and depth:
                    ch = src[j]
                    if ch == '"':  # a string nested in the interpolation
                        k = j + 1
                        while k < n and src[k] != '"':
                            k += 2 if src[k] == "\\" else 1
                        expr.append(src[j:k + 1])
                        j = k + 1
                        continue
                    depth += (ch == "(") - (ch == ")")
                    if depth:
                        expr.append(ch)
                    j += 1
                e_text = "".join(expr)
                buf.append("%lld" if INT_HINT.search(e_text) else "%@")
                i = j
                continue
            if e == "u" and src[i + 1:i + 2] == "{":
                close = src.find("}", i)
                buf.append(chr(int(src[i + 2:close], 16)))
                i = close + 1
                continue
            buf.append({"n": "\n", "t": "\t", "r": "\r", '"': '"', "\\": "\\", "'": "'", "0": "\0"}.get(e, e))
            i += 1
            continue
        if c == '"':
            return "".join(buf), i + 1
        if c == "\n":
            return None  # not a single-line literal
        buf.append(c)
        i += 1
    return None


def keep(text):
    stripped = SPEC_RE.sub("", text)
    return bool(re.search(r"[^\W\d_]", stripped)) and "\0" not in text


def swift_files(roots):
    for root in roots:
        for dirpath, dirs, files in os.walk(root):
            dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
            for name in files:
                if name.endswith(".swift"):
                    yield os.path.join(dirpath, name)


def extract_swift(roots, base):
    """Returns {key: 'File.swift:line'} for the first place each string appears."""
    found = {}
    for path in sorted(swift_files(roots)):
        try:
            with open(path, encoding="utf-8") as f:
                src = f.read()
        except (OSError, UnicodeDecodeError):
            continue
        for rx in (CALL_RE, LOCALIZED_RE):
            for m in rx.finditer(src):
                parsed = parse_swift_literal(src, m.end() - 1)
                if not parsed:
                    continue
                text, _ = parsed
                if text and keep(text):
                    line = src.count("\n", 0, m.start()) + 1
                    found.setdefault(text, f"{os.path.relpath(path, base)}:{line}")
    return found


# ---------- targets / languages ----------

def load_languages():
    with open(os.path.join(I18N, "languages.json"), encoding="utf-8") as f:
        return json.load(f)["languages"]


def load_targets():
    with open(os.path.join(I18N, "targets.json"), encoding="utf-8") as f:
        raw = json.load(f)
    out = {}
    for name, t in raw.items():
        sources = [os.path.normpath(os.path.join(ROOT, s)) for s in t["sources"]]
        strings = os.path.normpath(os.path.join(ROOT, t["strings"]))
        if all(os.path.isdir(s) for s in sources) and os.path.isdir(strings):
            out[name] = {"sources": sources, "strings": strings, "plist": t.get("plist")}
        else:
            print(f"note: target '{name}' skipped (folders not found)", file=sys.stderr)
    return out


def lproj(target, code):
    return os.path.join(target["strings"], f"{code}.lproj", "Localizable.strings")


def language_codes_present(target):
    codes = []
    if os.path.isdir(target["strings"]):
        for name in sorted(os.listdir(target["strings"])):
            if name.endswith(".lproj") and os.path.isfile(os.path.join(target["strings"], name, "Localizable.strings")):
                codes.append(name[:-6])
    return codes


# ---------- commands ----------

def cmd_extract(_args):
    for name, t in load_targets().items():
        found = extract_swift(t["sources"], ROOT)
        entries = [(k, k, loc) for k, loc in found.items()]
        write_strings(lproj(t, "en"), entries)
        print(f"{name}: {len(entries)} source strings -> {os.path.relpath(lproj(t, 'en'), ROOT)}")
        sync_target(name, t)
    return 0


def sync_target(name, t):
    en = {k: v for k, v, _ in read_strings(lproj(t, "en"))}
    for code in language_codes_present(t):
        if code == "en":
            continue
        path = lproj(t, code)
        have = {k: (v, c) for k, v, c in read_strings(path)}
        entries, added, dropped = [], 0, 0
        for key in en:
            if key in have:
                entries.append((key, have[key][0], have[key][1]))
            else:
                entries.append((key, en[key], TODO))
                added += 1
        dropped = len([k for k in have if k not in en])
        write_strings(path, entries)
        if added or dropped:
            print(f"{name}/{code}: +{added} new (marked {TODO}), -{dropped} obsolete")


def cmd_sync(_args):
    for name, t in load_targets().items():
        if not os.path.isfile(lproj(t, "en")):
            print(f"{name}: no en.lproj yet; run extract first", file=sys.stderr)
            return 1
        sync_target(name, t)
    return 0


def cmd_add_language(args):
    code = args.code
    langs = load_languages()
    if not any(l["code"] == code for l in langs):
        langs.append({"code": code, "name": args.name or code, "native": args.native or code, "rtl": False})
        with open(os.path.join(I18N, "languages.json"), "w", encoding="utf-8") as f:
            json.dump({"languages": langs}, f, ensure_ascii=False, indent=2)
            f.write("\n")
    for name, t in load_targets().items():
        en_path = lproj(t, "en")
        if not os.path.isfile(en_path):
            print(f"{name}: run extract first", file=sys.stderr)
            return 1
        if not os.path.isfile(lproj(t, code)):
            write_strings(lproj(t, code), [(k, v, TODO) for k, v, _ in read_strings(en_path)])
            print(f"{name}: created {os.path.relpath(lproj(t, code), ROOT)}")
    return 0


def check_target(name, t, langs):
    errors, warnings = [], []
    en_path = lproj(t, "en")
    if not os.path.isfile(en_path):
        return [f"{name}: en.lproj/Localizable.strings is missing (run extract)"], []
    try:
        en = {k: v for k, v, _ in read_strings(en_path)}
    except StringsError as e:
        return [str(e)], []
    declared = {l["code"] for l in langs}
    for code in language_codes_present(t):
        if code == "en":
            continue
        if code not in declared:
            warnings.append(f"{name}/{code}: not listed in languages.json")
        path = lproj(t, code)
        try:
            entries = read_strings(path)
        except StringsError as e:
            errors.append(str(e))
            continue
        seen = Counter(k for k, _, _ in entries)
        for key, n in seen.items():
            if n > 1:
                errors.append(f"{name}/{code}: duplicate key {key!r}")
        have = {k: (v, c) for k, v, c in entries}
        for key in en:
            if key not in have:
                errors.append(f"{name}/{code}: missing key {key!r} (run sync)")
                continue
            value, comment = have[key]
            if not value.strip():
                errors.append(f"{name}/{code}: empty translation for {key!r}")
            elif specifiers(value) != specifiers(key):
                errors.append(f"{name}/{code}: placeholders differ for {key!r}: "
                              f"{sorted(specifiers(key).elements())} vs {sorted(specifiers(value).elements())}")
        for key in have:
            if key not in en:
                warnings.append(f"{name}/{code}: obsolete key {key!r} (run sync)")
        todo = sum(1 for k, (v, c) in have.items() if c == TODO)
        if todo:
            warnings.append(f"{name}/{code}: {todo} of {len(en)} strings still marked {TODO}")
    plist = t.get("plist")
    if plist and os.path.isfile(os.path.join(ROOT, plist)):
        with open(os.path.join(ROOT, plist), encoding="utf-8") as f:
            text = f.read()
        for code in sorted(declared):
            if f"<string>{code}</string>" not in text:
                errors.append(f"{name}: {plist} CFBundleLocalizations is missing '{code}'")
    return errors, warnings


def cmd_check(args):
    langs = load_languages()
    errors, warnings = [], []
    for name, t in load_targets().items():
        e, w = check_target(name, t, langs)
        errors += e
        warnings += w
    for w in warnings:
        print("warning:", w)
    for e in errors:
        print("ERROR:", e)
    if errors:
        print(f"{len(errors)} error(s)")
        return 1
    print("localization OK" + (f" ({len(warnings)} warning(s))" if warnings else ""))
    return 1 if (args.strict and warnings) else 0


def cmd_status(_args):
    langs = {l["code"]: l for l in load_languages()}
    for name, t in load_targets().items():
        if not os.path.isfile(lproj(t, "en")):
            print(f"{name}: no en.lproj")
            continue
        total = len(read_strings(lproj(t, "en")))
        print(f"{name}: {total} strings")
        for code in langs:
            path = lproj(t, code)
            if not os.path.isfile(path):
                print(f"  {code:8} {langs[code]['name']:22} not started")
                continue
            todo = sum(1 for _, _, c in read_strings(path) if c == TODO)
            done = total - todo
            print(f"  {code:8} {langs[code]['name']:22} {done}/{total} ({100 * done // max(total, 1)}%)")
    return 0


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("extract").set_defaults(fn=cmd_extract)
    sub.add_parser("sync").set_defaults(fn=cmd_sync)
    c = sub.add_parser("check")
    c.add_argument("--strict", action="store_true", help="treat warnings (including untranslated strings) as errors")
    c.set_defaults(fn=cmd_check)
    sub.add_parser("status").set_defaults(fn=cmd_status)
    a = sub.add_parser("add-language")
    a.add_argument("code")
    a.add_argument("--name")
    a.add_argument("--native")
    a.set_defaults(fn=cmd_add_language)
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
