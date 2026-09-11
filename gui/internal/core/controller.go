package core

import (
	"context"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/pkg/ipc"
)

// Default timings. The two command timeouts match what cmd/svpn uses, so the
// GUI gives up on the same schedule the CLI does rather than inventing a
// second answer to "how long is too long".
const (
	// DefaultPoll is how often the daemon is asked for its status. The
	// protocol has no subscription, so a client that wants a live byte counter
	// polls; once a second over a unix socket costs nothing and is what makes
	// a throughput readout possible at all.
	DefaultPoll = time.Second
	// DefaultStatusTimeout bounds one poll. It is short because a status reply
	// is a struct the daemon already has in hand, and because this one blocks
	// the controller's loop.
	DefaultStatusTimeout = 3 * time.Second
	// DefaultCommandTimeout bounds connect and disconnect. Connecting is the
	// slow case: it allocates the session, negotiates PPP and rewrites the
	// routing table before it answers.
	DefaultCommandTimeout = 2 * time.Minute
	// DefaultLoginTimeout is how long the identity provider has to call back,
	// which is really how long the user has to finish authenticating.
	DefaultLoginTimeout = 3 * time.Minute
)

// Options tune a Controller. The zero value uses the defaults above.
type Options struct {
	Poll           time.Duration
	StatusTimeout  time.Duration
	CommandTimeout time.Duration
	LoginTimeout   time.Duration
}

func (o Options) withDefaults() Options {
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	if o.StatusTimeout <= 0 {
		o.StatusTimeout = DefaultStatusTimeout
	}
	if o.CommandTimeout <= 0 {
		o.CommandTimeout = DefaultCommandTimeout
	}
	if o.LoginTimeout <= 0 {
		o.LoginTimeout = DefaultLoginTimeout
	}

	return o
}

// Controller owns the client's state and is the only thing that changes it.
//
// Every mutation happens on the goroutine running Run, reached through a
// channel of closures. Nothing here takes a lock, and the UI never reads a
// field: it receives finished Snapshots. The alternative — widgets reading
// shared state under a mutex while a login blocks for three minutes — is where
// this kind of program usually goes wrong.
type Controller struct {
	daemon Daemon
	login  Authenticator
	opts   Options

	commands chan func()
	updates  chan Snapshot
	done     chan struct{}

	// Everything below belongs to the Run goroutine.
	snap Snapshot
	// working gates a second action while one is in flight.
	//
	// It is a field rather than Snapshot.Busy because the two answer different
	// questions: Busy describes what the user is looking at, and is true all
	// through a connect the daemon is driving on its own. This says that *this
	// process* has a worker out, and it is what decides whether a status reply
	// may overwrite the phase — deriving it from the phase instead makes the
	// busy phases self-sustaining, and nothing ever leaves them.
	working bool
	// cancelWork abandons the action in flight, and is nil when none is.
	cancelWork context.CancelFunc
	// generation identifies the action in flight. A worker stamps every
	// message it posts back, so a reply from an action the user already
	// cancelled cannot overwrite the state of the one that replaced it.
	generation uint64
}

// New builds a controller. It does nothing until Run is called.
func New(daemon Daemon, login Authenticator, options Options) *Controller {
	return &Controller{
		daemon:   daemon,
		login:    login,
		opts:     options.withDefaults(),
		commands: make(chan func(), 16),
		updates:  make(chan Snapshot, 1),
		done:     make(chan struct{}),
		snap:     Snapshot{Phase: PhaseUnknown},
	}
}

// Updates carries the state to render. It holds the latest snapshot only: a
// consumer that falls behind — a window being dragged, a compositor stalling —
// skips intermediate states rather than replaying a queue of stale ones.
func (c *Controller) Updates() <-chan Snapshot { return c.updates }

// Run drives the controller until ctx is cancelled. It blocks, and everything
// else on this type is safe to call from any goroutine while it does.
func (c *Controller) Run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(c.opts.Poll)
	defer ticker.Stop()

	c.refresh()

	for {
		select {
		case <-ctx.Done():
			_ = c.abandonWork()

			return
		case command := <-c.commands:
			command()
		case <-ticker.C:
			c.refresh()
		}
	}
}

// Connect authenticates and asks the daemon to bring the tunnel up.
func (c *Controller) Connect() { c.post(c.startConnect) }

// Disconnect asks the daemon to take the tunnel down.
func (c *Controller) Disconnect() { c.post(c.startDisconnect) }

// Cancel abandons an action in flight.
//
// It reaches the browser login, which is the one that lasts long enough to be
// worth abandoning. It does not reach a request already handed to the daemon:
// ipc.Client.Do takes a timeout rather than a context, and a connect the
// daemon has begun is better finished than half undone.
func (c *Controller) Cancel() {
	c.post(func() {
		if c.abandonWork() {
			c.refresh()
		}
	})
}

// Refresh asks for the status now instead of at the next tick.
func (c *Controller) Refresh() { c.post(c.refresh) }

// post queues work for the Run goroutine, and gives up if Run has returned.
func (c *Controller) post(command func()) {
	select {
	case c.commands <- command:
	case <-c.done:
	}
}

// startConnect begins a connection. Runs on the Run goroutine.
func (c *Controller) startConnect() {
	if c.working {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.working = true
	c.cancelWork = cancel
	c.generation++
	generation := c.generation

	c.snap.Err = ""
	c.snap.LoginURL = ""
	c.snap.Phase = PhaseAuthenticating
	c.emit()

	go c.connect(ctx, cancel, generation)
}

// connect runs off the Run goroutine: the login blocks for as long as the user
// takes, and the daemon's reply for as long as a tunnel takes to build. Both
// would stop the status poll if they happened on the loop.
func (c *Controller) connect(ctx context.Context, cancel context.CancelFunc, generation uint64) {
	defer cancel()

	loginCtx, stop := context.WithTimeout(ctx, c.opts.LoginTimeout)
	defer stop()

	session, err := c.login(loginCtx, func(url string) {
		c.postFor(generation, func() {
			c.snap.LoginURL = url
			c.emit()
		})
	})
	if err != nil {
		c.postFor(generation, func() { c.finish(generation, err) })

		return
	}

	c.postFor(generation, func() {
		c.snap.Phase = PhaseConnecting
		c.snap.LoginURL = ""
		c.emit()
	})

	// The cookie is a live credential: it goes straight to the daemon and is
	// never put in a Snapshot, which is the value the UI renders and the one
	// thing here most likely to end up in a log or a crash report.
	response, err := c.daemon.Send(ipc.Request{
		Command: ipc.CommandConnect,
		Cookie:  session.Cookie,
		Host:    session.Host,
		Port:    session.Port,
	}, c.opts.CommandTimeout)

	c.postFor(generation, func() { c.finish(generation, commandError(response, err)) })
}

// startDisconnect begins a disconnection. Runs on the Run goroutine.
func (c *Controller) startDisconnect() {
	if c.working {
		return
	}

	c.working = true
	c.generation++
	generation := c.generation

	c.snap.Err = ""
	c.snap.Phase = PhaseDisconnecting
	c.emit()

	go func() {
		response, err := c.daemon.Send(
			ipc.Request{Command: ipc.CommandDisconnect},
			c.opts.CommandTimeout,
		)

		c.postFor(generation, func() { c.finish(generation, commandError(response, err)) })
	}()
}

// abandonWork cancels the action in flight and reports whether there was one.
//
// A disconnect leaves cancelWork nil, so Cancel correctly does nothing to it:
// there is no safe half of a disconnect to stop at, and the daemon is already
// tearing the interface down.
//
// Runs on the Run goroutine.
func (c *Controller) abandonWork() bool {
	if c.cancelWork == nil {
		return false
	}

	c.cancelWork()
	c.cancelWork = nil
	c.working = false
	c.generation++
	c.snap.LoginURL = ""

	return true
}

// finish ends an action and returns to whatever the daemon says is true.
func (c *Controller) finish(generation uint64, err error) {
	if generation != c.generation {
		return
	}

	c.working = false
	c.cancelWork = nil
	c.snap.LoginURL = ""

	// A login the user walked away from is not a failure to report back to
	// them: they know, they are the one who cancelled it.
	if err != nil && !errors.Is(err, context.Canceled) {
		c.snap.Err = err.Error()
	}

	c.refresh()
}

// postFor queues work that a stale worker's reply must not carry out.
func (c *Controller) postFor(generation uint64, command func()) {
	c.post(func() {
		if generation != c.generation {
			return
		}

		command()
	})
}

// refresh asks the daemon where things stand. Runs on the Run goroutine, and
// is the one blocking call left there — bounded by StatusTimeout, which is
// why that one is seconds rather than minutes.
func (c *Controller) refresh() {
	response, err := c.daemon.Send(ipc.Request{Command: ipc.CommandStatus}, c.opts.StatusTimeout)
	if err != nil {
		c.snap.Status = ipc.Status{}
		if !c.working {
			c.snap.Phase = PhaseNoDaemon
			c.snap.Err = err.Error()
		}

		c.emit()

		return
	}

	if response.Status != nil {
		c.snap.Status = *response.Status
	}

	// While an action is in flight the phase is this side's to own: the daemon
	// still says "disconnected" all through the browser login, which it knows
	// nothing about.
	if !c.working {
		c.snap.Phase = phaseOf(c.snap.Status.State)
	}

	c.emit()
}

// emit publishes the current snapshot, replacing any the UI has not taken yet.
func (c *Controller) emit() {
	select {
	case c.updates <- c.snap:
		return
	default:
	}

	// The buffer holds one snapshot and it is now stale. Drop it and put the
	// current one in its place; a UI that reads whole states wants the newest,
	// not the oldest.
	select {
	case <-c.updates:
	default:
	}

	select {
	case c.updates <- c.snap:
	default:
	}
}

// commandError reduces a reply to the failure it reports, if any.
//
// A transport error and a refusal carried in an OK=false reply are the same
// thing to the user: the tunnel did not come up, and here is why.
func commandError(response ipc.Response, err error) error {
	if err != nil {
		return err
	}

	if !response.OK {
		message := response.Error
		if message == "" {
			message = "the daemon refused the command without saying why"
		}

		return errors.Errorf("%s", message)
	}

	return nil
}
