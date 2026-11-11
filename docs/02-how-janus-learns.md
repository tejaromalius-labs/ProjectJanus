# 2. How Janus learns

**What you'll learn:** the one formula at the heart of Janus, what each piece means,
why the update is cheap enough to run on every request, and why a brand-new pod
needs special care.

You don't need to be a mathematician. Read the plain-words version first; the
formulas follow for anyone who wants them.

---

## The idea in plain words

For every pod, Janus keeps two things:

1. **A guess** at how well this pod will do in the current situation.
2. **A measure of how unsure it is about that guess.**

The pod's **score** is the guess plus a little "benefit of the doubt" proportional to
the unsureness. A pod we know little about gets a small bonus, so it still gets
tried. A pod we know well gets almost no bonus, so the score is just our best guess.

That's the whole exploration-versus-exploitation balance, in one number.

## The score formula

```
score = xᵀθ  +  α · √(xᵀ A⁻¹ x)
        ────     ──────────────
       exploit      explore
```

| Symbol | Plain meaning |
| --- | --- |
| `x` | The **context**: 12 numbers describing the situation (next page) |
| `θ` ("theta") | What the model has learned: how each context number affects the reward |
| `xᵀθ` | The **predicted reward** for this pod right now |
| `A⁻¹` | A matrix that records how much data we have seen, in each direction of context |
| `√(xᵀ A⁻¹ x)` | **How unsure** the model is for this context. Big when we've seen little, small when we've seen a lot |
| `α` ("alpha") | How much benefit of the doubt to give. Default `0.15` |

The `−1` in `A⁻¹` means "inverse". Janus never computes an inverse from scratch (that
is slow); it *maintains* it, which is the next trick.

## The four things stored per pod

| Name | Shape | What it is |
| --- | --- | --- |
| `A` | 12×12 | Running total of "what contexts have we seen?" (starts as the identity matrix) |
| `A⁻¹` | 12×12 | Its inverse. **This is what Janus actually stores** |
| `b` | 12 numbers | Running total of "reward × context" |
| `θ = A⁻¹ b` | 12 numbers | The learned weights. Computed when needed, never stored (it would go stale the moment `A⁻¹` or `b` changes) |

Here is the full set of definitions, for reference:

```
A = I + Σ xᵢ xᵢᵀ          (starts at the identity; grows with each observation)
b = Σ rᵢ · xᵢ              (reward-weighted contexts)
θ = A⁻¹ b                  (ridge-regression estimate)
```

## The update: cheap on purpose

After every request we get one new data point: a context `x` and a reward `r`. The
obvious update, add `xxᵀ` to `A` and invert it again, costs about 1,700 operations at
12 features. Doing that on every request would eat the routing budget.

**Sherman-Morrison** is a classic shortcut. It lets you update the inverse directly
from the old inverse:

```
(A + xxᵀ)⁻¹ = A⁻¹ − (A⁻¹x)(A⁻¹x)ᵀ / (1 + xᵀA⁻¹x)
```

That costs about 290 operations (two matrix-by-vector steps), roughly six times
fewer, and it grows with the *square* of the number of features instead of the cube.
Here it is in the code (`linucb-core/src/arm.rs`):

```rust
let v = &self.a_inv * x;          // v = A⁻¹x
let denom = 1.0 + x.dot(&v);      // 1 + xᵀA⁻¹x
let a_inv_new = &self.a_inv - (&v * v.transpose()) / denom;
let b_new = &self.b + reward * x;
```

Two details matter:

- **Start from the identity, never zeros.** A zero matrix can't be inverted. The
  identity says "I know nothing yet, and every direction is equally uncertain", a
  sensible starting prior.
- **The denominator is always above 1** (for sensible inputs), and the code
  guards against the rare float-drift case where it isn't.

You'll see the *discounted* version of this update on [page 6](06-adapting-to-change.md).
It adds a forgetting factor so old data fades.

## A new pod needs special treatment

Here's a subtle trap. A fresh pod starts with `b = 0`, so its predicted reward is
zero. After one observation it still looks worse than a busy pod with a long, good
record, and the small exploration bonus (`α = 0.15`) isn't enough to close the gap.
By the score alone, the new pod would **never** get chosen.

So Janus adds a rule on top: **any pod with fewer than 20 observations is routed to
first** (least-observed first, ties broken at random). After about 20 requests its
score reflects reality and it competes like everyone else.

## Does it converge?

Under the model's assumptions, LinUCB has *sublinear regret*, meaning that the gap
between its total reward and the best possible choice grows more slowly than the
number of requests. In early demos with two backends, equal backends settled to
about 50/50, and with one backend delayed by 200 ms, traffic moved to over 99% on
the fast one in under five seconds. Those were small demos; the real evidence is the
[benchmark](12-testing-and-benchmarking.md).

## Keeping it fast: a hard cap on features

The update cost grows with the square of the feature count (12 features is cheap;
50 is not). So the code refuses to compile with more than 20 features:

```rust
pub const D: usize = 12;
pub const MAX_D: usize = 20;
const _: () = assert!(D <= MAX_D, "feature dimension D exceeds the hard cap MAX_D");
```

That keeps the 1.5 ms routing budget from being broken by accident.

---

[← Previous: The big picture](01-the-big-picture.md) · [Documentation index](README.md) · [Next: What Janus measures →](03-what-janus-measures.md)
