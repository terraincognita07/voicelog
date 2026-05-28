package telegram

import (
	"context"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/whisper"
)

// fakeCtx implements tele.Context for unit tests. Only Send / Edit / Notify
// / Respond / Callback / Message / Sender are exercised by the handlers we
// test; the rest return zero values. Mutations are captured under mu so
// goroutine-driven tests (none today) won't race.
type fakeCtx struct {
	mu sync.Mutex

	sent         []sentMessage
	edited       []editedMessage
	notifies     []tele.ChatAction
	responses    []*tele.CallbackResponse
	respondCalls int   // count of Respond invocations regardless of args
	notifyErr    error // injected error returned from Notify
	sendErr      error // injected error returned from Send
	editErr      error // injected error returned from Edit
	respondErr   error // injected error returned from Respond

	// inputs handlers read from
	message  *tele.Message
	callback *tele.Callback
	sender   *tele.User
}

type sentMessage struct {
	What interface{}
	Opts []interface{}
}

type editedMessage struct {
	What interface{}
	Opts []interface{}
}

// --- Capture sinks --------------------------------------------------------

func (c *fakeCtx) Send(what interface{}, opts ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sentMessage{What: what, Opts: opts})
	return c.sendErr
}

func (c *fakeCtx) Edit(what interface{}, opts ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edited = append(c.edited, editedMessage{What: what, Opts: opts})
	return c.editErr
}

func (c *fakeCtx) Notify(action tele.ChatAction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifies = append(c.notifies, action)
	return c.notifyErr
}

func (c *fakeCtx) Respond(resp ...*tele.CallbackResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.respondCalls++
	c.responses = append(c.responses, resp...)
	return c.respondErr
}

func (c *fakeCtx) RespondText(text string) error {
	return c.Respond(&tele.CallbackResponse{Text: text})
}

func (c *fakeCtx) RespondAlert(text string) error {
	return c.Respond(&tele.CallbackResponse{Text: text, ShowAlert: true})
}

// --- Inputs ---------------------------------------------------------------

func (c *fakeCtx) Message() *tele.Message   { return c.message }
func (c *fakeCtx) Callback() *tele.Callback { return c.callback }
func (c *fakeCtx) Sender() *tele.User       { return c.sender }

func (c *fakeCtx) Data() string {
	if c.callback != nil {
		return c.callback.Data
	}
	if c.message != nil {
		return c.message.Payload
	}
	return ""
}

func (c *fakeCtx) Text() string {
	if c.message != nil {
		return c.message.Text
	}
	return ""
}

// --- Zero-value stubs (interface filler) ---------------------------------
//
// The tele.Context interface has ~40 methods; the handlers under test
// touch only the small subset above. Everything else returns zero values
// so the fake satisfies the interface without behavioral commitments.

func (c *fakeCtx) Bot() *tele.Bot                              { return nil }
func (c *fakeCtx) Update() tele.Update                         { return tele.Update{} }
func (c *fakeCtx) Query() *tele.Query                          { return nil }
func (c *fakeCtx) InlineResult() *tele.InlineResult            { return nil }
func (c *fakeCtx) ShippingQuery() *tele.ShippingQuery          { return nil }
func (c *fakeCtx) PreCheckoutQuery() *tele.PreCheckoutQuery    { return nil }
func (c *fakeCtx) Poll() *tele.Poll                            { return nil }
func (c *fakeCtx) PollAnswer() *tele.PollAnswer                { return nil }
func (c *fakeCtx) ChatMember() *tele.ChatMemberUpdate          { return nil }
func (c *fakeCtx) ChatJoinRequest() *tele.ChatJoinRequest      { return nil }
func (c *fakeCtx) Migration() (int64, int64)                   { return 0, 0 }
func (c *fakeCtx) Topic() *tele.Topic                          { return nil }
func (c *fakeCtx) Boost() *tele.BoostUpdated                   { return nil }
func (c *fakeCtx) BoostRemoved() *tele.BoostRemoved            { return nil }
func (c *fakeCtx) Chat() *tele.Chat                            { return nil }
func (c *fakeCtx) Recipient() tele.Recipient                   { return nil }
func (c *fakeCtx) Entities() tele.Entities                     { return nil }
func (c *fakeCtx) Args() []string                              { return nil }
func (c *fakeCtx) SendAlbum(tele.Album, ...interface{}) error  { return nil }
func (c *fakeCtx) Reply(interface{}, ...interface{}) error     { return nil }
func (c *fakeCtx) Forward(tele.Editable, ...interface{}) error { return nil }
func (c *fakeCtx) ForwardTo(tele.Recipient, ...interface{}) error {
	return nil
}
func (c *fakeCtx) EditCaption(string, ...interface{}) error      { return nil }
func (c *fakeCtx) EditOrSend(interface{}, ...interface{}) error  { return nil }
func (c *fakeCtx) EditOrReply(interface{}, ...interface{}) error { return nil }
func (c *fakeCtx) Delete() error                                 { return nil }
func (c *fakeCtx) DeleteAfter(time.Duration) *time.Timer         { return nil }
func (c *fakeCtx) Ship(...interface{}) error                     { return nil }
func (c *fakeCtx) Accept(...string) error                        { return nil }
func (c *fakeCtx) Answer(*tele.QueryResponse) error              { return nil }
func (c *fakeCtx) Get(string) interface{}                        { return nil }
func (c *fakeCtx) Set(string, interface{})                       {}

// Compile-time check that fakeCtx still satisfies tele.Context.
var _ tele.Context = (*fakeCtx)(nil)

// --- Capture helpers (read under the same lock) --------------------------

func (c *fakeCtx) lastSent() (sentMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return sentMessage{}, false
	}
	return c.sent[len(c.sent)-1], true
}

func (c *fakeCtx) lastEdit() (editedMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.edited) == 0 {
		return editedMessage{}, false
	}
	return c.edited[len(c.edited)-1], true
}

// --- Fake transcriber -----------------------------------------------------

// fakeTranscriber is a stub for the transcriber interface. result and err
// are returned verbatim; calls records every input so tests can assert on
// prompt content / src path / call count.
type fakeTranscriber struct {
	mu     sync.Mutex
	calls  []transcribeCall
	result whisper.Result
	err    error
}

type transcribeCall struct {
	SrcPath string
	Prompt  string
}

func (f *fakeTranscriber) Transcribe(_ context.Context, srcPath, prompt string) (whisper.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, transcribeCall{SrcPath: srcPath, Prompt: prompt})
	return f.result, f.err
}

func (f *fakeTranscriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTranscriber) lastCall() (transcribeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return transcribeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}
