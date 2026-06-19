// SPDX-License-Identifier: AGPL-3.0-or-later

package mo_test

import (
	"errors"
	"testing"

	"github.com/e6qu/intraktible/platform/mo"
)

func TestOption(t *testing.T) {
	some := mo.Some(42)
	if !some.IsSome() || some.IsNone() {
		t.Fatal("Some should be present")
	}
	if v, ok := some.Get(); !ok || v != 42 {
		t.Fatalf("Get = (%d,%v)", v, ok)
	}
	if some.Unwrap() != 42 || some.OrElse(0) != 42 {
		t.Fatal("Some accessors")
	}

	none := mo.None[int]()
	if none.IsSome() || !none.IsNone() {
		t.Fatal("None should be absent")
	}
	if v, ok := none.Get(); ok || v != 0 {
		t.Fatalf("None Get = (%d,%v)", v, ok)
	}
	if none.OrElse(7) != 7 || none.OrZero() != 0 {
		t.Fatal("None defaults")
	}
}

func TestOptionZeroValueIsNone(t *testing.T) {
	var o mo.Option[string]
	if o.IsSome() {
		t.Fatal("zero Option must be None")
	}
}

func TestOptionUnwrapPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Unwrap on None should panic")
		}
	}()
	_ = mo.None[int]().Unwrap()
}

func TestOptionBridges(t *testing.T) {
	if mo.OptionOf("x", true).IsNone() || mo.OptionOf("x", false).IsSome() {
		t.Fatal("OptionOf")
	}
	n := 5
	if mo.PtrOption(&n).Unwrap() != 5 || mo.PtrOption[int](nil).IsSome() {
		t.Fatal("PtrOption")
	}
	if p := mo.Some(9).Ptr(); p == nil || *p != 9 {
		t.Fatal("Ptr(Some)")
	}
	if mo.None[int]().Ptr() != nil {
		t.Fatal("Ptr(None) should be nil")
	}
}

func TestMapOption(t *testing.T) {
	double := func(n int) int { return n * 2 }
	if mo.MapOption(mo.Some(3), double).Unwrap() != 6 {
		t.Fatal("MapOption(Some)")
	}
	if mo.MapOption(mo.None[int](), double).IsSome() {
		t.Fatal("MapOption(None) should stay None")
	}
}

func TestResult(t *testing.T) {
	ok := mo.Ok(42)
	if !ok.IsOk() || ok.IsErr() {
		t.Fatal("Ok should be ok")
	}
	if v, err := ok.Get(); err != nil || v != 42 {
		t.Fatalf("Get = (%d,%v)", v, err)
	}
	if ok.Unwrap() != 42 || ok.Error() != nil {
		t.Fatal("Ok accessors")
	}

	boom := errors.New("boom")
	bad := mo.Err[int](boom)
	if bad.IsOk() || !bad.IsErr() {
		t.Fatal("Err should be err")
	}
	if !errors.Is(bad.Error(), boom) {
		t.Fatal("Err should carry the error")
	}
	if bad.OrElse(7) != 7 {
		t.Fatal("Err OrElse")
	}
}

func TestErrNilBecomesSentinel(t *testing.T) {
	r := mo.Err[int](nil)
	if r.IsOk() || r.Error() == nil {
		t.Fatal("Err(nil) must still be an error")
	}
}

func TestResultBridge(t *testing.T) {
	if mo.ResultOf("x", nil).IsErr() {
		t.Fatal("ResultOf(_, nil) should be Ok")
	}
	if mo.ResultOf("", errors.New("e")).IsOk() {
		t.Fatal("ResultOf(_, err) should be Err")
	}
}

func TestMapAndThen(t *testing.T) {
	half := func(n int) mo.Result[int] {
		if n%2 != 0 {
			return mo.Err[int](errors.New("odd"))
		}
		return mo.Ok(n / 2)
	}
	if mo.MapResult(mo.Ok(4), func(n int) int { return n + 1 }).Unwrap() != 5 {
		t.Fatal("MapResult(Ok)")
	}
	if mo.MapResult(mo.Err[int](errors.New("e")), func(n int) int { return n }).IsOk() {
		t.Fatal("MapResult(Err) should stay Err")
	}
	if mo.AndThen(mo.Ok(8), half).Unwrap() != 4 {
		t.Fatal("AndThen(Ok even)")
	}
	if mo.AndThen(mo.Ok(3), half).IsOk() {
		t.Fatal("AndThen(Ok odd) should be Err")
	}
	if mo.AndThen(mo.Err[int](errors.New("e")), half).IsOk() {
		t.Fatal("AndThen(Err) should stay Err")
	}
}

func TestResultUnwrapPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Unwrap on Err should panic")
		}
	}()
	_ = mo.Err[int](errors.New("boom")).Unwrap()
}
