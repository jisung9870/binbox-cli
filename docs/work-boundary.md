# Work Boundary

This repository is the only writable project for the `binbox-next` effort.

## Writable path

- `/home/ubuntu/projects/binbox-next`

## Read-only references

- `/home/ubuntu/setup/binbox`
- `/home/ubuntu/setup/nvim`
- `/home/ubuntu/setup/workbench`
- `/home/ubuntu/setup`

No generated files, tests, Git operations that mutate state, or source edits may be
performed in the read-only references. Their initial branches were observed as:

- `binbox`: `main...origin/main`
- `nvim`: `agent/update-plugin-lock...origin/agent/update-plugin-lock`
- `workbench`: `orca/work...origin/orca/work`
- `setup`: `orca/work...origin/orca/work`

The target path did not exist when work began on 2026-08-10 (Asia/Seoul). It was
created as a new Git repository with initial branch `main`.
