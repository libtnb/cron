package cron

import (
	"errors"
	"strings"
	"testing"
)

func TestParseError_Error_FieldAndPos(t *testing.T) {
	e := &ParseError{Spec: "* * *", Field: "minute", Pos: 0, Reason: "out of range"}
	got := e.Error()
	for _, want := range []string{`"* * *"`, `"minute"`, "offset 0", "out of range"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, want substring %q", got, want)
		}
	}
}

func TestParseError_Error_FieldOnly(t *testing.T) {
	e := &ParseError{Spec: "x", Field: "minute", Pos: -1, Reason: "bad"}
	got := e.Error()
	if !strings.Contains(got, `"minute"`) || strings.Contains(got, "offset") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestParseError_Error_PosOnly(t *testing.T) {
	e := &ParseError{Spec: "x", Pos: 3, Reason: "bad"}
	got := e.Error()
	if !strings.Contains(got, "offset 3") || strings.Contains(got, "field") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestParseError_Error_PlainReason(t *testing.T) {
	e := &ParseError{Spec: "x", Pos: -1, Reason: "empty spec"}
	got := e.Error()
	if !strings.Contains(got, "empty spec") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestParseError_Unwrap(t *testing.T) {
	base := errors.New("inner")
	e := &ParseError{Spec: "x", Reason: "boom", Err: base}
	if !errors.Is(e, base) {
		t.Fatal("errors.Is should find wrapped error")
	}
	var pe *ParseError
	if !errors.As(e, &pe) {
		t.Fatal("errors.As should match *ParseError")
	}
	if pe.Reason != "boom" {
		t.Fatalf("Reason = %q", pe.Reason)
	}
}
