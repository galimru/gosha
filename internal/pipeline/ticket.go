package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/galimru/gosha/internal/agent"
	"github.com/galimru/gosha/internal/github"
	"github.com/galimru/gosha/internal/gitx"
	"github.com/galimru/gosha/internal/linear"
	"github.com/galimru/gosha/internal/notify"
	"github.com/galimru/gosha/internal/store"
	"github.com/galimru/gosha/internal/tmpl"
)

// Steps a run records (DESIGN §8).
const (
	StepClaimed   = "claimed"
	StepImplement = "implement"
	StepPush      = "push"
	StepPR        = "pr"
	StepReview    = "review"
	StepAddress   = "address"
	StepChecks    = "checks"
	StepFix       = "fix"
	StepSync      = "sync"
	StepMerge     = "merge"
	StepDeploy    = "deploy"
	StepBlocked   = "blocked"
)

// Run outcomes for runs that end without delivering.
const (
	OutcomeRestarted = "restarted"
	OutcomeMerged    = store.OutcomeMerged
	// OutcomeDelivered is a run Gosha merged and reported In Review.
	OutcomeDelivered = store.OutcomeDelivered
)

// ModeFollowUp is a fresh run on a ticket whose earlier PR was merged.
const ModeFollowUp = "fresh (follow-up)"

// Parked reports whether an open run is waiting for a human rather than
// active, so the project can pick up another ticket.
func Parked(r *store.Run) bool { return r.Step == StepBlocked }

// issueOps is the Linear surface a ticket run uses.
type issueOps interface {
	Transition(ctx context.Context, issueID, stateID string) error
	CreateComment(ctx context.Context, issueID, body string) (string, error)
	CreateAttachment(ctx context.Context, issueID, link, title, subtitle string) error
	Comments(ctx context.Context, issueID string) ([]linear.Comment, error)
}

// prOps is the GitHub surface a ticket run uses.
type prOps interface {
	Token(ctx context.Context) (string, error)
	PR(ctx context.Context, number int) (*github.PR, error)
	CreatePR(ctx context.Context, branch, base, title, body string) (*github.PR, error)
	ClosePR(ctx context.Context, number int) error
	CreateReview(ctx context.Context, number int, sha, body string, comments []github.ReviewComment) (int64, error)
	CreateComment(ctx context.Context, number int, body string) error
	HumanFeedback(ctx context.Context, number int, since time.Time) ([]github.Feedback, error)
	Checks(ctx context.Context, sha string) ([]github.Check, error)
	JobLog(ctx context.Context, jobID int64) (string, error)
	Merge(ctx context.Context, number int, sha, method, title, message string) (string, error)
	DeployRun(ctx context.Context, workflow, sha, branch string) (*github.WorkflowRun, error)
	RunJobs(ctx context.Context, runID int64) ([]github.Job, error)
}

// ticket is one pickup of one ticket.
type ticket struct {
	e      *Env
	p      *Project
	states *linear.States
	issues issueOps
	prs    prOps
	bot    agent.Identity
	// kb is this pickup's frozen copy of the project context.
	kb tmpl.Context

	issue linear.Issue
	// comments are the human comments since the last run: instructions.
	comments []linear.Comment
	// cursor is the newest comment on the ticket. It's saved once the agent
	// has had the comments, so an interrupted run doesn't lose them.
	cursor  *store.Cursor
	run     *store.Run
	started time.Time
	// prev is the ticket's last run whose PR was merged: what a follow-up
	// builds on.
	prev *store.Run
	// report is what the Delivered comment will say, kept on the run.
	report *delivery
	// fromReady is set when a human moved the ticket to Ready, as opposed
	// to an interrupted run continuing: it resets the round limits.
	fromReady bool
	// resuming is set when the pickup continues an open run.
	resuming bool
	// budget is what's left of agent.timeout in this pickup. Agent passes
	// and agent.setup spend it; waiting for CI doesn't.
	budget time.Duration
	// actx is the current agent pass's context, bounded by the budget.
	actx context.Context
	// recheck is set when the next review is of a fix for failed checks or
	// conflicts: approving it doesn't use up a review round.
	recheck bool
}

// agentPass starts an agent pass (or agent.setup) bounded by what's left of
// the budget, in t.actx. end charges the time it took.
func (t *ticket) agentPass(ctx context.Context) (end func()) {
	start := time.Now()
	actx, cancel := context.WithTimeoutCause(ctx, t.budget, errAgentTimeout)
	t.actx = actx
	return func() {
		t.budget -= time.Since(start)
		cancel()
	}
}

// Pass does one pickup pass for a project: it picks the next Ready ticket
// (if any) and takes it as far as it can go. It reports whether a ticket
// was handled. With only set, just that ticket is considered.
func (e *Env) Pass(ctx context.Context, name, only string) (bool, error) {
	p, err := e.LoadProject(ctx, name)
	if err != nil {
		return false, err
	}
	for _, w := range p.Warnings {
		e.Log.Warn("config warning", "project", name, "warning", w)
	}
	lin, err := e.ConnectLinear(ctx, p)
	if err != nil {
		return false, err
	}
	refreshed := false
	if e.contextDue(ctx, p) {
		if err := e.refreshContext(ctx, p); err != nil {
			return false, err
		}
		refreshed = true
	}
	e.resume(ctx, p)
	c, err := e.interrupted(ctx, name, lin, only)
	if err != nil {
		return false, err
	}
	if c == nil {
		cands, err := e.candidates(ctx, name, lin, only)
		if err != nil {
			return false, err
		}
		for _, d := range Plan(p.Config, lin.States, cands) {
			if d.Action == Pickup {
				i := slices.IndexFunc(cands, func(c Candidate) bool { return c.Issue.ID == d.Issue.ID })
				c = &cands[i]
				break
			}
		}
	}
	if c == nil {
		return false, nil
	}
	// A pickup always starts from the latest context.
	if !refreshed {
		if err := e.refreshContext(ctx, p); err != nil {
			return false, err
		}
	}
	kb, err := e.freezeContext(p, c.Issue.Identifier)
	if err != nil {
		return false, err
	}
	bot, err := e.botIdentity(ctx, p.Install)
	if err != nil {
		return false, err
	}
	return true, e.handle(ctx, p, lin.States, lin.Client, p.Install, bot, kb, *c)
}

// interrupted finds a run that stopped mid-step (usage limit, shutdown)
// while its ticket stayed In Progress, so it continues before anything new
// is picked up. A ticket a human moved elsewhere is left alone.
func (e *Env) interrupted(ctx context.Context, name string, lin *Linear, only string) (*Candidate, error) {
	runs, err := e.Store.OpenRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if r.Project != name || Parked(r) || (only != "" && r.TicketID != only) {
			continue
		}
		is, err := lin.Client.Issue(ctx, r.TicketID)
		if err != nil {
			return nil, err
		}
		if is.State.ID != lin.States.InProgress.ID {
			continue
		}
		c := Candidate{Issue: *is, OpenRun: r}
		if c.Delivered, err = e.Store.LastDelivered(ctx, name, r.TicketID); err != nil {
			return nil, err
		}
		cur, err := e.Store.CommentCursor(ctx, name, r.TicketID)
		if err != nil {
			return nil, err
		}
		if c.Comments, err = lin.Client.Comments(ctx, is.ID); err != nil {
			return nil, err
		}
		c.NewComments = NewHumanComments(c.Comments, cur)
		return &c, nil
	}
	return nil, nil
}

// botIdentity looks up the commit identity once per process.
func (e *Env) botIdentity(ctx context.Context, inst *github.Installation) (agent.Identity, error) {
	e.botMu.Lock()
	defer e.botMu.Unlock()
	if e.bot != nil {
		return *e.bot, nil
	}
	info, err := e.App.Info(ctx)
	if err != nil {
		return agent.Identity{}, err
	}
	id, err := inst.BotIdentity(ctx, *info)
	if err != nil {
		return agent.Identity{}, err
	}
	e.bot = &agent.Identity{Name: id.Name, Email: id.Email}
	return *e.bot, nil
}

// handle claims a ticket and runs it (DESIGN §8.1, §8.2). Failures after
// the claim go Blocked, except a usage limit or a shutdown: those leave the
// ticket In Progress and the run resumable.
func (e *Env) handle(ctx context.Context, p *Project, st *linear.States, is issueOps, prs prOps, bot agent.Identity, kb tmpl.Context, c Candidate) error {
	t := &ticket{e: e, p: p, states: st, issues: is, prs: prs, bot: bot, kb: kb,
		issue: c.Issue, comments: c.NewComments, run: c.OpenRun, prev: c.Delivered, report: &delivery{}, started: time.Now(),
		fromReady: c.Issue.State.ID != st.InProgress.ID, resuming: c.OpenRun != nil, budget: p.Config.Agent.Timeout}
	if t.run != nil {
		t.report = loadReport(t.run)
	}
	if err := t.claim(ctx, c.Comments); err != nil {
		return err
	}
	err := t.drive(ctx)
	var le *agent.LimitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &le):
		e.Notify.Send(notify.Event{Project: p.Name, Text: fmt.Sprintf("%s · ⏸ %v", t.issue.Identifier, le)})
		return err
	case ctx.Err() != nil:
		return err
	}
	e.Log.Error("ticket failed", "project", p.Name, "ticket", t.issue.Identifier, "step", t.run.Step, "err", err)
	if berr := t.block(ctx, "Something went wrong: "+err.Error(), nil); berr != nil {
		return errors.Join(err, berr)
	}
	return nil
}

// claim moves the ticket to In Progress, records the run and notes the
// newest comment, which becomes the cursor once the agent has the comments.
func (t *ticket) claim(ctx context.Context, all []linear.Comment) error {
	if err := t.issues.Transition(ctx, t.issue.ID, t.states.InProgress.ID); err != nil {
		return err
	}
	mode := ModeFresh
	switch {
	case HasRestart(t.comments):
		mode = ModeRestart
	case t.run != nil:
		mode = ModeResume
	case t.prev != nil:
		mode = ModeFollowUp
	}
	if t.run == nil {
		t.run = &store.Run{Project: t.p.Name, TicketID: t.issue.Identifier, Mode: mode, Step: StepClaimed, BaseBranch: t.p.Base}
		if err := t.e.Store.CreateRun(ctx, t.run); err != nil {
			return err
		}
	} else {
		// A follow-up stays one when it resumes, so it keeps its prompt.
		if t.run.Mode != ModeFollowUp || mode == ModeRestart {
			t.run.Mode = mode
		}
		// A restart never continues the old session.
		t.resuming = mode != ModeRestart
		if err := t.save(ctx, t.run.Step); err != nil {
			return err
		}
	}
	if len(all) > 0 {
		last := all[len(all)-1]
		t.cursor = &store.Cursor{CommentID: last.ID, At: last.CreatedAt}
	}
	t.e.Log.Info("picked up", "project", t.p.Name, "ticket", t.issue.Identifier, "mode", mode)
	t.notify(fmt.Sprintf("%s %s · picked up (%s)", t.issue.Identifier, t.issue.Title, mode), t.ticketLink())
	return nil
}

// drive decides the mode from the real state of the branch and PR, then
// implements, pushes and opens the PR.
func (t *ticket) drive(ctx context.Context) error {
	reuse := ""
	switch {
	case HasRestart(t.comments):
		if t.run.Branch != "" {
			reuse = t.run.Branch
			if err := t.restart(ctx, t.run); err != nil {
				return err
			}
			if err := t.newRun(ctx, ModeRestart); err != nil {
				return err
			}
		}
	case t.run.PRNumber != 0:
		pr, err := t.prs.PR(ctx, t.run.PRNumber)
		if err != nil {
			return err
		}
		switch {
		case pr.Merged && t.run.MergeSHA != "" && t.fromReady && len(instructions(t.comments)) > 0:
			// Blocked after the merge (its deploy failed), and a human asked
			// for a change: that's a follow-up on the merged code.
			if err := t.e.Store.FinishRun(ctx, t.run.ID, OutcomeDelivered); err != nil {
				return err
			}
			t.prev = t.run
			if err := t.newRun(ctx, ModeFollowUp); err != nil {
				return err
			}
		case pr.Merged && t.run.MergeSHA != "":
			// Gosha merged it and stopped before delivering, or its deploy
			// is to be checked again.
			return t.afterMerge(ctx)
		case pr.Merged:
			if err := t.e.Store.FinishRun(ctx, t.run.ID, OutcomeMerged); err != nil {
				return err
			}
			t.prev = t.run
			if err := t.newRun(ctx, ModeFollowUp); err != nil {
				return err
			}
		case pr.State == "open":
			return t.resumeOpen(ctx)
		default:
			// Closed without merging: start over.
			reuse = t.run.Branch
			if err := t.restart(ctx, t.run); err != nil {
				return err
			}
			if err := t.newRun(ctx, ModeRestart); err != nil {
				return err
			}
		}
	}
	if t.run.Mode == ModeFollowUp && t.run.Branch == "" && len(instructions(t.comments)) == 0 {
		return t.blockWith(ctx, "The ticket's earlier pull request is already merged, and there's no new comment saying what to change.", nil,
			"comment what's wrong or what to change, then move the ticket to Ready and I'll open a follow-up pull request.")
	}
	if t.run.Branch == "" {
		branch, err := t.chooseBranch(ctx, reuse)
		if err != nil {
			return err
		}
		t.run.Branch = branch
	}
	if t.run.BaseBranch == "" {
		t.run.BaseBranch = t.p.Base
	}
	return t.implement(ctx)
}

// newRun replaces the current run with a fresh one.
func (t *ticket) newRun(ctx context.Context, mode string) error {
	t.run = &store.Run{Project: t.p.Name, TicketID: t.issue.Identifier, Mode: mode, Step: StepClaimed, BaseBranch: t.p.Base}
	t.report, t.resuming = &delivery{}, false
	return t.e.Store.CreateRun(ctx, t.run)
}

// restart closes the run's PR, deletes its branch and checkout and ends it.
func (t *ticket) restart(ctx context.Context, r *store.Run) error {
	if r.PRNumber != 0 {
		pr, err := t.prs.PR(ctx, r.PRNumber)
		if err != nil {
			return err
		}
		if pr.State == "open" {
			if err := t.prs.ClosePR(ctx, r.PRNumber); err != nil {
				return err
			}
		}
	}
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return err
	}
	if err := t.p.Repo.DeleteBranch(ctx, tok, r.Branch); err != nil {
		return err
	}
	if err := os.RemoveAll(t.checkout()); err != nil {
		return err
	}
	return t.e.Store.FinishRun(ctx, r.ID, OutcomeRestarted)
}

// chooseBranch renders the branch pattern and adds -2, -3, … while the
// name is taken, on GitHub or by an earlier run: Gosha never takes over a
// branch it doesn't own (DESIGN §8.2). reuse is a name this run may take
// back because it just deleted it.
func (t *ticket) chooseBranch(ctx context.Context, reuse string) (string, error) {
	if reuse != "" {
		return reuse, nil
	}
	name, err := t.p.Config.BranchName(Ticket(t.issue, nil))
	if err != nil {
		return "", err
	}
	owned, err := t.e.Store.Branches(ctx, t.p.Name)
	if err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		cand := name
		if n > 1 {
			cand = fmt.Sprintf("%s-%d", name, n)
		}
		if slices.Contains(owned, cand) {
			continue
		}
		remote, err := t.p.Repo.RemoteBranchExists(ctx, cand)
		if err != nil {
			return "", err
		}
		local, err := t.p.Repo.LocalBranchExists(ctx, cand)
		if err != nil {
			return "", err
		}
		if !remote && !local {
			return cand, nil
		}
	}
}

func (t *ticket) checkout() string {
	return filepath.Join(t.e.Home.Project(t.p.Name).Worktrees(), t.issue.Identifier)
}

func (t *ticket) save(ctx context.Context, step string) error {
	t.run.Step = step
	t.run.Report = t.report.encode()
	return t.e.Store.UpdateRun(ctx, t.run)
}

// implement runs the implementer in the ticket checkout, then collects,
// pushes and opens the PR (DESIGN §8.2).
func (t *ticket) implement(ctx context.Context) error {
	if err := t.save(ctx, StepImplement); err != nil {
		return err
	}
	checkout := t.checkout()
	if t.run.AgentRuns == 0 {
		// A new run never inherits a checkout from an earlier one.
		if err := os.RemoveAll(checkout); err != nil {
			return err
		}
	}
	fresh, rp, err := t.prepare(ctx, checkout, agent.RoleImplementer)
	if err != nil || t.run.Step == StepBlocked {
		return err
	}
	resultFile := filepath.Join(rp.agent.RunDir, "result.json")
	prompt, err := agent.RenderPrompt(ctx, t.p.Repo, t.p.Commit, "implement.md", tmpl.Data{
		Project: t.p.Name, Ticket: Ticket(t.issue, t.comments), Context: t.kb,
		Branch: t.run.Branch, BaseBranch: t.run.BaseBranch, ResultFile: resultFile, FollowUp: t.followUp(),
	})
	if err != nil {
		return t.block(ctx, err.Error(), nil)
	}
	spec := t.spec(prompt, checkout, rp)
	if t.resuming && !fresh {
		spec.Resume = t.run.ImplSession
	}
	res, err := t.runImplementer(ctx, spec, resultFile)
	if res == nil {
		return err
	}
	if ok, err := t.collect(ctx, checkout); !ok {
		return err
	}
	ahead, err := t.p.Repo.CommitsAhead(ctx, t.run.BaseBranch, t.run.Branch)
	if err != nil {
		return err
	}
	if ahead == 0 {
		return t.block(ctx, "No changes produced: the agent finished without committing anything.", nil)
	}
	diff, err := t.p.Repo.Diff(ctx, t.run.BaseBranch, t.run.Branch)
	if err != nil {
		return err
	}
	t.notify(fmt.Sprintf("%s · implemented · %s · %s\n%s", t.issue.Identifier, diff, time.Since(t.started).Round(time.Minute), res.Summary))
	return t.publish(ctx, res, diff)
}

// prepare makes checkout ready for an agent pass: it clones it if needed,
// numbers the pass, lays out its files and runs agent.setup in a fresh
// clone. A failed setup sends the ticket Blocked.
func (t *ticket) prepare(ctx context.Context, checkout, role string) (fresh bool, rp runPaths, err error) {
	fresh, err = t.p.Repo.CloneCheckout(ctx, checkout, t.run.BaseBranch, t.run.Branch)
	if err != nil {
		return false, rp, err
	}
	t.run.AgentRuns++
	if err := t.save(ctx, t.run.Step); err != nil {
		return false, rp, err
	}
	if rp, err = t.runPaths(t.run.AgentRuns, role, checkout); err != nil {
		return false, rp, err
	}
	if fresh && len(t.p.Config.Agent.Setup) > 0 {
		end := t.agentPass(ctx)
		defer end()
		if err := t.setup(t.actx, checkout, rp); err != nil {
			if errors.Is(context.Cause(t.actx), errAgentTimeout) {
				return false, rp, t.block(ctx, fmt.Sprintf("`agent.setup` ran out of time (agent.timeout %v).", t.p.Config.Agent.Timeout), nil)
			}
			if ctx.Err() != nil {
				return false, rp, err
			}
			return false, rp, t.block(ctx, err.Error(), nil)
		}
	}
	return fresh, rp, nil
}

func (t *ticket) spec(prompt, dir string, rp runPaths) agent.Spec {
	cfg := t.p.Config.Agent
	return agent.Spec{
		Binary: t.e.ClaudeBinary, Prompt: prompt, Dir: dir, SettingsFile: rp.settings, Transcript: rp.transcript,
		Mode: cfg.PermissionMode, Model: cfg.Model,
		OAuthToken: t.e.Global.Claude.OAuthToken, ConfigDir: t.e.Home.Project(t.p.Name).Claude(), TmpDir: rp.agent.RunDir,
		Git: t.bot, Redact: t.e.Redactor.Redact, AddDirs: nonEmpty(t.kb.Dir),
	}
}

func nonEmpty(s ...string) []string {
	return slices.DeleteFunc(s, func(x string) bool { return x == "" })
}

// runImplementer runs an implementer pass (implement or address) and maps
// its failures. It returns a nil result when the pass ended the step: the
// ticket went Blocked (nil error), or a usage limit or shutdown (error).
func (t *ticket) runImplementer(ctx context.Context, spec agent.Spec, resultFile string) (*agent.ImplementerResult, error) {
	defer t.agentPass(ctx)()
	actx := t.actx
	res, out, err := t.e.implement(actx, spec, resultFile)
	if err != nil && spec.Resume != "" && !isStop(actx, err) {
		// The session may be gone (another host, a cleared config dir):
		// start a new one in the same checkout.
		t.e.Log.Warn("resume failed, starting a new session", "ticket", t.issue.Identifier, "err", err)
		spec.Resume = ""
		res, out, err = t.e.implement(actx, spec, resultFile)
	}
	if out != nil && out.SessionID != "" {
		t.run.ImplSession = out.SessionID
		if serr := t.save(ctx, t.run.Step); serr != nil {
			return nil, serr
		}
	}
	if !isStop(actx, err) || errors.Is(context.Cause(actx), errAgentTimeout) {
		if err := t.consumeComments(ctx); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, t.agentFailed(ctx, "agent", err)
	}
	if res.Status == agent.StatusBlocked {
		return nil, t.block(ctx, res.BlockedReason, res.Questions)
	}
	t.report.addResult(res)
	return res, t.save(ctx, t.run.Step)
}

// agentFailed handles a failed agent pass: a usage limit or shutdown is
// returned as it is, anything else sends the ticket Blocked.
func (t *ticket) agentFailed(ctx context.Context, who string, err error) error {
	var le *agent.LimitError
	switch {
	case errors.As(err, &le):
		return err
	case errors.Is(context.Cause(t.actx), errAgentTimeout):
		return t.block(ctx, fmt.Sprintf("The %s ran out of time: agent.timeout (%v) is the budget for all agent work in one pickup.", who, t.p.Config.Agent.Timeout), nil)
	case ctx.Err() != nil:
		return err
	}
	return t.block(ctx, fmt.Sprintf("The %s failed: %v", who, err), nil)
}

// collect fetches the agent's work from the checkout into the bare clone.
// It returns false when there's nothing to collect and the ticket went
// Blocked, or on error.
func (t *ticket) collect(ctx context.Context, checkout string) (bool, error) {
	err := t.p.Repo.FetchFrom(ctx, checkout, t.run.Branch)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, gitx.ErrNotCheckout) {
		return false, t.block(ctx, "The ticket checkout is damaged (its .git isn't a directory). Comment `/restart` and move the ticket to Ready to start over.", nil)
	}
	var gerr *gitx.Error
	if errors.As(err, &gerr) && strings.Contains(gerr.Stderr, "couldn't find remote ref") {
		return false, t.block(ctx, fmt.Sprintf("No changes produced: the agent left no commits on `%s`.", t.run.Branch), nil)
	}
	return false, err
}

var errAgentTimeout = errors.New("agent.timeout reached")

// newComments adds the human ticket comments posted since the pickup (or
// the last pass) to the instructions, so a comment written while Gosha
// works reaches the next implementer pass.
func (t *ticket) newComments(ctx context.Context) error {
	all, err := t.issues.Comments(ctx, t.issue.ID)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return nil
	}
	t.comments = append(t.comments, NewHumanComments(all, t.cursor)...)
	last := all[len(all)-1]
	t.cursor = &store.Cursor{CommentID: last.ID, At: last.CreatedAt}
	return nil
}

// consumeComments moves the comment cursor past the comments the agent has
// now been given.
func (t *ticket) consumeComments(ctx context.Context) error {
	if t.cursor == nil {
		return nil
	}
	return t.e.Store.SetCommentCursor(ctx, t.p.Name, t.issue.Identifier, *t.cursor)
}

// isStop reports errors that end the step without being the agent's fault.
func isStop(ctx context.Context, err error) bool {
	var le *agent.LimitError
	return errors.As(err, &le) || ctx.Err() != nil
}

// publish pushes the branch and opens the PR as the GitHub App.
func (t *ticket) publish(ctx context.Context, res *agent.ImplementerResult, diff gitx.DiffStat) error {
	if err := t.save(ctx, StepPush); err != nil {
		return err
	}
	if ok, err := t.push(ctx); !ok {
		return err
	}
	if err := t.save(ctx, StepPR); err != nil {
		return err
	}
	title := t.issue.Identifier + ": " + t.issue.Title
	if t.run.Mode == ModeFollowUp {
		title += " (follow-up)"
	}
	pr, err := t.prs.CreatePR(ctx, t.run.Branch, t.run.BaseBranch, title, prBody(t.issue, res, t.followUp()))
	if err != nil {
		return err
	}
	t.run.PRNumber, t.run.PRURL = pr.Number, pr.URL
	if err := t.save(ctx, StepPR); err != nil {
		return err
	}
	if err := t.issues.CreateAttachment(ctx, t.issue.ID, pr.URL, fmt.Sprintf("PR #%d", pr.Number), diff.String()); err != nil {
		return err
	}
	t.run.BlockedReason = ""
	if err := t.save(ctx, StepReview); err != nil {
		return err
	}
	t.e.Log.Info("PR opened", "project", t.p.Name, "ticket", t.issue.Identifier, "pr", pr.URL)
	t.notify(fmt.Sprintf("%s · PR #%d opened", t.issue.Identifier, pr.Number), t.prLink(), t.ticketLink())
	return t.reviewLoop(ctx, false)
}

// push pushes the branch from the bare clone. It returns false when GitHub
// refused it and the ticket went Blocked, or on error.
func (t *ticket) push(ctx context.Context) (bool, error) {
	tok, err := t.prs.Token(ctx)
	if err != nil {
		return false, err
	}
	err = t.p.Repo.Push(ctx, tok, t.run.Branch)
	if errors.Is(err, gitx.ErrWorkflowPermission) {
		return false, t.block(ctx, "GitHub refused the push because it changes `.github/workflows/`. "+
			"Gosha's GitHub App deliberately has no Workflows permission, so agents can't change CI or deploy pipelines. "+
			"Either make that change yourself, or comment that the workflow change should be left out and move the ticket to Ready.", nil)
	}
	return err == nil, err
}

func prBody(is linear.Issue, res *agent.ImplementerResult, fu tmpl.FollowUp) string {
	var b strings.Builder
	if fu.PR != 0 {
		fmt.Fprintf(&b, "Follow-up to #%d.\n\n", fu.PR)
	}
	b.WriteString(strings.TrimSpace(res.Summary) + "\n")
	if v := strings.TrimSpace(res.Verification); v != "" {
		b.WriteString("\n### Verification\n" + v + "\n")
	}
	if len(res.Assumptions) > 0 {
		b.WriteString("\n### Assumptions\n")
		for _, a := range res.Assumptions {
			b.WriteString("- " + a + "\n")
		}
	}
	if len(res.LeftOut) > 0 {
		b.WriteString("\n### Left out\n")
		for _, a := range res.LeftOut {
			b.WriteString("- " + a + "\n")
		}
	}
	fmt.Fprintf(&b, "\n---\nTicket: [%s](%s)\n", is.Identifier, is.URL)
	return b.String()
}

// runPaths lays out agent pass n of the ticket and creates its dirs.
type runPaths struct {
	agent      agent.Paths
	settings   string
	transcript string
	setupLog   string
}

func (t *ticket) runPaths(n int, role, checkout string) (runPaths, error) {
	// The sandbox matches real paths, so resolve symlinks in the home.
	home, err := filepath.EvalSymlinks(t.e.Home.Dir())
	if err != nil {
		return runPaths{}, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return runPaths{}, err
	}
	pp := filepath.Join(home, "projects", t.p.Name)
	runs := filepath.Join(pp, "runs", t.issue.Identifier)
	rp := runPaths{
		agent: agent.Paths{
			Home: home, Checkout: filepath.Join(pp, "worktrees", filepath.Base(checkout)),
			RunDir: filepath.Join(runs, fmt.Sprint(n)), Context: t.kb.Dir, UserHome: userHome, Writable: t.e.Global.Agent.Writable,
		},
		settings:   filepath.Join(runs, fmt.Sprintf("%d-settings.json", n)),
		transcript: filepath.Join(runs, fmt.Sprintf("%d-%s.jsonl", n, role)),
		setupLog:   filepath.Join(runs, fmt.Sprintf("%d-setup.log", n)),
	}
	if err := os.MkdirAll(t.e.Home.Project(t.p.Name).Claude(), 0o700); err != nil {
		return runPaths{}, err
	}
	if err := rp.agent.EnsureDirs(); err != nil {
		return runPaths{}, err
	}
	data, err := agent.Settings(rp.agent, role, t.e.Global.Agent.SandboxEnabled())
	if err != nil {
		return runPaths{}, err
	}
	return rp, os.WriteFile(rp.settings, data, 0o600)
}

// setup runs agent.setup in a fresh checkout. It runs outside the agent
// sandbox, with the agent's scrubbed environment (DESIGN §12).
func (t *ticket) setup(ctx context.Context, dir string, rp runPaths) error {
	log, err := os.OpenFile(rp.setupLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	for _, c := range t.p.Config.Agent.Setup {
		fmt.Fprintf(log, "$ %s\n", c)
		cmd := exec.CommandContext(ctx, "sh", "-c", c)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + rp.agent.RunDir}
		var tail tailWriter
		cmd.Stdout, cmd.Stderr = &tail, &tail
		err := cmd.Run()
		log.WriteString(t.e.Redactor.Redact(string(tail.buf)))
		if err != nil {
			return fmt.Errorf("`agent.setup` command `%s` failed: %v\n\n```\n%s\n```", c, err, strings.TrimSpace(lastLines(string(tail.buf), 20)))
		}
	}
	return nil
}

// tailWriter keeps the last 64 KiB of output.
type tailWriter struct{ buf []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - 64<<10; over > 0 {
		w.buf = w.buf[over:]
	}
	return len(p), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (t *ticket) ticketLink() notify.Link {
	return notify.Link{Text: t.issue.Identifier, URL: t.issue.URL}
}

func (t *ticket) prLink() notify.Link {
	return notify.Link{Text: fmt.Sprintf("PR #%d", t.run.PRNumber), URL: t.run.PRURL}
}

func (t *ticket) notify(text string, links ...notify.Link) {
	var ls []notify.Link
	for _, l := range links {
		if l.URL != "" {
			ls = append(ls, l)
		}
	}
	t.e.Notify.Send(notify.Event{Project: t.p.Name, Text: text, Links: ls})
}
