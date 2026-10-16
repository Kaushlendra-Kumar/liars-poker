package session

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
)

func TestTokenRedactsEverywhereButReveal(t *testing.T) {
	reg := NewRegistry("ABCDEF", rand.Reader)
	tok, err := reg.Issue("p_1")
	if err != nil {
		t.Fatal(err)
	}
	secret := tok.Reveal()
	if secret == "" {
		t.Fatal("token has no secret")
	}

	// Every formatting verb and JSON must redact. This is the mechanism that
	// keeps the credential out of logs.
	for _, rendered := range []string{
		tok.String(),
		fmt.Sprintf("%s", tok),
		fmt.Sprintf("%v", tok),
		fmt.Sprintf("%+v", tok),
		fmt.Sprintf("%#v", tok),
		fmt.Sprint(tok),
		fmt.Sprintf("%v", struct{ T Token }{tok}),
		fmt.Sprintf("%v", map[string]Token{"t": tok}),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("token leaked through formatting: %q", rendered)
		}
	}
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(secret)) {
		t.Fatalf("token leaked through JSON: %s", b)
	}
	b, err = json.Marshal(struct{ Token Token }{tok})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(secret)) {
		t.Fatalf("token leaked through an embedding struct: %s", b)
	}
}

func TestVerifyBindsToRoomAndPlayer(t *testing.T) {
	regA := NewRegistry("ROOMAA", rand.Reader)
	regB := NewRegistry("ROOMBB", rand.Reader)

	tok, err := regA.Issue("p_alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := regA.Verify("ROOMAA", "p_alice", tok); err != nil {
		t.Fatalf("the issuing room rejected its own token: %v", err)
	}

	// Wrong player, wrong room, wrong token, and a registry that never saw
	// the player at all: every one is the same undifferentiated failure.
	cases := []struct {
		name string
		run  func() error
	}{
		{"wrong player", func() error { return regA.Verify("ROOMAA", "p_bob", tok) }},
		{"wrong room code", func() error { return regA.Verify("ROOMBB", "p_alice", tok) }},
		{"other registry", func() error { return regB.Verify("ROOMBB", "p_alice", tok) }},
		{"wrong token", func() error { return regA.Verify("ROOMAA", "p_alice", ParseToken("guess")) }},
		{"empty token", func() error { return regA.Verify("ROOMAA", "p_alice", Token{}) }},
		{"unknown player", func() error { return regA.Verify("ROOMAA", "p_ghost", tok) }},
	}
	for _, tc := range cases {
		if err := tc.run(); err != ErrBadCredentials {
			t.Errorf("%s: error = %v, want ErrBadCredentials", tc.name, err)
		}
	}
}

func TestIssuedTokensAreUniqueAndUnguessable(t *testing.T) {
	reg := NewRegistry("ROOMAA", rand.Reader)
	seen := map[string]struct{}{}
	for i := 0; i < 500; i++ {
		id := game.PlayerID(fmt.Sprintf("p_%d", i))
		tok, err := reg.Issue(id)
		if err != nil {
			t.Fatal(err)
		}
		s := tok.Reveal()
		if len(s) < 40 {
			t.Fatalf("token %q is only %d chars; 256 bits should be longer", s, len(s))
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate token issued: %q", s)
		}
		seen[s] = struct{}{}
		// A token must not be derivable from the identity it belongs to.
		if strings.Contains(s, string(id)) || strings.Contains(s, "ROOMAA") {
			t.Fatalf("token %q embeds its own identity", s)
		}
	}
}

func TestRegistryIsConcurrencySafe(t *testing.T) {
	reg := NewRegistry("ROOMAA", rand.Reader)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := game.PlayerID(fmt.Sprintf("p_%d", i))
			tok, err := reg.Issue(id)
			if err != nil {
				t.Errorf("issue: %v", err)
				return
			}
			for j := 0; j < 20; j++ {
				if err := reg.Verify("ROOMAA", id, tok); err != nil {
					t.Errorf("verify: %v", err)
					return
				}
				if err := reg.Verify("ROOMAA", id, ParseToken("wrong")); err == nil {
					t.Error("a wrong token verified")
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if reg.Count() != 32 {
		t.Fatalf("registry holds %d credentials, want 32", reg.Count())
	}
}

func TestRoomCodesAvoidAmbiguousCharacters(t *testing.T) {
	for i := 0; i < 400; i++ {
		code, err := NewRoomCode(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != RoomCodeLength {
			t.Fatalf("code %q has length %d", code, len(code))
		}
		if strings.ContainsAny(code, "IO01") {
			t.Fatalf("code %q contains an ambiguous character", code)
		}
	}
}

func TestPlayerIDsAreOpaqueAndUnique(t *testing.T) {
	seen := map[game.PlayerID]struct{}{}
	for i := 0; i < 500; i++ {
		id, err := NewPlayerID(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(id), "p_") {
			t.Fatalf("id %q lacks its prefix", id)
		}
		if len(id) > game.MaxPlayerIDLength {
			t.Fatalf("id %q is longer than the engine accepts", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate player id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestEntropyFailureIsReported(t *testing.T) {
	dead := bytes.NewReader(nil)
	if _, err := NewRoomCode(dead); err != ErrNoEntropy {
		t.Errorf("room code error = %v, want ErrNoEntropy", err)
	}
	if _, err := NewPlayerID(bytes.NewReader(nil)); err != ErrNoEntropy {
		t.Errorf("player id error = %v, want ErrNoEntropy", err)
	}
	reg := NewRegistry("ROOMAA", bytes.NewReader(nil))
	if _, err := reg.Issue("p_1"); err != ErrNoEntropy {
		t.Errorf("issue error = %v, want ErrNoEntropy", err)
	}
}
