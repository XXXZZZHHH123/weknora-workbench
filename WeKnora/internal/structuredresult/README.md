# Structured result contract v2

`result_contract` is additive. Existing `columns`, `rows`, SQL and original
source metadata retain their meanings. `ValidatePayload` returns nil for a
legacy payload without a contract and rejects a present malformed/unsupported
contract. A failure must not fall back to legacy evidence. Source authorization
and current source version checks are still required by the caller.

The contract binds a result identity/hash, exact source snapshot and input
digest, execution/numeric semantics, output columns, unit evidence and explicit
capabilities. Its fixed `hash_input_keys` projection includes actual values,
coordinates, SQL, sources, input/output coverage, versions and limitations.
Transport additions (`kind`, `source_kind`, `source_context`, `result_status`,
`pdf_evidence_contract`, projection navigation) are outside the digest. A supplied
source context must agree with a bound knowledge-base identity. Budgeted model
views must retain the original Data payload for validation; they must not attach
the full result contract to changed rows as though it described that projection.

## Digest encoding

`sha256-typed-json-v1` hashes a typed UTF-8 stream, not JSON serialization bytes:

* null: `n;`; false/true: `b0;`/`b1;`.
* string: `s` + UTF-8 byte length in decimal + `:` + the UTF-8 bytes.
* number: `d` + normalized decimal byte length + `:` + decimal bytes. Expand
  exponents, remove insignificant zeros, normalize negative zero to `0`.
  A numeric string remains a string, never a number.
* array: `a` + item count + `:` + each encoded item in order.
* object: `o` + property count + `:` + each string key/value, keys sorted by
  Unicode scalar/UTF-8 order. JSON strings must be valid Unicode.

`result_hash` hashes `{payload: <present fixed keys>, contract: <all contract
properties except result_hash>}`. The input key list is itself bound and must
equal the present fixed keys. Preserve JSON numbers with `UseNumber` at the
first HTTP decoder; restoring a rounded float64 cannot recover evidence.
The independent Python/Go golden vector covers Unicode, a greater-than-2^53
integer, decimals, negative zero, null, booleans and nested values.

## Capabilities and units

Free SQL always has `level=dependency_only`, `cell_lineage_verified=false` and
`aggregate_membership_verified=false`. Column dependencies do not identify
which input cells/rows contributed to an individual output. Input coverage is
the loaded scope before SQL, not aggregate/window membership. Raw PDF/Excel
reads can have `level=source_cells` because their existing coordinates are
available; this does not certify OCR or recalculate cached Excel formulas.

Units are `unknown`, `mixed`, `declared`, or `derived`. Unknown/mixed units have
no symbol. Reviewed PDF plan declarations and imported series metadata remain
separate evidence levels; header coordinates or same-page text do not establish
an independently verified unit annotation. Declaration evidence binds the source
SHA, logical column/series and plan when applicable. Derivations retain this
evidence and identify a supported mechanical operation, not business additivity.

Supported rules cover scalar projections, numeric casts, same-unit arithmetic,
SUM/AVG/MIN/MAX, COUNT, constant scaling, explicit UNION alternatives and a
single-value-column UNPIVOT. Unsupported AST shapes/collection element rules
retain unknown units and do not change SQL execution. A ratio of same explicit
units is dimensionless `1`; multiplying by 100 never silently labels it `%`.
There is no automatic currency, quantity, percent or percentage-point conversion.
Literal labels do not inherit the Y unit or become sourced observations. SQL
predicates over mixed-unit loaded series are not unit binding proofs.

Exact SQL cell/branch/group/window lineage and original unit annotation binding
remain later capabilities. This version neither rewrites SQL nor inserts hidden
columns into DISTINCT/order/limit/window expressions.
