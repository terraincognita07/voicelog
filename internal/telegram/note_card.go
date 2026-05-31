package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Note card — the single-note detail view opened by tapping a note in a
// /pending or /recent list. It gathers every per-note action behind one
// list button (instead of a fat row of icons per note):
//
//	📝 #9 · Sat, Apr 2 · 22:04
//	«full text»
//	🏷 todo, философия
//	[✏️ Edit] [🏷 Tags]
//	[🗑 Delete] [⬅ To list]
//
// Editing reuses the saved-reply ✏️ button (editBtn / cbEditOpen): it opens
// the in-place edit menu, carrying this card's id:kind:state so the flow can
// restore the exact card on cancel / after the edit lands.
// Tags opens a sub-view with one [tag ❌] button each plus [➕ Add]. Deleting
// confirms, then returns to the list. Every button carries a cardRef so the
// flow can rebuild the exact originating list on the way back.

var (
	openCardBtn      = tele.InlineButton{Unique: "note_open"}      // list row → open the card
	cardTagsBtn      = tele.InlineButton{Unique: "card_tags"}      // card → tags sub-view
	cardDeleteBtn    = tele.InlineButton{Unique: "card_del"}       // card → delete confirm
	cardDeleteYesBtn = tele.InlineButton{Unique: "card_del_y"}     // confirm delete
	cardDeleteNoBtn  = tele.InlineButton{Unique: "card_del_n"}     // cancel → back to card
	cardBackBtn      = tele.InlineButton{Unique: "card_back"}      // card → back to the list
	cardTagAddBtn    = tele.InlineButton{Unique: "card_tag_add"}   // tags → force-reply add prompt
	cardTagRemoveBtn = tele.InlineButton{Unique: "card_tag_rm"}    // tags → remove one tag
	cardTagsBackBtn  = tele.InlineButton{Unique: "card_tags_back"} // tags → back to card
)

// cardRef is the callback payload threaded through every card button: the
// note id, the originating list kind ("p"=/pending, "r"=/recent), and that
// list's encoded state — so "⬅ back" rebuilds the exact view (filter, page,
// expanded day) the user came from. Encoded as `id:kind:state`; well under
// Telegram's 64-byte cap (id ≤ 19 digits, kind 1, state ≤ ~20).
type cardRef struct {
	id    int64
	kind  string
	state string
}

func (r cardRef) encode() string {
	return strconv.FormatInt(r.id, 10) + ":" + r.kind + ":" + r.state
}

// encodeTagRemove adds a tag index for the [tag ❌] buttons: `id:kind:idx:state`.
// The tag itself isn't in the payload — a 64-char tag would blow the 64-byte
// budget — so removal is by position in the note's (stable, alphabetical) list.
func (r cardRef) encodeTagRemove(idx int) string {
	return strconv.FormatInt(r.id, 10) + ":" + r.kind + ":" + strconv.Itoa(idx) + ":" + r.state
}

func validCardKind(s string) bool { return s == "p" || s == "r" }

func parseCardRef(data string) (cardRef, bool) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 3)
	if len(parts) < 2 {
		return cardRef{}, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 || !validCardKind(parts[1]) {
		return cardRef{}, false
	}
	state := ""
	if len(parts) == 3 {
		state = parts[2]
	}
	return cardRef{id: id, kind: parts[1], state: state}, true
}

func parseTagRemove(data string) (cardRef, int, bool) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 4)
	if len(parts) < 3 {
		return cardRef{}, 0, false
	}
	id, e1 := strconv.ParseInt(parts[0], 10, 64)
	idx, e2 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || id <= 0 || !validCardKind(parts[1]) {
		return cardRef{}, 0, false
	}
	state := ""
	if len(parts) == 4 {
		state = parts[3]
	}
	return cardRef{id: id, kind: parts[1], state: state}, idx, true
}

// parseListRef decodes the `kind:state` payload of the card's "⬅ to list"
// button. Defensive: an unknown/garbage kind falls back to /pending.
func parseListRef(data string) (kind, state string) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 2)
	kind = "p"
	if len(parts) > 0 && parts[0] == "r" {
		kind = "r"
	}
	if len(parts) == 2 {
		state = parts[1]
	}
	return kind, state
}

// renderListByKind rebuilds the originating list view from a card ref.
func (tb *Bot) renderListByKind(ctx context.Context, kind, state string) (string, *tele.ReplyMarkup, error) {
	if kind == "r" {
		return tb.renderRecent(ctx, parseRecentState(state))
	}
	return tb.renderPending(ctx, parsePendingState(state))
}

// renderCard builds the single-note detail view + its action keyboard.
func (tb *Bot) renderCard(ctx context.Context, ref cardRef) (string, *tele.ReplyMarkup, error) {
	n, err := tb.db.GetNote(ctx, ref.id)
	if err != nil {
		return "", nil, err
	}
	tags, terr := tb.db.TagsForNote(ctx, ref.id)
	if terr != nil {
		tb.logger.Warn("card: load tags", "err", terr)
	}
	when := tb.msg.DayLabel(n.CreatedAt) + " · " + n.CreatedAt.Format("15:04")
	body := tb.msg.CardBody(n.ID, when, previewText(n.RawText, 3500), tags)

	data := ref.encode()
	edit := editBtn
	edit.Text = tb.msg.EditBtn
	edit.Data = data // id:kind:state — the edit menu restores this exact view on cancel
	tagsBtn := cardTagsBtn
	tagsBtn.Text = tb.msg.CardTagsBtn
	tagsBtn.Data = data
	del := cardDeleteBtn
	del.Text = tb.msg.DeleteBtn
	del.Data = data
	back := cardBackBtn
	back.Text = tb.msg.CardToListBtn
	back.Data = ref.kind + ":" + ref.state

	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{edit, tagsBtn},
		{del, back},
	}}
	return body, kb, nil
}

// renderCardTags builds the tags sub-view: one [tag ❌] button per tag, then
// [➕ Add] / [⬅ Back].
func (tb *Bot) renderCardTags(ctx context.Context, ref cardRef) (string, *tele.ReplyMarkup, error) {
	tags, err := tb.db.TagsForNote(ctx, ref.id)
	if err != nil {
		return "", nil, err
	}
	var tagBtns []tele.InlineButton
	for i, tg := range tags {
		b := cardTagRemoveBtn
		b.Text = tg + " ❌"
		b.Data = ref.encodeTagRemove(i)
		tagBtns = append(tagBtns, b)
	}
	rows := chunkButtons(tagBtns, 2)

	add := cardTagAddBtn
	add.Text = tb.msg.VocabAddBtn // "➕ Add" — same affordance as /vocab
	add.Data = ref.encode()
	back := cardTagsBackBtn
	back.Text = tb.msg.CardBackBtn
	back.Data = ref.encode()
	rows = append(rows, []tele.InlineButton{add, back})

	return tb.msg.CardTagsHeader(ref.id, len(tags)), &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// --- callbacks ------------------------------------------------------------

func (tb *Bot) cbOpenCard(c tele.Context) error {
	return tb.editToCard(c, parseCardRef)
}

func (tb *Bot) cbCardTagsBack(c tele.Context) error {
	return tb.editToCard(c, parseCardRef)
}

func (tb *Bot) cbCardDeleteNo(c tele.Context) error {
	return tb.editToCard(c, parseCardRef)
}

// editToCard parses a cardRef from the callback and re-renders the card in
// place. Shared by open / tags-back / delete-cancel — they all land on the
// card view.
func (tb *Bot) editToCard(c tele.Context, parse func(string) (cardRef, bool)) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, ok := parse(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, kb, err := tb.renderCard(ctx, ref)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(ref.id))
			return c.Respond()
		}
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbCardTags(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, ok := parseCardRef(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, kb, err := tb.renderCardTags(ctx, ref)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbCardBack(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	kind, state := parseListRef(cb.Data)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, kb, err := tb.renderListByKind(ctx, kind, state)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbCardDeleteAsk(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, ok := parseCardRef(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	data := ref.encode()
	yes := cardDeleteYesBtn
	yes.Text = tb.msg.DeleteYesBtn
	yes.Data = data
	no := cardDeleteNoBtn
	no.Text = tb.msg.DeleteNoBtn
	no.Data = data
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	tb.tryEdit(c, tb.msg.DeleteAsk(ref.id), kb)
	return c.Respond()
}

func (tb *Bot) cbCardDeleteYes(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, ok := parseCardRef(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tb.deleteNote(ctx, ref.id); err != nil && !errors.Is(err, db.ErrNoteNotFound) {
		return tb.errToast(c, "delete", err)
	}
	body, kb, err := tb.renderListByKind(ctx, ref.kind, ref.state)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbCardTagRemove(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, idx, ok := parseTagRemove(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tags, err := tb.db.TagsForNote(ctx, ref.id)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	if idx >= 0 && idx < len(tags) {
		if _, err := tb.db.RemoveTag(ctx, ref.id, tags[idx]); err != nil {
			return tb.errToast(c, "tag rm", err)
		}
	}
	body, kb, err := tb.renderCardTags(ctx, ref)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

// cbCardTagAdd opens a force-reply prompt; the user's reply is caught in
// onText (via tagAddReplyNoteID) and applied through handleTagAddReply.
func (tb *Bot) cbCardTagAdd(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	ref, ok := parseCardRef(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	_ = c.Respond()
	return c.Send(tb.msg.TagAddPrompt(ref.id), &tele.ReplyMarkup{ForceReply: true, Selective: true})
}

// matchTagAddPrompt recovers the note id from a tag-add force-reply prompt:
// the id is the prompt's only number and a re-render of TagAddPrompt(id) must
// reproduce the text exactly (locale-agnostic, see firstInt).
func (tb *Bot) matchTagAddPrompt(promptText string) (int64, bool) {
	id, ok := firstInt(promptText)
	if !ok {
		return 0, false
	}
	if tb.msg.TagAddPrompt(id) != promptText {
		return 0, false
	}
	return id, true
}

// tagAddReplyNoteID reports whether msg is a reply to a tag-add prompt and,
// if so, the note id it targets.
func (tb *Bot) tagAddReplyNoteID(msg *tele.Message) (int64, bool) {
	if msg.ReplyTo == nil || msg.ReplyTo.Sender == nil || tb.bot.Me == nil {
		return 0, false
	}
	if msg.ReplyTo.Sender.ID != tb.bot.Me.ID {
		return 0, false
	}
	return tb.matchTagAddPrompt(strings.TrimSpace(msg.ReplyTo.Text))
}

// handleTagAddReply parses the reply as space-separated tags and attaches
// them to the note.
func (tb *Bot) handleTagAddReply(c tele.Context, id int64, text string) error {
	tags := strings.Fields(text)
	if len(tags) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	added, err := tb.db.AddTags(ctx, id, tags)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(tb.msg.NotFound(id))
		}
		return tb.errReply(c, "tag add", err)
	}
	return c.Send(tb.msg.TagsAdded(added))
}
