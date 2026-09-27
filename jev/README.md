# Jev op

Evaluates state using [TypeSafe Jev](https://docs.typesafe.ai/introduction) through
the official `typesafe-sdk==0.7.2` Python SDK. Requires `uv` and Python 3.12+.

```yaml
sequence:
  - id: triage
    op: git+https://github.com/colony-2/c2ops.git//jev@main
    inputs:
      api_key: "${{ secrets.typesafe_api_key }}"
      state:
        ticket: I was charged twice. Please refund the duplicate charge today.
      questions:
        urgent:
          type: noul
          instructions: Is this time-sensitive?
        route:
          type: choice
          instructions: Which team should handle this ticket?
          criteria:
            billing: Payments and refunds
            support: Technical help
        priority:
          type: score
          instructions: How urgently should this be handled?
          criteria: [Can wait, This week, Today]
```

`state` accepts a string, object, or array. `questions` is a nonempty map of
independent questions. The current API types are `noul`, `choice`, and `score`:

- `noul` returns a probability between 0 and 1.
- `choice` returns the selected label, probabilities, and confidence.
- `score` returns a fractional score, rubric legend, probabilities, and confidence.

Instructions and rubric descriptions may contain structured data. Choice criteria
have 1–255 options; score criteria have 2–10 ordered levels. Answers retain the
question names. See the [API reference](https://docs.typesafe.ai/api).

The JSON output envelope contains `model`, `answers`, and `usage` from the SDK,
without converting probabilities to booleans or discarding confidence scores.
`model` defaults to `jev-latest`; use an explicit model version when you need
stable thresholds. `api_key` takes precedence over `TYPESAFE_API_KEY`. Recipes
should pass the key explicitly because extension ops do not inherit the host
environment. `base_url` overrides the API origin for proxies/testing (the SDK adds
`/v1/systemone`). `timeout_seconds` defaults to 60 per HTTP attempt; SDK retry
behavior applies, within the manifest's five-minute process limit.

Validation and API failures exit nonzero without emitting a success envelope.
Error diagnostics omit request bodies and credentials.

```sh
make install-test-deps
make test
```

Tests invoke the real manifest and SDK against a local mock API, covering all
three primitives, model selection, credentials, malformed input, missing answers,
and authentication failure. No live TypeSafe credentials are required.
