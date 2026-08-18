---
name: code-comment-cleanup
description: Audit the comments a PR/branch newly introduces and flag which ones are genuinely worth keeping vs. redundant or a future liability. Use when the user asks to "clean up comments", "review comments in this PR", "check the comments we added", or before merging a branch that added a lot of explanatory comments (agent-generated PRs especially tend to over-comment).
---

# Code comment cleanup

Goal: look only at comments a diff *adds*, and sort each into keep / trim / cut — don't
re-litigate comments that already existed before this PR.

## Rationale

Every comment has a permanent cost: it makes the code slower to read forever, it has to be
kept in sync by hand forever, and there is no mechanism that prevents it from going stale —
unlike the code itself, nothing forces a comment to still be true after the tenth unrelated
edit near it. So the default posture per verdict isn't "does this comment help" (almost any
comment can be argued to help a little) — it's **"is a comment actually the cheapest fix for
whatever confusion this is addressing, or is there a code-level fix (a rename, a
restructure, deleting the confusing alternative) that would make the comment unnecessary and
can't go stale the way prose can?"** Reach for KEEP only when the answer is "a comment really
is the cheapest fix available right now," not just "this comment is accurate and mildly
helpful."

Every test below hinges on one variable that has to be set correctly before the test means
anything: **who actually reads this specific comment, and through what surface, and what else
do they already have access to?** The same words can be a CUT for one reader and a KEEP for
another — a sentence restating an ADR is dead weight for an engineer who has the ADR one click
away, but load-bearing for an operator reading `kubectl explain` who will never see that file.
Get the reader wrong and every test downstream gives the wrong answer. So before running
CUT/TRIM/KEEP on a comment, place its reader:
- **Fellow engineer reading source** (the default case) — has repo access: the ADR, git
  history, other files. "Point at the doc instead of restating it" and "a rename would fix
  this instead" both assume this reader.
- **Consumer of generated documentation** (CRD schema descriptions via `kubectl explain`,
  godoc for an exported package, any doc-comment-derived reference) — usually does *not* have
  repo access. Design docs, commit history, and renames of internal-only symbols are all
  invisible to them; only the words in the comment itself reach them. Judge each clause by
  whether *this* reader needs it, not by whether it duplicates something only a repo-reader
  would already have seen.

The CRD/godoc case in KEEP below is this second reader, not a special carve-out from the
general rules — same tests, correctly-set audience.

## Step 0 — scope the diff

Figure out the base and head:
- PR number/URL given: `gh pr diff <n> --repo <owner>/<repo>`, or `gh pr view <n> --json baseRefName,headRefName` then `git diff <base>...<head>`.
- Branch name given, or nothing given (default to current branch): `git diff main...<branch>` (three-dot — changes on the branch only, not what changed on main since it diverged).

Restrict to source files. Exclude generated/vendored content up front — comments there
aren't something a human wrote and aren't worth judging:
- `zz_generated*.go`, anything under `**/fakes/` or `**/*fakes/`
- `config/crd/**`, `config/rbac/**` (marker-generated YAML)
- `vendor/`, lockfiles

## Step 1 — extract added comments

```
git diff <base>...<head> -- '*.go' ':(exclude)**/zz_generated*' ':(exclude)**/fakes/**'
```

Pull only **added** lines (`+`, not `+++`) that are comments (`//`, `/* */`) — either a
whole new comment line, or a doc comment attached to a newly added declaration. A raw `+`/`-`
diff can't tell a genuinely new comment from one that only moved because code above it shifted
— use `git diff --color-moved=default <base>...<head>` (add `--color-moved-ws=allow-indentation-change`
to also catch pure reindentation) so moved blocks render distinctly from real adds; only lines
still shown as plain additions, not colored as moved, count as newly introduced.

The taxonomy below is language-agnostic, not Go-specific — apply the same CUT/TRIM/KEEP tests
to Makefile/shell `#` and YAML `#` comments too. E.g. a Makefile comment walking through why a
stale macOS binary gets reused in a Linux container (`go install` has no GOOS/GOARCH override →
bind-mounted repo root → stale binary → "Exec format error") is a workaround-for-external-
limitation KEEP case, but per the workaround test below, trim it to the one clause that
matters rather than the full causal chain.

## Step 2 — classify each comment

For every extracted comment, apply this test, in order:

**CUT** — delete it. The comment restates what the name/signature/adjacent code already
says; it explains WHAT, not WHY. Test: read just the code, no comment — is a future reader
actually missing something that matters? If no, cut it.
- Textbook case: `// resolveMaxUnavailable applies Spec.MaxUnavailable (defaulting to
  defaultMaxUnavailable)` on `func resolveMaxUnavailable(...)` — the function name and its one
  line of body already say this.
- Same test for doc comments on trivial getters/one-liners: if it just narrates the name back
  ("GetName gets the name"), it adds nothing even compressed to one line — cut, don't shrink.
- Before finalizing a CUT on an exported symbol's doc comment specifically: check whether the
  target repo's lint config actually requires one (a `revive` exported-comment rule, `godot`,
  or `stylecheck`-class `ST1000`/`ST1020`/`ST1021` checks) — cutting one that's mandatory hands
  back a fix that fails `make lint` immediately. Verified 2026-08-16: this repo's
  `.golangci.yml` enables `staticcheck` with no such check configured and neither `revive` nor
  `godot` at all, so no doc comment here is lint-mandatory — but that's this repo's config
  today, not a property of Go in general; re-check if this skill is ever pointed at a different
  repo or this one's lint config changes.
- **Repeated-copy case**: if the same fact is already stated elsewhere in this diff, and the
  later copy adds zero fact the earlier one(s) don't already cover, that's a full CUT of the
  later copy — not a TRIM. TRIM is for compressing one long comment down to its essential
  clause; it isn't for deciding which of several duplicate comments survives. If nothing
  unique survives in a copy, delete the whole thing. "Elsewhere in this diff" is not limited to
  the same symbol's decl-vs-use-sites — a comment on one declaration can restate a comment on
  a completely different one. Example: a constant's comment says the OS package manager keeps
  a given path up to date on upgrades; a separate function's comment, for a function that
  checks whether the binary is running from that path, restates the identical fact ("binaries
  elsewhere never get those upgrades") instead of saying anything new about what the function
  itself does. The second comment isn't safe just because it's attached to a different symbol —
  whichever one the reader hits first already tells them this.

**TRIM** — keep the point, cut the rest. The comment has a real "why" buried in it, but:
- it's a multi-clause reasoning chain ("X, so Y, which means Z, therefore...") — compress to
  the one clause that actually matters; the rest belongs in the PR description or commit
  message, not the code.
  - **Before finalizing, re-run the CUT test on the clause you're keeping, alone** — not on the
    original comment. "This comment has several clauses" is what got you into TRIM in the first
    place; it is not itself evidence that any one of those clauses is load-bearing. Ask again:
    read just the code, no comment — is a reader missing something *this specific retained
    clause* says? If the surviving clause is itself derivable from the adjacent code, the real
    verdict was CUT all along and TRIM just compressed a restatement instead of removing it.
  - **Watch for a clause that's rationale-*shaped* without being a rationale.** A clause
    connected with "so"/"because"/"which means" reads like a why, but the causal connector is
    grammar, not content — check whether the thing after it is derivable from code sitting right
    next to it. Example: `// pickAndAssign excludes hosts already counted unavailable from its
    candidates, so a picked host is never double-counted against the availability budget` looks
    like a kept rationale, but `maxUnavailable-len(summary.unavailable)` two lines below already
    *is* "budget minus what's already spent, so nothing's double-counted" — the "so" clause adds
    a sentence restating arithmetic the next line performs, not a fact the code doesn't state.
    Correct verdict there was CUT, not TRIM — caught only by applying the retained-clause test
    above instead of stopping once a plausible-looking "why" clause was identified.
- it reproduces content that already lives in a design doc (ADR/RFC) at length, instead of
  pointing at it, **and the reader of this comment has actual access to that doc.** This is a
  drift liability: the doc and the comment are now two sources of truth for the same design
  decision, and only one of them gets updated when the design changes. Replace the restated
  narrative with a short pointer (`// see docs/proposals/foo.md §2.3`) plus, at most, the one
  clause that's load-bearing for *this specific line of code*.
  - This does **not** apply when the reader is a generated-documentation consumer rather than
    a repo-reading engineer (see the CRD/godoc case under KEEP below) — that reader can't
    reach the ADR from where they're reading, so "point at it instead" fails to serve them;
    judge each clause on its own rather than defaulting to "compress and point elsewhere."
  - **Stricter default when the ADR/RFC is introduced in this same commit/diff, not
    pre-existing:** drop the "one load-bearing clause" allowance and CUT the narrative
    entirely, keeping at most a bare pointer with no restated content. A doc and a comment
    written in the same sitting by the same author about the same decision have had no time to
    diverge and no independent reason to know something the other doesn't — unlike a comment
    restating a long-pre-existing ADR, which might have picked up a genuinely local detail the
    doc never had reason to capture. The bare pointer earns its keep only as cheap navigation
    for someone who later opens just this file cold; it isn't teaching the PR's own reviewer
    anything, since they already see both files in the same diff.

**KEEP** — leave it exactly as-is. The comment documents something that isn't visible in the
code itself, there's no code-level fix (rename/restructure) that would make it unnecessary,
and its absence would cause a real future mistake:
- a non-obvious invariant ("this is never reached in production, only via a fake in tests")
- **a workaround for a specific external bug/limitation** — but only when *both* hold:
  1. The cause is genuinely external (a library/OS/runtime bug or limitation — not an internal
     design tradeoff, that's the rejected-alternative case below) and not inferable from the
     surrounding code. If a reader could work out the reason from local context alone, there's
     no confusion to protect against.
  2. The bug is still live — removing the workaround today would cause a real, currently
     reproducible regression, not one already fixed upstream or too rare to matter. Pin it to
     something falsifiable when possible (a version, an issue link, a condition): this category
     carries the highest staleness risk of anything in KEEP, since it describes a fact about the
     external world that can change without anyone re-checking, unlike the rest of this list,
     which describes something intrinsically true about this codebase's own design.

  Cut it despite superficially matching this bullet if the "bug" is common ecosystem knowledge
  nobody would question, or if the workaround code already reads as ordinary and nothing about
  it looks suspicious enough to invite "cleanup" in the first place — there's no real risk to
  guard against if no one would ever mistake it for cruft.
- the rationale for a lint/security suppression (`#nosec`, `//nolint`) — these are only ever
  correct *with* an explanation of why the suppression is safe; never cut these, and don't
  trim below a real justification
- **a deliberately rejected alternative** ("not X, because Y") — but only when *both* hold:
  1. X is something a competent contributor would plausibly reach for, not a strawman nobody
     would try. ("Add a `Watches()` + mapping function for faster reconciliation" is a standard
     controller-runtime instinct — plausible. "Rewrite this in a different language" is not.)
  2. Re-deriving the rejection live would cost more than the sentence — real time spent
     designing/reviewing the alternative, or worse, actually shipping it and causing a
     regression, before landing back at the same conclusion.

  Why this can't just be left to the commit message: a commit message documents why the code
  *came to look this way*, for someone doing archaeology after they're already confused or
  reviewing history. A code comment intercepts *before* the mistake, at the exact moment
  someone is about to write the tempting alternative — and nobody reads `git log` before
  writing code, only after something already looks wrong (and post-squash or post-refactor,
  the fine-grained rationale often doesn't survive blame intact anyway). That's the actual
  gap a rejected-alternative comment fills that history can't.
- a case where two similarly-named things could be confused (which function actually owns
  this behavior, and why the other one doesn't) — **but first ask whether a rename would
  eliminate the confusion instead.** If the two things are confusable only because of a naming
  collision (not because the underlying behavior is genuinely subtle), a rename is the more
  durable fix — it costs nothing ongoing and can't go stale the way the comment can. If the
  confusable thing predates this diff and renaming it is out of scope for this pass, KEEP the
  comment as a stopgap but flag it as superseded-by-a-rename, not a permanent keep.

- **generated-documentation audience** — doc comments on exported types/fields that surface
  through tooling (kubebuilder markers feeding CRD schema descriptions / `kubectl explain`,
  godoc for an exported package API) are read by whoever consumes that surface, often an
  operator who will never open the repo, let alone an ADR. Applying "point at the design doc
  instead" here fails outright — that reader can't follow a repo-relative path from `kubectl
  explain` output — so judge clause by clause instead: does *this* fact answer a question the
  field's actual reader would have while filling it in (format, safety properties, a gotcha
  they'd otherwise hit silently, e.g. "this field never triggers a downgrade")? Keep those
  clauses. Cut clauses that are dev-facing trivia that leaked into a user-facing doc (e.g.
  "this uses the same internal mechanism as some other unrelated feature" — true, but not
  something the field's own reader needs). Still cut pure filler ("Foo is a Foo"). This isn't a
  different rule from the rest of KEEP — it's the same "would the reader be missing something
  that matters" test, just run against the generated-doc reader instead of the default
  repo-reading one.

**Mandatory closing pass — do not skip this.** Every test above compares one comment against
its own adjacent code; none of them compare it against *other comments*. That means a comment
can pass every test in this section individually and still duplicate a fact some other
surviving comment already states elsewhere in the diff — the per-comment loop has no built-in
way to catch that, so it has to be a separate, deliberate step. Once every comment has an
individual verdict, take the ones still standing as KEEP or TRIM and compare each against every
other surviving comment — not only ones on the same symbol, but comments on any declaration it
references or is referenced by. Any fact stated by more than one survivor is the repeated-copy
CUT case above: keep whichever instance is clearest or most locally relevant, cut the rest.

## Step 3 — report

Group findings by verdict. For each: file:line, the comment (or its first line if long), and
a one-line reason tying it to the test above it failed/passed. Flag drift-liability trims
separately even though they live under TRIM, since that's the reason a reviewer would want to
know about, not just "too long."

If the diff added so many comments that a full one-by-one pass is impractical, triage rather
than sample randomly: exported/public-surface doc comments first (wrong audience-targeting
there is the costliest mistake — see the generated-documentation case above), then
drift-liability TRIMs (the ones actively growing stale by construction), then the rest.

## Step 4 — apply (only if asked)

Cutting/trimming comments still touches someone's finished work — confirm before editing.
Default to confirming the Step 3 report as a whole (batch-approve the verdicts, not a
per-comment back-and-forth); only drop to reviewing one-by-one if the user asks for that or
pushes back on a specific verdict. Only ever remove or shorten CUT/TRIM comments; never touch a
KEEP verdict, and never touch a comment that predates this diff.

If the branch's history is going to be reshaped afterward (multi-commit PR, or the
`git-history-cleanup` skill is coming next) — commit small, not one bulk commit. For each
CUT/TRIM edit, `git blame` the line against the branch to find which commit introduced it,
group edits by that origin commit, and make one small commit per origin commit (touching only
that commit's own lines). A single commit isn't wrong, but it turns a follow-on reshape into
the hard case that skill documents — distributing one cross-cutting commit's diff back across
several earlier commits via edit-stops and re-authoring final state. Pre-grouped small commits
skip that entirely: each one is already scoped to a single target, so the reshape is a plain
reorder + `fixup`, no edit-stops needed. This is safe to do even under time pressure, since
comment-only edits can't reference a symbol from a later commit and can't fail to compile —
none of the risk that technique exists to manage applies here.

Where a comment's added lines aren't cleanly attributable to one commit (e.g. it restates
something two different commits each added a version of, like a field comment repeated near
three separate call sites introduced at different times), split the edit itself along the same
lines rather than lumping it into whichever commit happens to be easiest.
