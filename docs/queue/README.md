# Backlog items

Each file here is one open item, and its `rank` key sets its priority. To read
them in order, use the [rendered backlog](https://kube-headroom.dev/docs/queue/)
or run:

```sh
python3 scripts/queue.py render --all
```

A directory listing sorts by ID, which is neither priority nor age, so don't
read this tree top to bottom.

## Filing an item

1. Claim an ID: `bash scripts/alloc-queue-id.sh 'The item title'`. Never pick a
   number by hand; `make backlog-lint` fails an ID with no claim on the remote.
2. Compute a rank: `python3 scripts/queue.py rank --head`, or `--tail`,
   `--after`, `--before`. Never type one by hand.
3. Write `docs/queue/QN.md`, using any existing item as the template, and run
   `make backlog-lint`.

Title at most 72 characters; the body has no limit. A blocked item's note opens
with `Blocked by [Q3](Q3.md)`. A deferred item's note opens with the `**Demand:**`,
`**Event:**` or `**Decision:**` that revives it. An item ending in an unanswered
question carries the `open-question` label and names its route with `**Settles:**`,
`**Measure:**` or `**Ask:**`.

Labels: `policy` `controller` `webhook` `observability` `tests` `docs` `infra`
`security` `spike`, plus the process labels `open-question`, `accruing` and
`perishable`. Sizes: S is one session or PR, M is two or three sessions, and L
needs a plan doc under `docs/plan/`.

## Changing and completing items

Commit backlog edits in their own `docs(status):` commits, separate from code;
the pre-commit hook rejects a commit that mixes them. Delete an item's file when
its work ships, in a commit whose subject says why (`complete Q3`, `prune Q3`,
`merge Q3 into Q4`). Git history is the archive.

Never commit `docs/queue/index.md` or another generated index. The website
builds that page with `scripts/render-backlog-page.py`, and a tracked index is
one file that every completing session would have to edit.
