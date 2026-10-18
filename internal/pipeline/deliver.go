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
	Summary     string        `json:"summary,omitempty"`
	Assumptions []string      `json:"assumptions,omitempty"`
	LeftOut     []string      `json:"left_out,omitempty"`
	Deploy      *deployResult `json:"deploy,omitempty"`
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
	d.Assumptions = appendNew(d.Assumptions, res.Assumptions)
	d.LeftOut = appendNew(d.LeftOut, res.LeftOut)
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
	if t.p.Config.Deploy.Workflow != "" && t.report.Deploy == nil {
		return t.deployWatch(ctx)
	}
	return t.deliver(ctx)
}

// deployWatch waits for the deploy workflow's run on the merge commit and
// records how it went (DESIGN §8.7). The code is merged already, so the
// ticket is delivered either way: the Delivered comment reports the deploy,
// and a failure is fixed through a follow-up.
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
	switch {
	case run == nil:
		t.report.Deploy = &deployResult{Conclusion: "no run",
			Failure: fmt.Sprintf("No run of `%s` started for the merge commit within `deploy.timeout` (%v).", cfg.Workflow, cfg.Timeout)}
	case run.Status != "completed":
		t.report.Deploy = &deployResult{Conclusion: "still running", URL: run.URL,
			Failure: fmt.Sprintf("Still running after `deploy.timeout` (%v).", cfg.Timeout)}
	default:
		t.report.Deploy = &deployResult{Conclusion: run.Conclusion, URL: run.URL}
		if run.Conclusion != "success" {
			t.report.Deploy.Failure = t.deployFailure(ctx, run)
		}
	}
	if err := t.save(ctx, StepDeploy); err != nil {
		return err
	}
	t.e.Log.Info("deploy", "project", t.p.Name, "ticket", t.issue.Identifier, "conclusion", t.report.Deploy.Conclusion, "url", t.report.Deploy.URL)
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
	t.leave()
	if err := t.issues.Transition(ctx, t.issue.ID, t.states.InReview.ID); err != nil {
		return err
	}
	if err := t.e.Store.FinishRun(ctx, t.run.ID, OutcomeDelivered); err != nil {
		return err
	}
	t.e.Log.Info("delivered", "project", t.p.Name, "ticket", t.issue.Identifier)
	msg := fmt.Sprintf("%s · ✅ delivered · In Review", t.issue.Identifier)
	if d := t.report.Deploy; d != nil && !d.ok() {
		msg += " · deploy ❌ " + d.Conclusion
	}
	t.notify(msg, t.ticketLink(), t.prLink())
	return nil
}

// deliveredComment is the Delivered comment (DESIGN §3): what shipped, the
// deploy, and what was assumed or left out. The rest is on the PR.
func (t *ticket) deliveredComment() string {
	d, red := t.report, t.e.Redactor.Redact
	var b strings.Builder
	fmt.Fprintf(&b, "**Delivered:** [PR #%d](%s) is merged (`%.7s`).\n", t.run.PRNumber, t.run.PRURL, t.run.MergeSHA)
	if d.Summary != "" {
		b.WriteString("\n" + red(d.Summary) + "\n")
	}
	if dep := d.Deploy; dep != nil {
		mark := "✅"
		if !dep.ok() {
			mark = "❌"
		}
		fmt.Fprintf(&b, "\n**Deploy:** %s `%s` %s", mark, t.p.Config.Deploy.Workflow, dep.Conclusion)
		if dep.URL != "" {
			b.WriteString(" · [run](" + dep.URL + ")")
		}
		if u := t.p.Config.Deploy.URL; u != "" && dep.ok() {
			b.WriteString(" · " + u)
		}
		b.WriteString("\n")
		if !dep.ok() {
			if dep.Failure != "" {
				b.WriteString("\n" + red(dep.Failure) + "\n")
			}
			b.WriteString("\nThe code is merged. To have Gosha fix the deploy, comment what to change and move the ticket to Ready for a follow-up.\n")
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
