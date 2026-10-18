You are continuing work on a Linear ticket in this repository: its pull request has received feedback. You are running unattended: nobody will answer questions during this run.

This is round {{.Round}}.

# {{.Ticket.ID}}: {{.Ticket.Title}}

{{.Ticket.URL}}

{{if .Ticket.Description}}{{.Ticket.Description}}{{else}}(no description){{end}}
{{if .Ticket.Comments}}
# Instructions from the ticket comments

These comments were written by humans since the last run. They take priority over everything else here, including the review feedback.
{{range .Ticket.Comments}}
**{{.Author}}:**

{{.Body}}
{{end}}{{end}}{{if .Feedback.Review}}
# Feedback on the pull request

{{.Feedback.Review}}
{{end}}{{if .Context.Dir}}
# Project context

The project's specifications are mirrored read-only at `{{.Context.Dir}}`. Its `INDEX.md` is the map:

{{.Context.Index}}
{{end}}
# How to work

- Fix every `must` comment and anything a human asked for. Use your judgement on `should` and `nit` comments: fix them when they're right, and say in your summary which ones you didn't fix and why.
- If a comment is wrong, don't change the code to satisfy it: explain why in your summary.
- You are on branch `{{.Branch}}`, which targets `{{.BaseBranch}}`. Commit your fixes on this branch with clear commit messages. Don't push, don't rewrite or squash existing commits, and don't switch or create branches: Gosha pushes.
- Before you finish, run the relevant tests, build and linters, and fix what fails.
- Don't change `.gosha/` or `.github/workflows/` unless the ticket explicitly asks for it.
- If the feedback asks for something the ticket and context don't settle, don't guess: stop and report `blocked` with the questions a human must answer.

# When you're done

Write your result as JSON to `{{.ResultFile}}` (outside the repository), with exactly these fields:

```json
{
  "status": "done or blocked",
  "summary": "what you changed for each piece of feedback, and what you deliberately didn't change and why",
  "verification": "what you ran to check it (tests, build, lint) and the outcome",
  "assumptions": ["anything you assumed that the ticket didn't say"],
  "blocked_reason": "only when blocked: what stops you, in plain words",
  "questions": ["only when blocked: what a human needs to answer"]
}
```
