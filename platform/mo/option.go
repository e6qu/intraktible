// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mo provides Option[T] and Result[T] — the two small algebraic types
// the codebase uses instead of (value, nil) sentinels and bare booleans where an
// "absent" or "failed" state is easy to mishandle. They make the absent/failed
// case impossible to ignore: you cannot read the value without first asking
// whether it is there. Bridges (Get) interop with idiomatic Go (T, bool)/(T, error)
// at the edges (HTTP handlers, the store interface) so the rest of the code can
// stay ergonomic.
package mo

// Option is a value that may be absent. The zero Option is None — a useful
// default, so a struct field of type Option needs no initialization to be valid.
type Option[T any] struct {
	value   T
	present bool
}

// Some wraps a present value.
func Some[T any](v T) Option[T] { return Option[T]{value: v, present: true} }

// None is the absent value.
func None[T any]() Option[T] { return Option[T]{} }

// OptionOf bridges from idiomatic Go: a (value, ok) pair becomes an Option.
func OptionOf[T any](v T, ok bool) Option[T] {
	if !ok {
		return None[T]()
	}
	return Some(v)
}

// PtrOption turns a nil-able pointer into an Option (nil → None), the standard
// way to lift a *T "maybe" into the type system.
func PtrOption[T any](p *T) Option[T] {
	if p == nil {
		return None[T]()
	}
	return Some(*p)
}

// IsSome reports whether a value is present.
func (o Option[T]) IsSome() bool { return o.present }

// IsNone reports whether the value is absent.
func (o Option[T]) IsNone() bool { return !o.present }

// Get bridges back to idiomatic Go: (value, ok). The value is the zero T when
// absent, so callers must check ok before using it.
func (o Option[T]) Get() (T, bool) { return o.value, o.present }

// OrElse returns the value if present, else the supplied default.
func (o Option[T]) OrElse(def T) T {
	if o.present {
		return o.value
	}
	return def
}

// OrZero returns the value if present, else the zero value of T.
func (o Option[T]) OrZero() T { return o.value }

// Unwrap returns the value, panicking if absent. Use only where presence is a
// proven invariant (after IsSome) — prefer Get/OrElse at boundaries.
func (o Option[T]) Unwrap() T {
	if !o.present {
		panic("mo: Unwrap on None")
	}
	return o.value
}

// Ptr returns a pointer to the value, or nil when absent — the bridge for JSON
// structs that model "absent" as an omitempty pointer field.
func (o Option[T]) Ptr() *T {
	if !o.present {
		return nil
	}
	v := o.value
	return &v
}

// MapOption transforms a present value, leaving None untouched. A free function
// because Go methods cannot introduce new type parameters.
func MapOption[T, U any](o Option[T], f func(T) U) Option[U] {
	if !o.present {
		return None[U]()
	}
	return Some(f(o.value))
}
