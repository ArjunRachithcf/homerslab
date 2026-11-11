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
| `parked` | Failed twice, written up |
| `bug` | Found while working; worked before slices |
| `digest` | The daily summary issue |
| `integration` | The standing PR from the integration branch to `main` |

## Branches

- **`main`** takes PRs that don't touch the security core and need nothing that is only on `integration`. The loop
  merges them.
- **`integration`** takes everything else:
  - PRs that touch the security core (see `CLAUDE.md`);
  - PRs whose blocking tickets were merged only into `integration`.

  The loop merges into it after the same CI and review. One standing PR, `integration` → `main`, labelled
  `security-core` and `integration`, lists every ticket inside it. The maintainer squash-merges it into `main` as one
  commit.
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
10. **Merge.**
    1. Squash-merge into the PR's base and delete the branch.
    2. Remove `in-progress`.
    3. For `integration`: close the ticket by hand, with a comment naming the PR, since `Closes` fires only on `main`.
       Then add the ticket to the standing PR's list (open that PR if there is none) and to today's digest.
11. **Failures.** A PR that fails CI or review twice for the same ticket gets parked:
    - close the PR as a draft and keep its branch;
    - comment what was tried, what failed and what's needed;
    - label the ticket `parked` and remove `in-progress`.
12. **Bugs found along the way** become new issues labelled `bug`, each with a repro, the expected result and blocking
    links. They aren't fixed inside an unrelated PR.

## The daily digest

Once a day, update one issue labelled `digest`, titled `Digest <date>`. It lists:
- **merged**: PRs and closed tickets;
- **waiting for you**: the integration PR and the tickets in it;
- **questions**: `needs-decision` tickets, each linked;
- **parked**: tickets with their one-line reason;
- **next**: the frontier.

The digest is the maintainer's one place to look.

## Stopping

The loop runs while the frontier has work. When only `needs-decision` and `parked` items are left, it updates the
digest and sleeps until the maintainer answers.
