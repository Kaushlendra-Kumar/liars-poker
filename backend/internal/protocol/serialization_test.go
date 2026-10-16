package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
	"github.com/kaushlendra/liars-poker/backend/pkg/poker"
)

// Wire representation tests.
//
// Go marshals a nil slice as JSON null, which means a client can meet two
// spellings of "empty" for the same field and will usually only discover the
// second one in production, on an ordinary empty round. These tests pin the
// rule at the producer:
//
//	a public slice field must marshal as an array, never as null.
//
// One field is deliberately exempt. PlayerSummary.Cards is tagged omitempty,
// so an empty hand is *absent* rather than null, and absence carries meaning
// there: it says "not yours to see", which is a different statement from
// "has no cards". The rule below is therefore "array, or absent when
// omitempty — never null".

// sliceFields is every JSON field name in the public payloads whose Go type
// is a slice.
//
// TestSliceFieldInventoryIsComplete keeps this honest: adding a new slice
// field to any public payload fails that test until the field is listed
// here, which is the moment to decide whether it can marshal as null.
var sliceFields = map[string]struct{}{
	"players": {},
	"claims":  {},
	"witness": {},
	"cards":   {}, // omitempty: absent when empty, never null
}

// omitemptySliceFields are the slice fields allowed to be absent entirely.
var omitemptySliceFields = map[string]struct{}{
	"cards": {},
}

// ---------------------------------------------------------------- helpers

// assertNoNullSlices fails if any guarded field appears as null anywhere in
// the marshalled message.
func assertNoNullSlices(t *testing.T, label string, msg Outbound) []byte {
	t.Helper()
	raw, err := Encode(msg)
	if err != nil {
		t.Fatalf("%s: encode: %v", label, err)
	}
	for field := range sliceFields {
		needle := []byte(`"` + field + `":null`)
		if bytes.Contains(raw, needle) {
			t.Fatalf("%s: %s marshalled as null:\n%s", label, field, raw)
		}
	}
	return raw
}

// assertFieldIsArray walks decoded JSON and checks every occurrence of a
// guarded field is an array, or absent when that is allowed.
func assertFieldIsArray(t *testing.T, label string, raw []byte) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: decode: %v", label, err)
	}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch node := v.(type) {
		case map[string]any:
			for key, child := range node {
				childPath := path + "." + key
				if _, guarded := sliceFields[key]; guarded {
					if child == nil {
						t.Fatalf("%s: %s is null, want an array", label, childPath)
					}
					if _, ok := child.([]any); !ok {
						t.Fatalf("%s: %s is %T, want an array", label, childPath, child)
					}
				}
				walk(child, childPath)
			}
		case []any:
			for i, child := range node {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(decoded, "")
}

// collectSliceFields walks a Go type and records the JSON names of every
// slice field it can reach.
func collectSliceFields(t reflect.Type, out map[string]struct{}, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if seen[t] {
		return
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported, never marshalled
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() != reflect.Uint8 {
				if name == "" {
					name = f.Name
				}
				out[name] = struct{}{}
			}
			collectSliceFields(f.Type, out, seen)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		collectSliceFields(t.Elem(), out, seen)
	}
}

// ---------------------------------------------------------------- tests

// TestSliceFieldInventoryIsComplete is the future-proofing. A new slice field
// on any public payload lands here first.
func TestSliceFieldInventoryIsComplete(t *testing.T) {
	found := map[string]struct{}{}
	for _, payload := range []any{
		WelcomePayload{}, SnapshotPayload{}, ViewPayload{}, ClaimSpacePayload{},
		ErrorPayload{}, ClosedPayload{}, EventPayload{},
	} {
		collectSliceFields(reflect.TypeOf(payload), found, map[reflect.Type]bool{})
	}

	var missing, extra []string
	for name := range found {
		if _, ok := sliceFields[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range sliceFields {
		if _, ok := found[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Fatalf("public payloads have slice fields not covered by these tests: %v\n"+
			"add them to sliceFields and make sure the producer cannot emit null for them",
			missing)
	}
	if len(extra) > 0 {
		t.Fatalf("sliceFields lists fields no public payload has: %v", extra)
	}
}

// TestEmptySlicesMarshalAsArrays covers the states where the slices are
// genuinely empty, which is where a nil would show up.
func TestEmptySlicesMarshalAsArrays(t *testing.T) {
	// A brand-new room: no players, no claims. Both slices are nil in the
	// state and must still be arrays on the wire.
	empty := game.NewGame(game.DefaultConfig(1))
	raw := assertNoNullSlices(t, "empty room view", ViewMessage(game.View(empty, "nobody")))
	assertFieldIsArray(t, "empty room view", raw)
	if !bytes.Contains(raw, []byte(`"players":[]`)) {
		t.Fatalf("empty players did not marshal as []:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(`"claims":[]`)) {
		t.Fatalf("empty claims did not marshal as []:\n%s", raw)
	}

	// A welcome wraps the same view.
	raw = assertNoNullSlices(t, "welcome",
		WelcomeMessage("p_a", "ABCDEF", "tok", game.View(empty, "p_a")))
	assertFieldIsArray(t, "welcome", raw)

	// A snapshot likewise.
	raw = assertNoNullSlices(t, "snapshot", SnapshotMessage(game.View(empty, "p_a")))
	assertFieldIsArray(t, "snapshot", raw)

	// The claim ladder.
	raw = assertNoNullSlices(t, "claim space", ClaimSpaceMessage())
	assertFieldIsArray(t, "claim space", raw)
}

// TestFalseShowResultWitnessMarshalsAsArray is the case the rule exists for:
// a claim that turned out false has no witness, so the slice is nil at the
// source.
func TestFalseShowResultWitnessMarshalsAsArray(t *testing.T) {
	result := &game.ShowResult{
		Round:      2,
		Claimant:   "p_a",
		Challenger: "p_b",
		Claim:      poker.NewQuads(poker.Ace),
		ClaimTrue:  false,
		Witness:    nil, // nothing to show: the claim was a bluff
		Loser:      "p_a",
		Mode:       poker.ModeReducedPool,
		PoolSize:   4,
		HandSize:   4,
	}

	// Through an event.
	raw := assertNoNullSlices(t, "false result event",
		EventMessage(game.Event{
			Seq: 9, Type: game.EvClaimValidated, Round: 2, Result: result,
		}, "p_b"))
	assertFieldIsArray(t, "false result event", raw)
	if !bytes.Contains(raw, []byte(`"witness":[]`)) {
		t.Fatalf("a false result's witness did not marshal as []:\n%s", raw)
	}

	// And the projection must not have mutated the caller's result.
	if result.Witness != nil {
		t.Fatal("ProjectEvent wrote back into the event's own ShowResult")
	}

	// Through a view.
	s := startedGame(t)
	s.LastShow = result
	raw = assertNoNullSlices(t, "false result view", ViewMessage(game.View(s, "p_a")))
	assertFieldIsArray(t, "false result view", raw)
	if !bytes.Contains(raw, []byte(`"witness":[]`)) {
		t.Fatalf("a view's false-result witness did not marshal as []:\n%s", raw)
	}
}

// TestSlicesAcrossEveryObservablePhase sweeps real states rather than
// hand-built ones, so the rule is checked where it actually has to hold.
func TestSlicesAcrossEveryObservablePhase(t *testing.T) {
	s := startedGame(t)
	phases := []struct {
		label string
		state game.State
	}{{"claiming", s}}

	// Claim, then resolve a SHOW, so a revealed round and a round end are
	// both covered.
	turn := s.Players[s.TurnSeat].ID
	next, _, err := game.Apply(s, game.ActionMakeClaim{
		Meta: game.Meta{PlayerID: turn}, Claim: poker.NewPair(poker.Two)})
	if err != nil {
		t.Fatal(err)
	}
	phases = append(phases, struct {
		label string
		state game.State
	}{"one claim standing", next})

	challenger := next.Players[next.TurnSeat].ID
	after, events, err := game.Apply(next, game.ActionCallShow{
		Meta: game.Meta{PlayerID: challenger}})
	if err != nil {
		t.Fatal(err)
	}
	phases = append(phases, struct {
		label string
		state game.State
	}{"revealed round end", after})

	for _, p := range phases {
		for i := range p.state.Players {
			viewer := p.state.Players[i].ID
			label := fmt.Sprintf("%s / %s", p.label, viewer)
			raw := assertNoNullSlices(t, label, ViewMessage(game.View(p.state, viewer)))
			assertFieldIsArray(t, label, raw)
		}
	}

	// Every event emitted by the SHOW, projected to every player.
	for _, e := range events {
		for i := range after.Players {
			viewer := after.Players[i].ID
			label := fmt.Sprintf("event %s / %s", e.Type, viewer)
			raw := assertNoNullSlices(t, label, EventMessage(e, viewer))
			assertFieldIsArray(t, label, raw)
		}
	}
}

// TestCardsIsAbsentRatherThanNull documents the one exemption: an empty hand
// is omitted, because absence means "not yours to see" there.
func TestCardsIsAbsentRatherThanNull(t *testing.T) {
	if _, ok := omitemptySliceFields["cards"]; !ok {
		t.Fatal("cards should be the omitempty exemption")
	}
	s := startedGame(t)
	raw, err := Encode(ViewMessage(game.View(s, s.Players[0].ID)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"cards":null`)) {
		t.Fatalf("cards marshalled as null:\n%s", raw)
	}
	// The viewer sees their own hand; the others carry no cards field at all.
	var wrapper struct {
		Payload struct {
			View json.RawMessage `json:"view"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if err := json.Unmarshal(wrapper.Payload.View, &view); err != nil {
		t.Fatal(err)
	}
	players, ok := view["players"].([]any)
	if !ok {
		t.Fatalf("players is not an array: %T", view["players"])
	}
	withCards := 0
	for _, p := range players {
		entry := p.(map[string]any)
		if raw, present := entry["cards"]; present {
			if raw == nil {
				t.Fatal("a present cards field was null")
			}
			withCards++
		}
	}
	if withCards != 1 {
		t.Fatalf("%d players carried a cards field, want exactly 1 (the viewer)", withCards)
	}
}

// startedGame returns a four-player game in CLAIMING.
func startedGame(t *testing.T) game.State {
	t.Helper()
	cfg := game.DefaultConfig(77)
	cfg.FirstStarter = game.FirstStarterSeatZero
	s := game.NewGame(cfg)
	for _, id := range []game.PlayerID{"p_a", "p_b", "p_c", "p_d"} {
		var err error
		s, _, err = game.Apply(s, game.ActionJoin{
			Meta: game.Meta{PlayerID: id}, Name: string(id)})
		if err != nil {
			t.Fatal(err)
		}
		s, _, err = game.Apply(s, game.ActionSetReady{
			Meta: game.Meta{PlayerID: id}, Ready: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, _, err := game.Apply(s, game.ActionStartGame{Meta: game.Meta{PlayerID: "p_a"}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
