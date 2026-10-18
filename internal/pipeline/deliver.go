package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/store"
	"github.com/galimru/gosha/internal/tmpl"
)

// deployPoll is how often the deploy run is polled (DESIGN §8.7). A
// variable for tests.
var deployPoll = 15 * time.Second

// delivery is what the Delivered comment reports (DESIGN §3). It's kept
// on the run as it's gathered, so a delivery resumed after a crash still
// has it, and a follow-up can be told what was shipped.
type delivery struct {
	Summary      string   `json:"summary,omitempty"`
	Verification string   `json:"verification,omitempty"`
	Assumptions  []string `json:"assumptions,omitempty"`
	LeftOut      []string `json:"left_out,omitempty"`
	// Reviews are the posted reviews, in order, and Fixes the check and
	// conflict rounds, in plain words.
	Reviews []reviewRound `json:"reviews,omitempty"`
	Fixes   []string      `json:"fixes,omitempty"`
	Checks  []string      `json:"checks,omitempty"`
	Diff    string        `json:"diff,omitempty"`
	Deploy  *deployResult `json:"deploy,omitempty"`
}

type reviewRound struct {
	Label    string `json:"label"`
	Approved bool   `json:"approved"`
	Must     int    `json:"must"`
	Should   int    `json:"should"`
	Nit      int    `json:"nit"`
}

type deployResult struct {
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	// Failure is the failing jobs with their failing step, when it failed.
	Failure string `json:"failure,omitempty"`
}

func (d *deployResult) ok() bool { return d != nil && d.Conclusion == "success" }

func loadReport(r *store.Run) *delivery {
	var d delivery
	if r.Report != "" {
		// A report Gosha can't read only costs detail in the comment.
		_ = json.Unmarshal([]byte(r.Report), &d)
	}
	return &d
}

func (d *delivery) encode() string {
	b, _ := json.Marshal(d)
	if string(b) == "{}" {
		return ""
	}
	return string(b)
}

// addResult records an implementer pass. The summary is the first pass's:
// later passes answer feedback rather than describe the change.
func (d *delivery) addResult(res *agent.ImplementerResult) {
	if d.Summary == "" {
		d.Summary = strings.TrimSpace(res.Summary)
	}
	if v := strings.TrimSpace(res.Verification); v != "" {
		d.Verification = v
	}
	d.Assumptions = appendNew(d.Assumptions, res.Assumptions)
	d.LeftOut = appendNew(d.LeftOut, res.LeftOut)
}

func (d *delivery) addReview(label string, res *agent.ReviewerResult) {
	d.Reviews = append(d.Reviews, reviewRound{
		Label: strings.TrimPrefix(label, "Gosha "), Approved: verdictMark(res) == "✅",
		Must: res.Count(agent.SeverityMust), Should: res.Count(agent.SeverityShould), Nit: res.Count(agent.SeverityNit),
	})
}

func appendNew(to, from []string) []string {
	for _, s := range from {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(to, s) {
			to = append(to, s)
		}
	}
	return to
}

// followUp is .FollowUp for a follow-up run: the ticket's earlier, merged
// pull request.
func (t *ticket) followUp() tmpl.FollowUp {
	if t.run.Mode != ModeFollowUp || t.prev == nil {
		return tmpl.FollowUp{}
	}
	d := loadReport(t.prev)
	f := tmpl.FollowUp{PR: t.prev.PRNumber, URL: t.prev.PRURL, MergeSHA: t.prev.MergeSHA, Summary: d.Summary}
	if d.Deploy != nil {
		f.Deploy = "succeeded"
		if !d.Deploy.ok() {
			f.Deploy = fmt.Sprintf("%s (%s)", d.Deploy.Conclusion, d.Deploy.URL)
			if d.Deploy.Failure != "" {
				f.Deploy += "\n\n" + d.Deploy.Failure
			}
		}
	}
	return f
}

// afterMerge cleans up the merged branch and checkout (DESIGN §8.5), then
// watches the deploy if one is configured, and delivers.
func (t *ticket) afterMerge(ctx context.Context) error {
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return err
	}
	if err := t.p.Repo.DeleteBranch(ctx, tok, t.run.Branch); err != nil {
		return err
	}
	// No agent runs after the merge, so the whole workspace can go.
	if err := os.RemoveAll(t.workspace().Dir); err != nil {
		return err
	}
	if t.p.Config.Deploy.Workflow != "" && !t.report.Deploy.ok() {
		return t.deployWatch(ctx)
	}
	return t.deliver(ctx)
}

// deployWatch waits for the deploy workflow's run on the merge commit
// (DESIGN §8.7). The code is merged already, so a failure goes Blocked for
// a human to see; moving the ticket to Ready looks at the deploy again.
func (t *ticket) deployWatch(ctx context.Context) error {
	if err := t.save(ctx, StepDeploy); err != nil {
		return err
	}
	cfg := t.p.Config.Deploy
	deadline := time.Now().Add(cfg.Timeout)
	var run *github.WorkflowRun
	for {
		var err error
		if run, err = t.prs.DeployRun(ctx, cfg.Workflow, t.run.MergeSHA, t.run.BaseBranch); err != nil {
			return err
		}
		if (run != nil && run.Status == "completed") || time.Now().After(deadline) {
			break
		}
		if err := sleep(ctx, deployPoll); err != nil {
			return err
		}
	}
	const need = "the code is already merged. Once the deploy is fixed or re-run, move the ticket to Ready and I'll check it again. " +
		"To have me change the code instead, comment what to change and move the ticket to Ready for a follow-up pull request."
	wf := "`" + cfg.Workflow + "`"
	switch {
	case run == nil:
		return t.blockWith(ctx, fmt.Sprintf("No run of %s started for the merge commit `%.7s` on `%s` within `deploy.timeout` (%v). "+
			"Check that the workflow runs on pushes to `%s`.", wf, t.run.MergeSHA, t.run.BaseBranch, cfg.Timeout, t.run.BaseBranch), nil, need)
	case run.Status != "completed":
		return t.blockWith(ctx, fmt.Sprintf("The deploy (%s) was still running after `deploy.timeout` (%v): %s", wf, cfg.Timeout, run.URL), nil, need)
	}
	t.report.Deploy = &deployResult{Conclusion: run.Conclusion, URL: run.URL}
	if run.Conclusion != "success" {
		t.report.Deploy.Failure = t.deployFailure(ctx, run)
		if err := t.save(ctx, StepDeploy); err != nil {
			return err
		}
		t.notify(fmt.Sprintf("%s · deploy ❌ %s", t.issue.Identifier, run.Conclusion), notify.Link{Text: "run", URL: run.URL})
		reason := fmt.Sprintf("The deploy (%s) ended with `%s`: %s", wf, run.Conclusion, run.URL)
		if f := t.report.Deploy.Failure; f != "" {
			reason += "\n\n" + f
		}
		return t.blockWith(ctx, reason, nil, need)
	}
	if err := t.save(ctx, StepDeploy); err != nil {
		return err
	}
	where := cfg.URL
	if where == "" {
		where = run.URL
	}
	t.notify(fmt.Sprintf("%s · deploy ✅ %s", t.issue.Identifier, where), notify.Link{Text: "run", URL: run.URL})
	return t.deliver(ctx)
}

// maxDeployJobs is how many failed deploy jobs get their log excerpt.
const maxDeployJobs = 3

// deployFailure is the failed deploy jobs, each with its failing step.
func (t *ticket) deployFailure(ctx context.Context, run *github.WorkflowRun) string {
	jobs, err := t.prs.RunJobs(ctx, run.ID)
	if err != nil {
		t.e.Log.Warn("deploy jobs", "ticket", t.issue.Identifier, "err", err)
		return ""
	}
	var b strings.Builder
	n := 0
	for _, j := range jobs {
		if j.Conclusion == "" || j.Conclusion == "success" || j.Conclusion == "skipped" || j.Conclusion == "neutral" {
			continue
		}
		fmt.Fprintf(&b, "**%s** (%s)\n", j.Name, j.Conclusion)
		if n++; n > maxDeployJobs {
			continue
		}
		log, err := t.prs.JobLog(ctx, j.ID)
		if err != nil {
			t.e.Log.Warn("deploy job log", "ticket", t.issue.Identifier, "job", j.Name, "err", err)
		} else if ex := failingStep(log); ex != "" {
			b.WriteString("```\n" + t.e.Redactor.Redact(ex) + "\n```\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// deliver reports on the ticket and moves it to In Review (DESIGN §8.8).
func (t *ticket) deliver(ctx context.Context) error {
	if _, err := t.issues.CreateComment(ctx, t.issue.ID, t.deliveredComment()); err != nil {
		return err
	}
	if err := t.issues.Transition(ctx, t.issue.ID, t.states.InReview.ID); err != nil {
		return err
	}
	if err := t.e.Store.FinishRun(ctx, t.run.ID, OutcomeDelivered); err != nil {
		return err
	}
	t.e.Log.Info("delivered", "project", t.p.Name, "ticket", t.issue.Identifier)
	t.notify(fmt.Sprintf("%s · ✅ delivered · In Review", t.issue.Identifier), t.ticketLink(), t.prLink())
	return nil
}

// deliveredComment is the Delivered comment (DESIGN §3).
func (t *ticket) deliveredComment() string {
	d, red := t.report, t.e.Redactor.Redact
	var b strings.Builder
	fmt.Fprintf(&b, "**Delivered:** [PR #%d](%s) is merged (`%.7s`)", t.run.PRNumber, t.run.PRURL, t.run.MergeSHA)
	if d.Diff != "" {
		fmt.Fprintf(&b, " · %s", d.Diff)
	}
	b.WriteString(".\n")
	if d.Summary != "" {
		b.WriteString("\n" + red(d.Summary) + "\n")
	}

	b.WriteString("\n**How it was checked**\n")
	if d.Verification != "" {
		b.WriteString("- Verification: " + red(oneLine(d.Verification)) + "\n")
	}
	if len(d.Reviews) > 0 {
		var rs []string
		for _, r := range d.Reviews {
			s := fmt.Sprintf("%s ✅", r.Label)
			if !r.Approved {
				s = fmt.Sprintf("%s ❌ (%d must, %d should) → addressed", r.Label, r.Must, r.Should)
			}
			rs = append(rs, s)
		}
		b.WriteString("- Review: " + strings.Join(rs, "; ") + "\n")
	} else {
		fmt.Fprintf(&b, "- Review: %d round(s) by Gosha's reviewer\n", max(t.run.ReviewRound, 1))
	}
	for _, f := range d.Fixes {
		b.WriteString("- " + f + "\n")
	}
	switch {
	case len(d.Checks) > 0:
		b.WriteString("- Checks passed: " + codeList(d.Checks) + "\n")
	case t.run.CheckedSHA != "":
		b.WriteString("- Checks: none expected\n")
	}

	if dep := d.Deploy; dep != nil {
		b.WriteString("\n**Deployment**\n")
		fmt.Fprintf(&b, "- `%s` %s: %s\n", t.p.Config.Deploy.Workflow, dep.Conclusion, dep.URL)
		if u := t.p.Config.Deploy.URL; u != "" {
			b.WriteString("- " + u + "\n")
		}
	}
	writeList(&b, "Assumptions", d.Assumptions, red)
	writeList(&b, "Left out", d.LeftOut, red)
	b.WriteString("\nMove the ticket to Done once you've accepted it, or comment what's wrong and move it to Ready for a follow-up.\n")
	return b.String()
}

func writeList(b *strings.Builder, title string, items []string, red func(string) string) {
	if len(items) == 0 {
		return
	}
	b.WriteString("\n**" + title + "**\n")
	for _, s := range items {
		b.WriteString("- " + red(s) + "\n")
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
