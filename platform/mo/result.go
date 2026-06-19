// SPDX-License-Identifier: AGPL-3.0-or-later

package mo

// Result is either a value (Ok) or an error (Err). It carries the same
// information as the idiomatic (T, error) pair, but as a single value that can be
// returned through Option/Result combinators and that cannot be read as a value
// without first being checked. The zero Result is an Err with a nil error, which
// is invalid — always construct with Ok or Err.
type Result[T any] struct {
	value T
	err   error
}

// Ok wraps a success value.
func Ok[T any](v T) Result[T] { return Result[T]{value: v} }

// Err wraps a failure. A nil error is treated as a failure with no detail
// (callers should pass a real error); use Ok for success.
func Err[T any](e error) Result[T] {
	if e == nil {
		e = errNilResult
	}
	return Result[T]{err: e}
}

// ResultOf bridges from idiomatic Go: a (value, err) pair becomes a Result.
func ResultOf[T any](v T, err error) Result[T] {
	if err != nil {
		return Err[T](err)
	}
	return Ok(v)
}

// IsOk reports whether the Result holds a value.
func (r Result[T]) IsOk() bool { return r.err == nil }

// IsErr reports whether the Result holds an error.
func (r Result[T]) IsErr() bool { return r.err != nil }

// Get bridges back to idiomatic Go: (value, err). The value is the zero T on
// error.
func (r Result[T]) Get() (T, error) { return r.value, r.err }

// Err returns the error, or nil when Ok.
func (r Result[T]) Error() error { return r.err }

// Unwrap returns the value, panicking on error. Use only where success is a
// proven invariant (after IsOk) — prefer Get at boundaries.
func (r Result[T]) Unwrap() T {
	if r.err != nil {
		panic("mo: Unwrap on Err: " + r.err.Error())
	}
	return r.value
}

// OrElse returns the value if Ok, else the supplied default.
func (r Result[T]) OrElse(def T) T {
	if r.err != nil {
		return def
	}
	return r.value
}

// MapResult transforms an Ok value, propagating an Err unchanged. A free function
// because Go methods cannot introduce new type parameters.
func MapResult[T, U any](r Result[T], f func(T) U) Result[U] {
	if r.err != nil {
		return Err[U](r.err)
	}
	return Ok(f(r.value))
}

// AndThen chains a fallible step onto an Ok value (monadic bind), propagating an
// Err unchanged.
func AndThen[T, U any](r Result[T], f func(T) Result[U]) Result[U] {
	if r.err != nil {
		return Err[U](r.err)
	}
	return f(r.value)
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const errNilResult sentinelErr = "mo: Err constructed with a nil error"
