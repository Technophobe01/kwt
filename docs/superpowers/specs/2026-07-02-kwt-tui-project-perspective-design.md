# kwt TUI Project Perspective and Footer Hints Design

## Goal

Make the dashboard usable as a cross-project workspace manager without leaving the TUI to change directories, and make the bottom hint bar compact enough to fit like kata and roborev.

## Design

The lowercase `p` behavior remains a project-name filter. Add uppercase `P` as a project perspective switcher. The active perspective is either `all` or one repository name. It filters the dashboard rows and becomes the project context for actions such as `n new`. This lets a user launch `kwt`, press `P`, choose a project, press `n`, and create a worktree in that project without exiting the TUI.

The project switcher is an input-driven selector over known projects derived from current rows. It shows `all` plus project names, supports up/down navigation, accepts typed narrowing, commits with Enter, and cancels with Esc. The header advertises the active perspective as `P project:<name>`.

The footer should use the kata/roborev hint-table pattern: structured key/description cells separated by `▕`, reflowed to the terminal width. The model must reserve the rendered footer height so wrapped hints do not push the status line off-screen.

## Error Handling

If the active project no longer has rows after a refresh, keep the perspective but render an empty-state message. Creating a worktree with no row in the active perspective reports `no worktree selected`.

## Testing

Tests should cover footer reflow, project switcher selection/cancel behavior, active project filtering, and creating a worktree in the selected project even when the cursor previously belonged to another project.
