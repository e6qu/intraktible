// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"fmt"

	"go.starlark.net/syntax"
)

// rewriteGuards routes every unbounded-growth op in a parsed Code script through
// the guarded builtins in code_limits.go, by replacing the syntax that reaches
// them: `x * y` and `x *= y` become _intraktible_mul(x, y), and `recv.join(…)` /
// `recv.replace(…)` become _intraktible_join(recv, …) / _intraktible_replace(recv,
// …). (`range` needs no rewrite — a predeclared name shadows the universe.)
//
// Operators are not values in Starlark, so this is the only interception point:
// there is no hook on Binary, and the guarded methods live on starlark.String,
// which we do not own.
func rewriteGuards(f *syntax.File) error {
	r := &guardRewriter{}
	r.stmts(f.Stmts)
	return r.err
}

type guardRewriter struct{ err error }

func (r *guardRewriter) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

func (r *guardRewriter) stmts(list []syntax.Stmt) {
	for _, s := range list {
		r.stmt(s)
	}
}

func (r *guardRewriter) stmt(s syntax.Stmt) {
	switch x := s.(type) {
	case *syntax.AssignStmt:
		r.assign(x)
	case *syntax.DefStmt:
		r.exprs(x.Params)
		r.stmts(x.Body)
	case *syntax.ExprStmt:
		x.X = r.expr(x.X)
	case *syntax.ForStmt:
		x.Vars = r.expr(x.Vars)
		x.X = r.expr(x.X)
		r.stmts(x.Body)
	case *syntax.IfStmt:
		x.Cond = r.expr(x.Cond)
		r.stmts(x.True)
		r.stmts(x.False)
	case *syntax.ReturnStmt:
		if x.Result != nil {
			x.Result = r.expr(x.Result)
		}
	case *syntax.WhileStmt:
		x.Cond = r.expr(x.Cond)
		r.stmts(x.Body)
	case *syntax.BranchStmt, *syntax.LoadStmt:
		// No sub-expressions. (load is disabled for Code nodes anyway.)
	default:
		r.fail("decision-engine: unsupported statement %T in a code node", s)
	}
}

// assign desugars `x *= y` into `x = mul(x, y)`. Only a plain variable may be
// multiplied in place: rewriting `a[f()] *= n` would evaluate the subscript twice,
// so it is refused rather than silently changing the script's meaning.
func (r *guardRewriter) assign(x *syntax.AssignStmt) {
	x.LHS = r.expr(x.LHS)
	x.RHS = r.expr(x.RHS)
	if x.Op != syntax.STAR_EQ {
		return
	}
	target, ok := x.LHS.(*syntax.Ident)
	if !ok {
		r.fail("decision-engine: `*=` in a code node is only supported on a plain variable")
		return
	}
	// The binding on an Ident is resolved once, so the load position gets a copy
	// rather than sharing the store position's node.
	load := &syntax.Ident{NamePos: target.NamePos, Name: target.Name}
	x.Op = syntax.EQ
	x.RHS = call(guardMul, target.NamePos, load, x.RHS)
}

func (r *guardRewriter) exprs(list []syntax.Expr) {
	for i, e := range list {
		list[i] = r.expr(e)
	}
}

func (r *guardRewriter) expr(e syntax.Expr) syntax.Expr {
	switch x := e.(type) {
	case nil:
		return nil
	case *syntax.BinaryExpr:
		x.X = r.expr(x.X)
		x.Y = r.expr(x.Y)
		if x.Op == syntax.STAR {
			return call(guardMul, x.OpPos, x.X, x.Y)
		}
		return x
	case *syntax.CallExpr:
		return r.callExpr(x)
	case *syntax.Comprehension:
		x.Body = r.expr(x.Body)
		for _, clause := range x.Clauses {
			switch c := clause.(type) {
			case *syntax.ForClause:
				c.Vars = r.expr(c.Vars)
				c.X = r.expr(c.X)
			case *syntax.IfClause:
				c.Cond = r.expr(c.Cond)
			}
		}
		return x
	case *syntax.CondExpr:
		x.Cond, x.True, x.False = r.expr(x.Cond), r.expr(x.True), r.expr(x.False)
		return x
	case *syntax.DictEntry:
		x.Key, x.Value = r.expr(x.Key), r.expr(x.Value)
		return x
	case *syntax.DictExpr:
		r.exprs(x.List)
		return x
	case *syntax.DotExpr:
		x.X = r.expr(x.X)
		// A guarded method must be called in place; handing it around as a value
		// (`f = s.join`) would escape the guard.
		if _, guarded := guardedMethods[x.Name.Name]; guarded {
			r.fail("decision-engine: `.%s` in a code node must be called directly, not referenced", x.Name.Name)
		}
		return x
	case *syntax.Ident:
		if len(x.Name) >= len(guardPrefix) && x.Name[:len(guardPrefix)] == guardPrefix {
			r.fail("decision-engine: %q is reserved in a code node", x.Name)
		}
		return x
	case *syntax.IndexExpr:
		x.X, x.Y = r.expr(x.X), r.expr(x.Y)
		return x
	case *syntax.LambdaExpr:
		r.exprs(x.Params)
		x.Body = r.expr(x.Body)
		return x
	case *syntax.ListExpr:
		r.exprs(x.List)
		return x
	case *syntax.Literal:
		return x
	case *syntax.ParenExpr:
		x.X = r.expr(x.X)
		return x
	case *syntax.SliceExpr:
		x.X, x.Lo, x.Hi, x.Step = r.expr(x.X), r.expr(x.Lo), r.expr(x.Hi), r.expr(x.Step)
		return x
	case *syntax.TupleExpr:
		r.exprs(x.List)
		return x
	case *syntax.UnaryExpr:
		x.X = r.expr(x.X) // nil for the `*args` form
		return x
	default:
		r.fail("decision-engine: unsupported expression %T in a code node", e)
		return e
	}
}

// callExpr rewrites `recv.join(sep)` into `_intraktible_join(recv, sep)`. The
// receiver becomes the first argument, so the guard can size it before the real
// method runs.
func (r *guardRewriter) callExpr(x *syntax.CallExpr) syntax.Expr {
	dot, isMethod := x.Fn.(*syntax.DotExpr)
	guard, guarded := "", false
	if isMethod {
		guard, guarded = guardedMethods[dot.Name.Name]
	}
	if !guarded {
		x.Fn = r.expr(x.Fn)
		r.exprs(x.Args)
		return x
	}
	recv := r.expr(dot.X)
	r.exprs(x.Args)
	for _, arg := range x.Args {
		// `s.join(*xs)` and `s.join(k=v)` would land in the guard's positional
		// slots misaligned, so they are refused rather than mis-sized.
		if unary, ok := arg.(*syntax.UnaryExpr); ok && (unary.Op == syntax.STAR || unary.Op == syntax.STARSTAR) {
			r.fail("decision-engine: `.%s` in a code node does not accept argument unpacking", dot.Name.Name)
			return x
		}
		if binary, ok := arg.(*syntax.BinaryExpr); ok && binary.Op == syntax.EQ {
			r.fail("decision-engine: `.%s` in a code node does not accept keyword arguments", dot.Name.Name)
			return x
		}
	}
	return call(guard, dot.Dot, append([]syntax.Expr{recv}, x.Args...)...)
}

// call builds `name(args...)`. The positions are the rewritten op's, so a runtime
// error still points at the source the author wrote.
func call(name string, pos syntax.Position, args ...syntax.Expr) *syntax.CallExpr {
	return &syntax.CallExpr{
		Fn:     &syntax.Ident{NamePos: pos, Name: name},
		Lparen: pos,
		Args:   args,
		Rparen: pos,
	}
}
