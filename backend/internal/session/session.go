// Package session owns identity: room codes, player ids, and the rejoin
// token that proves a reconnecting client is who it says it is.
//
// It contains no gameplay logic and never touches game state.
//
// The token is the credential that protects a player's hidden cards, so the
// type is built to make leaking it hard rather than merely discouraged. Token
// is a struct with an unexported field whose String, GoString and
// MarshalJSON all redact, so a token cannot reach a log line or a JSON
// payload by accident. Getting the secret out requires calling Reveal, which
// happens in exactly one place: the welcome message.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"sync"

	"github.com/kaushlendra/liars-poker/backend/internal/game"
)

var (
	ErrBadCredentials = errors.New("session: bad credentials")
	ErrNoEntropy      = errors.New("session: entropy source failed")
)

// roomCodeAlphabet omits I, O, 0 and 1 so a code read aloud or typed from a
// screenshot is unambiguous.
const roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	// RoomCodeLength is the shareable join handle length.
	RoomCodeLength = 6
	// playerIDBytes of entropy behind each player id.
	playerIDBytes = 9
	// tokenBytes is 256 bits of entropy for the rejoin token.
	tokenBytes = 32
)

// ---------------------------------------------------------------- token

// Token is a rejoin credential. Its zero value is invalid.
type Token struct {
	secret string
}

// String redacts. This is what stops a token reaching a log line through
// %s, %v, or a struct dump.
func (t Token) String() string { return "[redacted]" }

// GoString redacts %#v as well.
func (t Token) GoString() string { return "session.Token{[redacted]}" }

// MarshalJSON redacts, so a Token embedded in any payload cannot serialise
// its secret. The welcome message passes Reveal() explicitly instead.
func (t Token) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// Reveal returns the secret. Call this only where the token is genuinely
// being handed to its owner, which is the welcome message and nowhere else.
func (t Token) Reveal() string { return t.secret }

// IsZero reports an unset token.
func (t Token) IsZero() bool { return t.secret == "" }

// ParseToken wraps a token arriving from a client. It performs no
// validation; only Registry.Verify decides whether it is real.
func ParseToken(s string) Token { return Token{secret: s} }

// ---------------------------------------------------------------- minting

func randomString(src io.Reader, n int, alphabet string) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil {
		return "", ErrNoEntropy
	}
	out := make([]byte, n)
	// Rejection-free modulo bias is not a concern here: the alphabet is 32
	// characters, which divides 256 exactly.
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// NewRoomCode mints a shareable room code.
func NewRoomCode(src io.Reader) (string, error) {
	if src == nil {
		src = rand.Reader
	}
	return randomString(src, RoomCodeLength, roomCodeAlphabet)
}

// NewPlayerID mints an opaque player id. The id is public: knowing it must
// not let anyone act as that player or see their cards.
func NewPlayerID(src io.Reader) (game.PlayerID, error) {
	if src == nil {
		src = rand.Reader
	}
	buf := make([]byte, playerIDBytes)
	if _, err := io.ReadFull(src, buf); err != nil {
		return "", ErrNoEntropy
	}
	return game.PlayerID("p_" + base64.RawURLEncoding.EncodeToString(buf)), nil
}

// newToken mints a rejoin token.
func newToken(src io.Reader) (Token, error) {
	if src == nil {
		src = rand.Reader
	}
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(src, buf); err != nil {
		return Token{}, ErrNoEntropy
	}
	return Token{secret: base64.RawURLEncoding.EncodeToString(buf)}, nil
}

// bind hashes a token together with the room and player it belongs to. Only
// the digest is stored, so the registry holds nothing replayable, and the
// binding means a token minted for one room cannot authenticate in another
// even if both digests were somehow compared.
func bind(roomCode string, id game.PlayerID, t Token) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(roomCode))
	h.Write([]byte{0})
	h.Write([]byte(id))
	h.Write([]byte{0})
	h.Write([]byte(t.secret))
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ---------------------------------------------------------------- registry

// Registry holds the credential digests for one room. It is safe for
// concurrent use.
type Registry struct {
	roomCode string
	entropy  io.Reader

	mu      sync.RWMutex
	digests map[game.PlayerID][sha256.Size]byte
}

// NewRegistry returns a registry scoped to one room. Pass nil entropy for
// crypto/rand.
func NewRegistry(roomCode string, entropy io.Reader) *Registry {
	if entropy == nil {
		entropy = rand.Reader
	}
	return &Registry{
		roomCode: roomCode,
		entropy:  entropy,
		digests:  map[game.PlayerID][sha256.Size]byte{},
	}
}

// Issue mints and records a token for a player, returning it once. The
// registry keeps only the digest, so this is the only moment the secret
// exists outside the client.
func (r *Registry) Issue(id game.PlayerID) (Token, error) {
	t, err := newToken(r.entropy)
	if err != nil {
		return Token{}, err
	}
	digest := bind(r.roomCode, id, t)
	r.mu.Lock()
	r.digests[id] = digest
	r.mu.Unlock()
	return t, nil
}

// Verify checks a presented credential.
//
// It returns a single undifferentiated error for every failure: unknown
// room, unknown player, and wrong token are indistinguishable to the caller,
// so the reply cannot be used to enumerate rooms or players. The comparison
// is constant time, and an unknown player is compared against a zero digest
// rather than short-circuiting, so the timing does not distinguish either.
func (r *Registry) Verify(roomCode string, id game.PlayerID, t Token) error {
	r.mu.RLock()
	stored, known := r.digests[id]
	r.mu.RUnlock()

	presented := bind(roomCode, id, t)
	match := subtle.ConstantTimeCompare(stored[:], presented[:])
	roomOK := subtle.ConstantTimeCompare([]byte(r.roomCode), []byte(roomCode))

	if !known || match != 1 || roomOK != 1 {
		return ErrBadCredentials
	}
	return nil
}

// Known reports whether a player has ever been issued a credential here.
// For internal bookkeeping only; never expose it to a client, since that
// would be the enumeration oracle Verify avoids being.
func (r *Registry) Known(id game.PlayerID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.digests[id]
	return ok
}

// Count is the number of issued credentials.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.digests)
}
