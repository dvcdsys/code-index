// Package tokenizer carries the model-token knowledge the chunker needs and
// the embedding providers own.
//
// The chunker decides WHERE to cut; only the provider knows WHAT the model
// counts. Keeping the two apart is what lets a model change without teaching
// the chunker about byte-level BPE, SentencePiece, or llama-server's
// /tokenize endpoint.
package tokenizer

// Budget is the whole surface the chunker sees. One interface rather than a
// mandatory one plus an optional splitter: a single constructor argument, and
// callers that need both never have to type-assert.
//
// Cost note, because the two methods look interchangeable and are not:
// CountTokens and SplitPoints do the same single left-to-right pass over the
// same memo, so neither is algorithmically cheaper. What differs is
// allocation. CountTokens returns an int and allocates nothing; SplitPoints
// must build a slice of offsets — for a 60 KB input that is on the order of
// 15k entries. CountTokens runs on every chunk (1.9M of them on the reference
// corpus) while SplitPoints runs only on inputs that exceed the model's
// context (5 of that same 1.9M). Call CountTokens on the hot path and reach
// for SplitPoints only once a text is known not to fit.
//
// A third shortcut avoids both: byte-level BPE cannot emit a token covering
// less than one byte, so len(text) <= budget PROVES the text fits, with no
// tokenisation at all. Use it before calling anything here.
type Budget interface {
	// MaxInputTokens is the model's context window for a single input.
	MaxInputTokens() int

	// ExactCounts reports whether CountTokens and SplitPoints are exact.
	// False means the provider has no tokenizer and is estimating from
	// byte length: counts may be wrong in both directions and split points
	// are byte windows, not token boundaries. Callers that need a
	// guarantee must widen their safety margin when this is false —
	// silently trusting an estimate is what the byte-window splitter used
	// to do, and it produced averaged vectors nobody could see was wrong.
	ExactCounts() bool

	// CountTokens returns the number of tokens the model will charge for.
	CountTokens(s string) int

	// SplitPoints returns byte offsets at which s must be cut so no piece
	// exceeds budget tokens, plus the total token count of s. Offsets, not
	// substrings, so the caller keeps ownership of the metadata that hangs
	// off those positions — line numbers, symbol names, byte ranges.
	// A nil offsets slice means s already fits.
	SplitPoints(s string, budget int) (offsets []int, total int)
}
