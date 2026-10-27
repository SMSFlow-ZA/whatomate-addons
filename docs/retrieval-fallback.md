# Governed retrieval fallback

`ai.HybridRetriever.Fallback` is trusted host configuration. Empty or `embeddings`
preserves the existing behavior after a successful full-text search with no hits:
query-only native vector retrieval when configured, otherwise embedding reranking
of governed lexical candidates.

`ranked_lexical` explicitly selects the repository's relevance-ranked lexical
candidates after that same empty full-text result. It preserves the first eight
sources in repository order, including their exact source/page provenance. It
does not call an embedder or vector repository. It never returns arbitrary pages,
switches strategy after an error, or uses a model to grant source access.

The PostgreSQL candidate repository bounds the query to 64 distinct non-stopword
terms and 20 matching pages. It rechecks current durable grants, audience, tenant,
effective version, withdrawal, publication approval and extraction provenance.
The answer service still re-retrieves after generation and rejects citations
whose sources are no longer accessible. Query privacy/risk checks, human handoff
and source-bound answer validation remain independent and unchanged.

This mode does not claim semantic search and does not authorize embedding policy
pages through a query-only provider contract. Approved-source embedding requires
its own reviewed indexing and provider contract. Indexing configuration is not
changed by the retrieval fallback choice.
