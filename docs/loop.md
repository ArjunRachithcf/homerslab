# The loop

The loop works HomersLab's GitHub issues from the map of tickets to merged PRs, and never waits on the maintainer.
It asks him only for decisions and for one review of the security core: security-core work stacks on the
**integration branch**, which reaches `main` as a single squash that he merges. Each **iteration** runs these steps in order. An iteration is done
when its ticket is merged, waiting for the maintainer, or parked, and the tracker says which.

## Labels

| Label | Meaning |
|---|---|
| `in-progress` | Claimed by an iteration |
| `security-core` | Waiting for the maintainer to merge (see `CLAUDE.md`) |
| `needs-decision` | The loop asked the maintainer something |
| `parked` | Failed three times (or twice without a clear fix), written up |
| `bug` | Found while working; worked before slices |
| `digest` | The daily summary issue |
| `idea` | From standing work: a proposal with a recommendation |
| `integration` | The standing PR from the integration branch to `main` |

## Branches

- **`main`** takes PRs that don't touch the security core and need nothing that is only on `integration`. The loop
  merges them.
- **`integration`** takes everything else:
  - PRs that touch the security core (see `CLAUDE.md`);
  - PRs whose blocking tickets were merged only into `integration`.

  The loop merges into it after the same CI and review. One standing PR, `integration` → `main`, labelled
  `security-core` and `integration`, lists every ticket inside it.
- **When `integration` is ready** (every PR stacked for it is merged into it), the loop reviews the standing PR along
  several axes at once: security, correctness, the spec and ADRs, tests and isolation, the design system and
  accessibility, and supply chain and CI. It runs up to three rounds, incrementally (see step 9). When a round passes,
  the loop squash-merges it into `main` (the maintainer, 2026-11-11). Three failed rounds park it for the maintainer.
- **Up to three iterations run at once** on independent tickets, each in its own worktree.

## Steps

1. **Sync.**
   - Update `main`.
   - Merge `main` into `integration` whenever `main` has moved.
   - If the maintainer has squash-merged the integration PR, recreate `integration` from `main` and open a fresh
     standing PR when there is new work for it.
2. **Find the frontier.** Open issues labelled `slice` or `bug` with:
   - every blocking issue closed;
   - none of `in-progress`, `parked` or `needs-decision`.

   Pick the first `bug`. If there is none, take the lowest-numbered slice.
3. **Claim it.** Add `in-progress`. Choose the base (`main` or `integration`, as in Branches), and create a worktree on
   branch `t/<number>-<short-name>` from it.
4. **Read.** Read the ticket, the spec sections and ADRs it links, the glossary words it uses, and the prototype screens
   for UI work.
   - **If the spec is silent or contradicts itself, or the ticket needs a choice**: post the question as a grilling
     round. Number each question, give your recommendation, and word it so that "yes" accepts the recommendation.
     Label the ticket `needs-decision`, remove `in-progress`, and start this step again with the next ticket.
5. **Build it test-first** at the ticket's test seam (the `tdd` skill): red, green, refactor, one acceptance criterion
   at a time. Every checkbox in the ticket must be satisfied by a test that went red first, or by a check the ticket
   names. Keep to `CLAUDE.md`'s conventions, test isolation included.
6. **Review your own diff** with the `code-review` skill. Fix what it finds, then run the full test suite locally.
7. **Open the PR** against its base, in the `pr` skill's shape. The body says `Closes #<number>` and links the spec
   sections and the decisions (ADR numbers, traceability rows) it implements. Label it `security-core` if it touches the
   security core.
8. **Wait for CI.** It must be green.
9. **Get an independent review.** Start a fresh subagent that gets only the PR's diff, the ticket, the linked spec and
   ADRs, and the glossary. It approves, or lists problems. Fix them and go back to step 8.
   - **Later rounds are incremental** (the maintainer, 2026-11-11), so a PR can actually pass:
     - they check that each earlier finding is fixed;
     - they review only what changed since the last round;
     - a new problem in code the round didn't change is filed as a follow-up issue, not a blocker, unless it is a
       high-severity security problem.
10. **Merge.**
    1. Squash-merge into the PR's base and delete the branch.
    2. Remove `in-progress`.
    3. For `integration`: close the ticket by hand, with a comment naming the PR, since `Closes` fires only on `main`.
       Then add the ticket to the standing PR's list (open that PR if there is none) and to today's digest.
11. **Failures.**
    - **The second failure** (CI or review) for the same ticket: when the review wrote down a clear fix, the loop
      resumes the ticket once with that fix, and says so in the digest. Otherwise, it parks the ticket as below.
    - **The third failure** parks the ticket:
      - close the PR as a draft and keep its branch;
      - comment what was tried, what failed and what's needed;
      - label the ticket `parked`, remove `in-progress`, and ask the maintainer.
12. **Bugs found along the way** become new issues labelled `bug`, each with a repro, the expected result and blocking
    links. They aren't fixed inside an unrelated PR.

## The daily digest

Once a day, update one issue labelled `digest`, titled `Digest <date>`. It lists:
- **merged**: PRs and closed tickets;
- **waiting for you**: decisions, and anything parked;
- **questions**: `needs-decision` tickets, each linked;
- **parked**: tickets with their one-line reason;
- **next**: the frontier.

The digest is the maintainer's one place to look.

## Standing work

The loop never idles. Whenever a slot is free (fewer than three tickets running, or nothing on the frontier), it runs
the next item of this rotation in a fresh subagent:

1. **Bug hunt.** Read and run what's merged on `main` and `integration`. Try the edge cases the tests skip. Write a
   failing test for each bug found.
2. **Security review.** Use the `security-review` skill and a threat model of what's merged: the first mile, sign-in,
   the edge, secrets, the firewall, the supply chain, CI. The reviewer attacks as a stranger on the LAN, a stranger on
   the internet, a family member, a kid, a malicious app and a compromised catalog.
3. **Adversarial ideation.** Red-team the spec and the ADRs ahead of the code: how each planned flow fails, is abused,
   or locks the household out. Look for the edge case a family hits in its first year.
4. **Design review.** Compare the built surfaces with `design/` and `prototypes/`: fidelity, accessibility, copy
   against the glossary, and states that are missing (empty, error, slow, offline).
5. **Spec review.** Find contradictions, gaps, and tickets whose criteria no longer match the spec or the code.

Each round ends in tracker items:
- **Bugs** become `bug` issues with a failing test or repro. They're worked first.
- **Design and spec problems** become `idea` issues with a recommendation, or `needs-decision` questions for choices
  that are the maintainer's.
- **New work** becomes tickets with blocking links.

Unreported security problems in other projects' software stay out of the tracker; they go to the maintainer
privately.

## Stopping

The loop doesn't stop. With only `needs-decision` and `parked` items left, it keeps the digest current and runs
standing work.
