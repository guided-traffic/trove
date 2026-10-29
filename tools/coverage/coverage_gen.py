#!/usr/bin/env python3
"""Coverage generator for trove.

Reads the feature catalog (docs/features/**/*.md) and the specification
(docs/spec/**/*.md), then renders docs/coverage/checklist.md completely and
the marked blocks of docs/coverage/README.md (between
"<!-- coverage-gen:begin NAME -->" and "<!-- coverage-gen:end NAME -->").
Prose outside the markers is never touched.

Usage:
    python3 -I coverage_gen.py --repo /path/to/trove          # write files
    python3 -I coverage_gen.py --repo /path/to/trove --check  # compare only

Exit codes:
    0  success
    1  data error: duplicate ID, Covers ID that does not exist, malformed
       entry or requirement, unclosed code fence, missing README marker,
       unknown audience page
    2  --check: a file on disk differs from what would be generated

Extraction rules (the README's "How the numbers are produced" states them):
    * Lines inside fenced code blocks (``` or ~~~) are ignored, so the format
      examples in docs/features/README.md and docs/spec/README.md are not
      entries.
    * Catalog entry: "### <ID> · <Title>" with "Priority: **<P>**" on the
      next line, P in MVP, P1, P2, Later, Out of scope.
    * Spec requirement: "### R-<...>-<n> · <Title>" with
      "Priority: **<P>** · Covers: <ID>, <ID>, ..." (or "trove-only") on
      the next line.
    * A catalog feature is "specified" when at least one requirement lists
      its ID in Covers, otherwise "open". Implementation is not measured:
      nothing is implemented, so every implemented count is 0.
"""

import argparse
import collections
import pathlib
import re
import sys
import textwrap

PRIORITIES = ("MVP", "P1", "P2", "Later", "Out of scope")
IN_SCOPE = ("MVP", "P1", "P2", "Later")
TROVE_ONLY = "trove-only"
SPECIFIED = "specified"
OPEN = "open"
DETAILS_THRESHOLD = 15  # tables with more rows than this go into <details>

_ID = r"[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]+"
_P = r"(MVP|P1|P2|Later|Out of scope)"
CAT_HEADING = re.compile(rf"^### ({_ID}) · (\S.*?)\s*$")
CAT_LOOKALIKE = re.compile(r"^### +[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]")
CAT_PRIORITY = re.compile(rf"^Priority: \*\*{_P}\*\*\s*$")
REQ_HEADING = re.compile(rf"^### (R-{_ID}) · (\S.*?)\s*$")
REQ_LOOKALIKE = re.compile(r"^### +R-")
REQ_PRIORITY = re.compile(rf"^Priority: \*\*{_P}\*\* · Covers: (\S.*?)\s*$")
CATALOG_ID = re.compile(rf"^{_ID}$")
FENCE = re.compile(r"^\s*(`{3,}|~{3,})(.*)$")
H1 = re.compile(r"^# (\S.*?)\s*$")
MD_LINK = re.compile(r"\]\(([^)#\s]+\.md)(?:#[^)]*)?\)")
MARKER = "<!-- coverage-gen:{kind} {name} -->"

# Readiness by audience: an audience is a list of catalog pages; its numbers
# count the MVP and the P1 entries of those pages. Page-level on purpose: the
# catalog carries no per-feature audience tags.
DEV_SHARED_PAGES = [
    "formats/README.md",
    "repositories.md",
    "remote-repositories.md",
    "artifacts.md",
    "search-and-query.md",
    "builds-and-ci.md",
    "identity-and-access.md",
    "authentication-providers.md",
    "access-tokens.md",
    "oidc-ci-integration.md",
    "ui-inventory.md",
]
AUDIENCES = [
    (
        "npm-only team",
        "publishes and installs npm packages from workstations and GitLab CI",
        ["formats/npm.md"] + DEV_SHARED_PAGES,
    ),
    (
        ".NET team",
        "publishes and restores NuGet packages from workstations and GitLab CI",
        ["formats/nuget.md"] + DEV_SHARED_PAGES,
    ),
    (
        "platform/ops team",
        "runs trove on Kubernetes and administers it",
        [
            "ha-and-deployment.md",
            "operations-and-monitoring.md",
            "storage-and-blobs.md",
            "retention-and-cleanup.md",
            "repositories.md",
            "remote-repositories.md",
            "identity-and-access.md",
            "authentication-providers.md",
            "access-tokens.md",
            "oidc-ci-integration.md",
            "api-admin-operations.md",
            "events-and-extensibility.md",
            "ui-inventory.md",
        ],
    ),
]
AUDIENCE_PRIORITIES = ("MVP", "P1")

Feature = collections.namedtuple("Feature", "id title priority page line")
Requirement = collections.namedtuple("Requirement", "id title priority covers page line")
Page = collections.namedtuple("Page", "rel title features")


class DataError(Exception):
    pass


# --------------------------------------------------------------------------
# Parsing


def visible_lines(path, rel, fenced_headings):
    """Return (lines, indexes outside fenced code blocks)."""
    lines = path.read_text(encoding="utf-8").split("\n")
    visible = []
    fence = None  # (char, length, line number)
    for idx, line in enumerate(lines):
        m = FENCE.match(line)
        if fence is None:
            if m and not (m.group(1)[0] == "`" and "`" in m.group(2)):
                fence = (m.group(1)[0], len(m.group(1)), idx + 1)
                continue
            visible.append(idx)
        else:
            if (
                m
                and m.group(1)[0] == fence[0]
                and len(m.group(1)) >= fence[1]
                and m.group(2).strip() == ""
            ):
                fence = None
                continue
            if line.startswith("### "):
                fenced_headings.append(f"{rel}:{idx + 1}: {line}")
    if fence is not None:
        raise DataError(f"{rel}:{fence[2]}: code fence opened here is never closed")
    return lines, visible


def parse_catalog(features_dir, docs_dir, errors, fenced_headings):
    pages = {}
    for path in sorted(features_dir.rglob("*.md")):
        rel = path.relative_to(features_dir).as_posix()
        lines, visible = visible_lines(path, path.relative_to(docs_dir).as_posix(), fenced_headings)
        title = None
        feats = []
        for idx in visible:
            line = lines[idx]
            if title is None:
                h1 = H1.match(line)
                if h1:
                    title = h1.group(1)
            if not line.startswith("### "):
                continue
            where = f"features/{rel}:{idx + 1}"
            m = CAT_HEADING.match(line)
            if not m:
                if CAT_LOOKALIKE.match(line) or " · " in line:
                    errors.append(f"{where}: entry heading not in the form '### <ID> · <Title>': {line}")
                continue
            nxt = lines[idx + 1] if idx + 1 < len(lines) else ""
            p = CAT_PRIORITY.match(nxt)
            if not p:
                errors.append(f"{where}: {m.group(1)} has no 'Priority: **<P>**' line directly below")
                continue
            feats.append(Feature(m.group(1), m.group(2), p.group(1), rel, idx + 1))
        if feats:
            if title is None:
                errors.append(f"features/{rel}: page has entries but no '# ' title")
                title = rel
            pages[rel] = Page(rel, title, feats)
    return pages


def parse_spec(spec_dir, errors, fenced_headings, docs_dir):
    reqs = []
    for path in sorted(spec_dir.rglob("*.md")):
        rel = path.relative_to(spec_dir).as_posix()
        lines, visible = visible_lines(path, path.relative_to(docs_dir).as_posix(), fenced_headings)
        for idx in visible:
            line = lines[idx]
            if not REQ_LOOKALIKE.match(line):
                continue
            where = f"spec/{rel}:{idx + 1}"
            m = REQ_HEADING.match(line)
            if not m:
                errors.append(f"{where}: requirement heading not in the form '### R-<...>-<n> · <Title>': {line}")
                continue
            nxt = lines[idx + 1] if idx + 1 < len(lines) else ""
            p = REQ_PRIORITY.match(nxt)
            if not p:
                errors.append(f"{where}: {m.group(1)} has no 'Priority: **<P>** · Covers: ...' line directly below")
                continue
            tokens = [t.strip() for t in p.group(2).split(",")]
            covers = []
            for tok in tokens:
                if tok == TROVE_ONLY:
                    continue
                if not CATALOG_ID.match(tok):
                    errors.append(f"{where}: {m.group(1)} Covers token is not a catalog ID: {tok!r}")
                    continue
                if tok in covers:
                    errors.append(f"{where}: {m.group(1)} lists {tok} twice in Covers")
                    continue
                covers.append(tok)
            if TROVE_ONLY in tokens and len(tokens) > 1:
                errors.append(f"{where}: {m.group(1)} mixes '{TROVE_ONLY}' with catalog IDs")
            reqs.append(Requirement(m.group(1), m.group(2), p.group(1), tuple(covers), rel, idx + 1))
    return reqs


def page_order(features_dir, pages):
    """Order of the catalog README's page table; format pages follow the
    order of first mention in formats/README.md; the rest alphabetically."""

    def linked(readme, base):
        order = []
        if not readme.exists():
            return order
        for target in MD_LINK.findall(readme.read_text(encoding="utf-8")):
            try:
                rel = (base / target).resolve().relative_to(features_dir.resolve()).as_posix()
            except ValueError:
                continue
            if rel not in order:
                order.append(rel)
        return order

    formats = [p for p in linked(features_dir / "formats" / "README.md", features_dir / "formats")
               if p.startswith("formats/") and p != "formats/README.md"]
    order = []
    for rel in linked(features_dir / "README.md", features_dir):
        if rel in order:
            continue
        order.append(rel)
        if rel == "formats/README.md":
            order.extend(p for p in formats if p not in order)
    for rel in sorted(pages):
        if rel not in order:
            if rel.startswith("formats/"):
                order.insert(max(i for i, p in enumerate(order) if p.startswith("formats/")) + 1, rel)
            else:
                order.append(rel)
    return [p for p in order if p in pages]


# --------------------------------------------------------------------------
# Formatting helpers


def n(v):
    return f"{v:,}"


def pct(a, b):
    if b == 0:
        return "–"
    v = 100.0 * a / b
    if a == 0:
        return "0%"
    if a == b:
        return "100%"
    return f"{v:.1f}%"


def cell(text):
    return text.replace("|", "\\|")


def slug(text):
    s = re.sub(r"[^\w\- ]", "", text.lower()).replace(" ", "-")
    return s


def prefixes(page):
    seen = []
    for f in page.features:
        p = f.id.rsplit("-", 1)[0]
        if p not in seen:
            seen.append(p)
    return seen


def section_heading(page):
    return f"{page.title} ({', '.join(prefixes(page))})"


def table(header, rows, align=None):
    out = ["| " + " | ".join(header) + " |"]
    out.append("|" + "|".join(align or ["---"] * len(header)) + "|")
    out.extend("| " + " | ".join(r) + " |" for r in rows)
    return out


def maybe_details(summary, lines, nrows):
    if nrows <= DETAILS_THRESHOLD:
        return lines
    return ["<details>", f"<summary>{summary}</summary>", ""] + lines + ["", "</details>"]


# --------------------------------------------------------------------------
# Model


class Model:
    def __init__(self, pages, order, reqs):
        self.pages = [pages[p] for p in order]
        self.features = [f for p in self.pages for f in p.features]
        self.reqs = reqs
        self.covered_by = collections.defaultdict(list)
        for r in reqs:
            for fid in r.covers:
                self.covered_by[fid].append(r)
        self.req_page = {r.id: r.page for r in reqs}

    def status(self, f):
        return SPECIFIED if self.covered_by.get(f.id) else OPEN

    def count(self, feats, priority=None, status=None):
        return sum(
            1
            for f in feats
            if (priority is None or f.priority in (priority if isinstance(priority, tuple) else (priority,)))
            and (status is None or self.status(f) == status)
        )

    def anchor(self, page):
        return slug(section_heading(page))


def build(repo):
    docs = repo / "docs"
    errors = []
    fenced = []
    pages = parse_catalog(docs / "features", docs, errors, fenced)
    reqs = parse_spec(docs / "spec", errors, fenced, docs)

    seen = {}
    for p in pages.values():
        for f in p.features:
            seen.setdefault(f.id, []).append(f"features/{f.page}:{f.line}")
    for fid, where in seen.items():
        if len(where) > 1:
            errors.append(f"duplicate catalog ID {fid}: {', '.join(where)}")
    rseen = {}
    for r in reqs:
        rseen.setdefault(r.id, []).append(f"spec/{r.page}:{r.line}")
    for rid, where in rseen.items():
        if len(where) > 1:
            errors.append(f"duplicate requirement ID {rid}: {', '.join(where)}")
    for r in reqs:
        for fid in r.covers:
            if fid not in seen:
                errors.append(f"spec/{r.page}:{r.line}: {r.id} covers {fid}, which is not in the catalog")
    order = page_order(docs / "features", pages)
    for name, _, apages in AUDIENCES:
        for ap in apages:
            if ap not in pages:
                errors.append(f"audience {name!r} names catalog page {ap!r}, which has no entries")
    if errors:
        raise DataError("\n".join(errors))
    model = Model(pages, order, reqs)
    anchors = [model.anchor(p) for p in model.pages]
    dup = [a for a, c in collections.Counter(anchors).items() if c > 1]
    bad = [a for a in anchors if "--" in a]
    if dup or bad:
        raise DataError(f"section anchors not unique or not host-independent: {dup + bad}")
    return model, fenced


# --------------------------------------------------------------------------
# checklist.md


def render_checklist(m):
    feats = m.features
    spec_n = m.count(feats, status=SPECIFIED)
    open_n = m.count(feats, status=OPEN)
    only = [r for r in m.reqs if not r.covers]
    out = [
        "# Coverage checklist",
        "",
        "<!-- Generated file: do not edit by hand. -->",
        "",
        "Generated from the headings of the [feature catalog](../features/README.md) (`### <ID> · <Title>`",
        "and the `Priority:` line below it) and of the [specification](../spec/README.md)",
        "(`### R-… · …` and the `Priority: … · Covers: …` line below it). Do not edit it by hand: change",
        "the catalog or the spec, then regenerate this file whenever an ID, a title, a priority or a",
        "`Covers:` line changes. The rules and the headline numbers are in",
        "[README.md](README.md#how-the-numbers-are-produced).",
        "",
        "**Status.** Nothing is implemented. *specified*: at least one spec requirement lists the feature",
        "in its `Covers:` line. *open*: no requirement does. For entries with priority *Out of scope*,",
        "open is the intended end state.",
        "",
        *textwrap.wrap(
            f"**Totals.** {n(len(feats))} catalog features on {len(m.pages)} pages: {n(spec_n)} specified, "
            f"{n(open_n)} open. {n(len(m.reqs))} spec requirements: {n(len(m.reqs) - len(only))} cover "
            f"catalog features, {len(only)} cover none (`{TROVE_ONLY}`, "
            "[listed at the end](#requirements-that-cover-no-catalog-feature)).",
            width=100, break_long_words=False, break_on_hyphens=False),
        "",
        "## Pages",
        "",
    ]
    rows = []
    for p in m.pages:
        rows.append([
            f"[{cell(p.title)}](#{m.anchor(p)})",
            ", ".join(f"`{x}`" for x in prefixes(p)),
            n(len(p.features)),
            (f"{m.count(p.features, 'MVP', SPECIFIED)} of {m.count(p.features, 'MVP')}"
             if m.count(p.features, "MVP") else "–"),
            n(m.count(p.features, status=SPECIFIED)),
            n(m.count(p.features, status=OPEN)),
        ])
    lines = table(["Catalog page", "Prefix", "Features", "MVP specified", "Specified", "Open"], rows,
                  ["---", "---", "---:", "---:", "---:", "---:"])
    out += maybe_details(f"{len(m.pages)} catalog pages", lines, len(rows))
    for p in m.pages:
        out += ["", f"## {section_heading(p)}", ""]
        per_prio = " · ".join(
            f"{pr} {m.count(p.features, pr, SPECIFIED)} of {m.count(p.features, pr)}"
            for pr in PRIORITIES
            if m.count(p.features, pr)
        )
        out.append(f"Catalog page: [{p.rel}](../features/{p.rel}) · specified by priority: {per_prio}")
        out.append("")
        rows = []
        for f in p.features:
            reqs = m.covered_by.get(f.id, [])
            rows.append([
                f.id,
                cell(f.title),
                f.priority,
                m.status(f),
                ", ".join(f"[{r.id}](../spec/{r.page})" for r in reqs) or "–",
            ])
        lines = table(["ID", "Feature", "Priority", "Status", "Covered by"], rows)
        s = m.count(p.features, status=SPECIFIED)
        o = m.count(p.features, status=OPEN)
        out += maybe_details(f"{len(rows)} features: {s} specified, {o} open", lines, len(rows))
    out += ["", "## Requirements that cover no catalog feature", ""]
    out.append(f"Requirements whose `Covers:` line says `{TROVE_ONLY}`: trove's own design work that no")
    out.append("catalog feature measures. They count toward the requirement total, not toward any share.")
    out.append("")
    rows = [[r.id, cell(r.title), r.priority, f"[{r.page}](../spec/{r.page})"] for r in only]
    lines = table(["Requirement", "Title", "Priority", "Spec page"], rows)
    out += maybe_details(f"{len(rows)} requirements", lines, len(rows))
    out.append("")
    return "\n".join(out)


# --------------------------------------------------------------------------
# README.md blocks


def block_headline(m):
    feats = m.features
    mvp = [f for f in feats if f.priority == "MVP"]
    p1 = [f for f in feats if f.priority == "P1"]
    scope = [f for f in feats if f.priority in IN_SCOPE]
    only = [r for r in m.reqs if not r.covers]
    rp = collections.Counter(r.priority for r in m.reqs)
    rp_text = ", ".join(f"{rp[p]} {p}" for p in PRIORITIES if rp[p])
    spec_pages = sorted({r.page for r in m.reqs})
    rows = [
        ["**Implemented**", f"**0%** (0 of {n(len(feats))} catalog features)",
         "fact, not computed: the repository holds no code yet"],
        ["**MVP features covered by spec requirements**",
         f"**{pct(m.count(mvp, status=SPECIFIED), len(mvp))}** ({m.count(mvp, status=SPECIFIED)} of {len(mvp)})",
         "measured from `Covers:` lines"],
        ["P1 features covered by spec requirements",
         f"{pct(m.count(p1, status=SPECIFIED), len(p1))} ({m.count(p1, status=SPECIFIED)} of {len(p1)})",
         "measured from `Covers:` lines"],
        ["In-scope features covered (MVP, P1, P2, Later)",
         f"{pct(m.count(scope, status=SPECIFIED), len(scope))} ({n(m.count(scope, status=SPECIFIED))} of {n(len(scope))})",
         "measured from `Covers:` lines"],
        ["Catalog features", f"{n(len(feats))} on {len(m.pages)} pages", "measured from catalog headings"],
        ["Spec requirements", f"{n(len(m.reqs))} on {len(spec_pages)} pages ({rp_text})",
         "measured from spec headings"],
        ["Requirements covering no catalog feature", f"{len(only)} (`{TROVE_ONLY}`)",
         "measured from `Covers:` lines"],
    ]
    return table(["Number", "Value", "Kind"], rows)


def block_priority(m):
    feats = m.features
    rows = []
    for pr in PRIORITIES:
        fs = [f for f in feats if f.priority == pr]
        s = m.count(fs, status=SPECIFIED)
        pulled = sum(1 for f in fs if any(r.priority == "MVP" for r in m.covered_by.get(f.id, [])))
        rows.append([f"**{pr}**", n(len(fs)), n(s), n(len(fs) - s), pct(s, len(fs)), n(pulled), "0"])
    s = m.count(feats, status=SPECIFIED)
    pulled = sum(1 for f in feats if any(r.priority == "MVP" for r in m.covered_by.get(f.id, [])))
    rows.append(["**Total**", n(len(feats)), n(s), n(len(feats) - s), pct(s, len(feats)), n(pulled), "0"])
    return table(
        ["Priority", "Features", "Specified", "Open", "Specified share", "Covered by an MVP requirement", "Implemented"],
        rows, ["---", "---:", "---:", "---:", "---:", "---:", "---:"])


def page_rows(m, pages):
    rows = []
    for p in pages:
        c = [m.count(p.features, pr) for pr in PRIORITIES]
        rows.append([
            f"[{cell(p.title)}](checklist.md#{m.anchor(p)})",
            n(len(p.features)),
            *[n(x) for x in c],
            n(m.count(p.features, status=SPECIFIED)),
            n(m.count(p.features, status=OPEN)),
        ])
    return rows


def block_pages(m):
    hdr = ["Catalog page", "Features", "MVP", "P1", "P2", "Later", "Out of scope", "Specified", "Open"]
    align = ["---"] + ["---:"] * 8
    general = [p for p in m.pages if not p.rel.startswith("formats/") or p.rel == "formats/README.md"]
    formats = [p for p in m.pages if p.rel.startswith("formats/") and p.rel != "formats/README.md"]
    out = table(hdr, page_rows(m, general), align)
    fmt_feats = [f for p in formats for f in p.features]
    out += [
        "",
        f"Per-format pages ({len(formats)} pages, {n(len(fmt_feats))} features, "
        f"{n(m.count(fmt_feats, status=SPECIFIED))} specified):",
        "",
    ]
    out += maybe_details(f"{len(formats)} format pages", table(hdr, page_rows(m, formats), align), len(formats))
    return out


def block_audiences(m):
    by_rel = {p.rel: p for p in m.pages}
    rows = []
    defs = []
    for name, who, apages in AUDIENCES:
        feats = [f for rel in apages for f in by_rel[rel].features]
        mvp = [f for f in feats if f.priority == "MVP"]
        both = [f for f in feats if f.priority in AUDIENCE_PRIORITIES]
        ms, bs = m.count(mvp, status=SPECIFIED), m.count(both, status=SPECIFIED)
        opens = sorted(
            ((m.count([f for f in by_rel[rel].features if f.priority in AUDIENCE_PRIORITIES], status=OPEN),
              by_rel[rel].title) for rel in apages),
            key=lambda x: (-x[0], x[1]))
        top = "; ".join(f"{t} {c}" for c, t in opens[:3] if c) or "none"
        rows.append([
            f"**{name}**",
            f"{ms} of {len(mvp)} specified, {len(mvp) - ms} open ({pct(ms, len(mvp))})",
            f"{bs} of {len(both)} specified, {len(both) - bs} open ({pct(bs, len(both))})",
            "0",
            top,
        ])
        defs.append([f"**{name}**", who, ", ".join(f"[{rel}](../features/{rel})" for rel in apages)])
    out = table(["Audience", "MVP entries", "MVP and P1 entries", "Implemented", "Most open entries (MVP and P1)"],
                rows, ["---", "---", "---", "---:", "---"])
    counted = {rel for _, _, ap in AUDIENCES for rel in ap}
    uncounted = [p for p in m.pages if p.rel not in counted]
    out += ["", "Pages counted per audience:", ""]
    out += table(["Audience", "Who", "Catalog pages counted"], defs)
    un_mvp_p1 = sum(m.count(p.features, AUDIENCE_PRIORITIES) for p in uncounted)
    out += [
        "",
        f"Pages counted for no audience ({len(uncounted)}, {un_mvp_p1} MVP and P1 entries): "
        + ", ".join(f"[{p.title}](checklist.md#{m.anchor(p)})" for p in uncounted if m.count(p.features, AUDIENCE_PRIORITIES))
        + f"; plus {sum(1 for p in uncounted if not m.count(p.features, AUDIENCE_PRIORITIES))} pages"
        " without MVP or P1 entries.",
    ]
    return out


BLOCKS = {
    "headline": block_headline,
    "priority": block_priority,
    "pages": block_pages,
    "audiences": block_audiences,
}


def render_readme(m, text):
    for name, fn in BLOCKS.items():
        begin = MARKER.format(kind="begin", name=name)
        end = MARKER.format(kind="end", name=name)
        if text.count(begin) != 1 or text.count(end) != 1 or text.index(begin) > text.index(end):
            raise DataError(f"README.md: marker pair for block {name!r} missing or malformed")
        head, rest = text.split(begin, 1)
        _, tail = rest.split(end, 1)
        text = head + begin + "\n" + "\n".join(fn(m)) + "\n" + end + tail
    return text


# --------------------------------------------------------------------------


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--repo", required=True, type=pathlib.Path)
    ap.add_argument("--check", action="store_true", help="compare only, write nothing")
    args = ap.parse_args()
    repo = args.repo.resolve()
    try:
        model, fenced = build(repo)
        checklist = render_checklist(model)
        readme_path = repo / "docs" / "coverage" / "README.md"
        readme_old = readme_path.read_text(encoding="utf-8") if readme_path.exists() else None
        if readme_old is None:
            raise DataError("docs/coverage/README.md does not exist; it holds the prose around the generated blocks")
        readme = render_readme(model, readme_old)
    except DataError as e:
        print(f"coverage_gen: error\n{e}", file=sys.stderr)
        return 1

    # Self-check: nothing dropped.
    dropped = [f.id for f in model.features if f"\n| {f.id} | " not in checklist]
    dropped += [r.id for r in model.reqs
                if f"[{r.id}](" not in checklist and f"\n| {r.id} | " not in checklist]
    if dropped:
        print(f"coverage_gen: error\nnot rendered: {', '.join(dropped)}", file=sys.stderr)
        return 1

    feats = model.features
    pc = collections.Counter(f.priority for f in feats)
    rc = collections.Counter(r.priority for r in model.reqs)
    only = sum(1 for r in model.reqs if not r.covers)
    refs = sum(len(r.covers) for r in model.reqs)
    print(f"catalog: {len(model.pages)} pages, {len(feats)} features ("
          + ", ".join(f"{p} {pc[p]}" for p in PRIORITIES) + ")")
    print(f"spec: {len({r.page for r in model.reqs})} pages, {len(model.reqs)} requirements ("
          + ", ".join(f"{p} {rc[p]}" for p in PRIORITIES if rc[p])
          + f"); {len(model.reqs) - only} cover catalog features, {only} {TROVE_ONLY}; {refs} Covers references")
    print(f"coverage: {model.count(feats, status=SPECIFIED)} specified, {model.count(feats, status=OPEN)} open; "
          + "; ".join(f"{p} {model.count(feats, p, SPECIFIED)}/{pc[p]}" for p in PRIORITIES))
    print(f"headings skipped inside code fences: {len(fenced)}")
    for line in fenced:
        print(f"  {line}")

    targets = [(repo / "docs" / "coverage" / "checklist.md", checklist), (readme_path, readme)]
    if args.check:
        stale = [p for p, content in targets if not p.exists() or p.read_text(encoding="utf-8") != content]
        for p in stale:
            print(f"stale: {p.relative_to(repo)}", file=sys.stderr)
        return 2 if stale else 0
    for p, content in targets:
        p.write_text(content, encoding="utf-8")
        print(f"wrote {p.relative_to(repo)} ({len(content.encode('utf-8')):,} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
