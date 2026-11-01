# Security Policy

cassette sits between your tests and an AI service, and its recordings are meant to be committed to Git, often in public repositories. Security matters here, and reports are very welcome.

## Supported versions

Only the latest release gets security fixes. If you're on an older version, please update first.

| Version | Supported |
| ------- | --------- |
| 0.2.x   | ✅        |
| < 0.2   | ❌        |

## What counts as a security problem

The biggest risk for cassette is **secrets ending up somewhere public**. Please report anything like:

- an API key, token or account ID being written into a cassette file
- a key or token showing up in cassette's log output (CI logs are often public)
- cassette being reachable from other computers, not just your own (it should only listen on `127.0.0.1`)
- a cassette file that makes cassette crash or misbehave when loaded

If you're not sure whether something counts, report it anyway.

## How to report

**Please don't open a public issue.** Report it privately instead:

1. Go to the [Security tab](https://github.com/TemirlanN-dev/cassette/security) of this repository.
2. Click **Report a vulnerability**.
3. Describe what happened, how to reproduce it, and which version you used (`cassette --version`).

**Never include a real API key** in your report. Use a made-up one like `sk-test-123`. If a real key was exposed, revoke it with your AI provider right away.

## What happens next

- I'll acknowledge your report **within 7 days**.
- I'll keep you updated on whether it's confirmed, and on the fix.
- Once a fix is released, I'll publish an advisory and credit you, unless you'd rather stay anonymous.

cassette is maintained by one person, so I can't promise a fix date, but security reports always come first.
