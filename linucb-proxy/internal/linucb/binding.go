// Package linucb wraps the Rust linucb-core engine via CGO.
//
// The Rust crate is built as a static library (liblinucb_core.a) exposing a C
// ABI; see linucb-core/include/linucb.h. Build the Rust side first:
//
//	(cd ../../linucb-core && cargo build --release)
//
// then build/test this package with CGO enabled (the default).
package linucb

/*
#cgo CFLAGS: -I${SRCDIR}/../../../linucb-core/include
// Link the Rust core statically by naming the archive directly. Pointing at the
// .a (rather than -llinucb_core) avoids the linker preferring the sibling .so,
// which would otherwise require the shared object on the runtime loader path.
#cgo LDFLAGS: ${SRCDIR}/../../../linucb-core/target/release/liblinucb_core.a -ldl -lpthread -lm
#include <stdlib.h>
#include "linucb.h"
*/
import "C"

import (
	"errors"
	"math"
	"runtime"
	"unsafe"
)

// D is the feature dimension, mirrored from the Rust core.
const D = int(C.D)

// ErrEngineNil is returned when the engine failed to construct.
var ErrEngineNil = errors.New("linucb: engine construction failed (invalid dim/alpha)")

// ErrUpdate is returned when an update is rejected by the core.
var ErrUpdate = errors.New("linucb: update rejected (bad arm, dimension, or reward)")

// Engine is a handle to the Rust LinUCB engine. Safe for concurrent scoring;
// callers must serialize Update calls per arm (the design runs one worker per
// arm).
type Engine struct {
	ptr *C.Engine
}

// NewEngine creates an engine with no forgetting (gamma=1.0).
// The returned engine must be released with Close.
func NewEngine(dim int, alpha float64) (*Engine, error) {
	return NewEngineWithGamma(dim, alpha, 1.0)
}

// NewEngineWithGamma creates an engine with a forgetting factor gamma ∈ (0,1].
// gamma=1.0 is identical to NewEngine. gamma=0.999 gives an effective memory
// window of ~1000 observations, allowing the model to adapt when backends change.
func NewEngineWithGamma(dim int, alpha float64, gamma float64) (*Engine, error) {
	p := C.linucb_engine_new_with_gamma(C.size_t(dim), C.double(alpha), C.double(gamma))
	if p == nil {
		return nil, ErrEngineNil
	}
	e := &Engine{ptr: p}
	runtime.SetFinalizer(e, func(e *Engine) { e.Close() })
	return e, nil
}

// Close frees the underlying Rust engine. Idempotent.
func (e *Engine) Close() {
	if e.ptr != nil {
		C.linucb_engine_free(e.ptr)
		e.ptr = nil
		runtime.SetFinalizer(e, nil)
	}
}

// AddArm registers a pod as a new arm and returns its arm ID, or an error.
func (e *Engine) AddArm(podID string) (int, error) {
	cstr := C.CString(podID)
	defer C.free(unsafe.Pointer(cstr))
	id := C.linucb_add_arm(e.ptr, cstr)
	if int(id) < 0 {
		return 0, errors.New("linucb: add_arm failed")
	}
	return int(id), nil
}

// ResetArm clears an arm's learned state back to cold-start, reclaiming its
// model memory (the d×d A_inv matrix + b vector). The arm ID stays valid — the
// underlying slot is reset in place, not removed, so no other arm's index
// shifts. Used when a pod is removed. Caller must serialize ResetArm against
// Update calls to the same arm (see registry.RemovePod for the best-effort note).
func (e *Engine) ResetArm(armID int, podID string) error {
	cstr := C.CString(podID)
	defer C.free(unsafe.Pointer(cstr))
	rc := C.linucb_reset_arm(e.ptr, C.int(armID), cstr)
	if int(rc) != 0 {
		return errors.New("linucb: reset_arm failed")
	}
	return nil
}

// Score returns the UCB score for an arm against context x. x must have length
// D. Returns NaN-derived error on dimension mismatch or bad arm.
func (e *Engine) Score(armID int, x []float64) (float64, error) {
	if len(x) == 0 {
		return 0, errors.New("linucb: empty context")
	}
	// Footgun guard: &x[0] is only valid for non-empty slices.
	score := C.linucb_score(
		e.ptr,
		C.int(armID),
		(*C.double)(unsafe.Pointer(&x[0])),
		C.size_t(len(x)),
	)
	f := float64(score)
	if math.IsNaN(f) {
		return 0, errors.New("linucb: score failed (bad arm or dimension)")
	}
	return f, nil
}

// Update applies a reward observation to an arm (Sherman-Morrison, O(d²)).
// Caller must serialize Update calls to the same arm.
func (e *Engine) Update(armID int, x []float64, reward float64) error {
	if len(x) == 0 {
		return errors.New("linucb: empty context")
	}
	rc := C.linucb_update(
		e.ptr,
		C.int(armID),
		(*C.double)(unsafe.Pointer(&x[0])),
		C.size_t(len(x)),
		C.double(reward),
	)
	if int(rc) != 0 {
		return ErrUpdate
	}
	return nil
}

// NObs returns the observation count for an arm.
func (e *Engine) NObs(armID int) uint64 {
	return uint64(C.linucb_n_obs(e.ptr, C.int(armID)))
}
