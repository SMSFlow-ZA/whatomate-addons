# Source revision and recovery generation

`governance.Record.SourceRevision` is an optional, immutable version supplied by
the physical source owner. It identifies the source revision covered by the
inventory and approval. `Generation` identifies governance execution and recovery
attempts. Restoring a database advances the latter, not the former.

A nonzero source revision participates in `RecordFingerprint`. Discovery through
ordinary, coordinated, delegated and review paths rejects a changed revision
instead of silently replacing an approved binding. Sources not using this
contract retain zero; the omitted JSON field preserves their prior fingerprints.

An adapter that requires the binding must capture it from its own data and check
exact equality at export and erasure, alongside current authorization and holds.
It must not infer a source version by subtracting from a recovery generation.

Legacy unbound records are not automatically migrated or rebound. A source that
now requires the binding must deny their use until a separately reviewed migration
establishes the original source revision and renewed authority. This change does
not supply that migration or authorize deployment over affected legacy data.

Tests cover fingerprint binding, immutable registration/discovery and distinct
logical/physical recovery generations against PostgreSQL. A synthetic boundary
adapter verifies governance behavior; each integrating host must separately test
its actual source adapter and restored data.
