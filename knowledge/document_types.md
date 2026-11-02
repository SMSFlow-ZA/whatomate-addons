# Immutable document classification

`UploadMetadata` supports optional `document_type` and `title`. Supported types
are `policy` and `form`; a form requires a nonblank title of at most 200
characters. Titles cannot contain line breaks or NUL. Omitted type is interpreted
as policy, never inferred from a filename.

The fields use `omitempty` and are appended after existing canonical fields.
Old upload metadata therefore hashes exactly as before; no stored edition or
manifest is rewritten. New values participate in the existing immutable upload
metadata hash, claim/completion checks and review job. Scanner extraction and
publication evidence are still mandatory. This is metadata only, not an approval
or proof of legal validity.

Standalone form catalogue/download composition verifies the current publication
and matching metadata. Other add-on consumers retain their existing retrieval
behavior. No external transport or worker-image contract change is required.
