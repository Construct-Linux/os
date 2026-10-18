// Package apkver orders apk package versions as apk-tools does, so that what the lock takes as
// Wolfi's newest is what apk and apko take. It is apk-tools' version.c (apk_version_compare and
// its token state machine), ported: Alpine's own Go module tokenizes with apk-tools 2.10's
// tokenizer, which strips a "0" component away and weighs _alpha above _beta, and go-apk-version,
// which Grype uses, strips the zero too (knqyf263/go-apk-version#4) - either orders 1.0_rc1 above
// 1.0, where apk orders it below.
package apkver

import "strings"

// The kinds of token, in the order a version may carry them: a token may be followed only by
// one of its own kind or a later kind, except a digit after a letter.
const (
	initialDigit = iota
	digit
	letter
	suffix
	suffixNo
	commitHash
	revisionNo
	end
	invalid
)

// The suffixes, in their order: a version with one below none is a pre-release, older than the
// version without it; one above is newer.
const (
	alpha = iota
	beta
	pre
	rc
	none
	cvs
	svn
	git
	hg
	p
	suffixInvalid
)

var suffixes = map[string]int{"alpha": alpha, "beta": beta, "pre": pre, "rc": rc, "cvs": cvs, "svn": svn, "git": git, "hg": hg, "p": p}

// token is the tokenizer's state: the kind of the token it is on, its text, and its value as a
// number or a suffix.
type token struct {
	kind   int
	value  string
	number uint64
	suffix int
}

// digits takes the digits at the front of *b as the token's text and number; none is invalid.
func (t *token) digits(b *string) {
	i := 0
	for i < len(*b) && (*b)[i] >= '0' && (*b)[i] <= '9' {
		i++
	}
	t.value, *b = (*b)[:i], (*b)[i:]
	t.number = 0
	for _, c := range t.value {
		t.number = t.number*10 + uint64(c-'0')
	}
	if i == 0 {
		t.kind = invalid
	}
}

// first is the token a version starts with: its first component, digits.
func (t *token) first(b *string) {
	t.kind = initialDigit
	t.digits(b)
}

// next moves to the token after the current one, by what *b starts with, invalid when the
// current one does not allow it.
func (t *token) next(b *string) {
	if *b == "" {
		t.kind = end
		return
	}
	c := (*b)[0]
	switch {
	case c >= 'a' && c <= 'z':
		if t.kind > digit {
			break
		}
		t.value, *b = (*b)[:1], (*b)[1:]
		t.kind = letter
		return
	case c == '.' || (c >= '0' && c <= '9'):
		if c == '.' {
			if t.kind > digit {
				break
			}
			*b = (*b)[1:]
		}
		switch t.kind {
		case initialDigit, digit:
			t.kind = digit
		case suffix:
			t.kind = suffixNo
		default:
			t.kind = invalid
			return
		}
		t.digits(b)
		return
	case c == '_':
		if t.kind > suffixNo {
			break
		}
		*b = (*b)[1:]
		i := 0
		for i < len(*b) && (*b)[i] >= 'a' && (*b)[i] <= 'z' {
			i++
		}
		t.value, *b = (*b)[:i], (*b)[i:]
		s, ok := suffixes[t.value]
		if !ok {
			break
		}
		t.suffix, t.kind = s, suffix
		return
	case c == '~':
		if t.kind >= commitHash {
			break
		}
		*b = (*b)[1:]
		i := 0
		for i < len(*b) && strings.IndexByte("0123456789abcdef", (*b)[i]) >= 0 {
			i++
		}
		if i == 0 {
			break
		}
		t.value, *b = (*b)[:i], (*b)[i:]
		t.kind = commitHash
		return
	case c == '-':
		if t.kind >= revisionNo || !strings.HasPrefix(*b, "-r") {
			break
		}
		*b = (*b)[2:]
		t.kind = revisionNo
		t.digits(b)
		return
	}
	t.kind = invalid
}

// cmp compares two tokens of the same kind.
func (a *token) cmp(b *token) int {
	var x, y uint64
	switch a.kind {
	case digit:
		if a.value[0] == '0' || b.value[0] == '0' {
			// A component with a leading zero is ordered as text, as Gentoo orders it.
			return strings.Compare(a.value, b.value)
		}
		x, y = a.number, b.number
	case initialDigit, suffixNo, revisionNo:
		x, y = a.number, b.number
	case letter:
		x, y = uint64(a.value[0]), uint64(b.value[0])
	case suffix:
		x, y = uint64(a.suffix), uint64(b.suffix)
	default:
		return strings.Compare(a.value, b.value)
	}
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// Compare is -1, 0 or 1 as a is older than, the same as, or newer than b.
func Compare(a, b string) int {
	var ta, tb token
	ta.first(&a)
	tb.first(&b)
	for ta.kind == tb.kind && ta.kind < end {
		if r := ta.cmp(&tb); r != 0 {
			return r
		}
		ta.next(&a)
		tb.next(&b)
	}
	if ta.kind == tb.kind {
		return 0
	}
	// The same so far, and one goes on: the longer is newer, unless what it goes on with is a
	// pre-release suffix.
	if ta.kind == suffix && ta.suffix < none {
		return -1
	}
	if tb.kind == suffix && tb.suffix < none {
		return 1
	}
	if ta.kind > tb.kind {
		return -1
	}
	return 1
}
