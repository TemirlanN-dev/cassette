<div align="center">

# cassette 📼

**Record your AI calls once. Replay them forever.**

Fast, free, and identical every time, so your tests stop waiting on the AI.

[![tests](https://github.com/TemirlanN-dev/cassette/actions/workflows/test.yml/badge.svg)](https://github.com/TemirlanN-dev/cassette/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Made with Go](https://img.shields.io/badge/made%20with-Go-00ADD8.svg)](https://go.dev)

</div>

---

## The problem

Your app talks to an AI. Your tests do too. And that hurts in three ways:

- 🐢 **It's slow.** Every AI reply takes a few seconds, so your tests take minutes.
- 💸 **It costs money.** You pay for every call, on every run, forever.
- 🎲 **It's random.** The AI words things a little differently each time, so a test passes on Monday and fails on Tuesday, even though you changed nothing.

## The fix

cassette is a tape recorder that sits between your app and the AI.

```
            first run:  your app  ──▶  📼 cassette  ──▶  the AI
                                         │
                                         └──▶ saves the answer to a file

  every run after that:  your app  ──▶  📼 cassette     (the AI is never called)
                                         │
                                         └──▶ plays back the saved answer
```

The first time, cassette passes your question to the real AI and **saves the answer**.
After that, it **plays the saved answer back** instantly: no waiting, no bill, no surprises.

In one of our tests against Google's Gemini:

| | Live AI | Replayed by cassette |
|---|---|---|
| Normal reply | 3.6 seconds | **0.02 seconds** |
| Streamed reply | 3.1 seconds | **0.008 seconds** |
| Cost | a few tokens | **$0** |

## Quick start

**1. Install it.** Download the program for your computer from the [latest release](https://github.com/TemirlanN-dev/cassette/releases/latest), or, if you have [Go](https://go.dev/dl/):

```sh
go install github.com/TemirlanN-dev/cassette@latest
```

**2. Add two scripts** to your `package.json`:

```json
"scripts": {
  "test": "cassette replay -- jest",
  "test:record": "cassette auto --target <your AI service> -- jest"
}
```

Swap `jest` for whatever runs your tests, and `<your AI service>` for its address:

| AI service | Address |
|---|---|
| Anthropic (Claude) | `https://api.anthropic.com` |
| OpenAI (ChatGPT) | `https://api.openai.com` |
| Google (Gemini) | `https://generativelanguage.googleapis.com` |
| Mistral | `https://api.mistral.ai` |
| Anything else | the address its docs tell you to send requests to |

Not using Anthropic or OpenAI? Add one `--env` option too. It takes a minute: see [How does my app find cassette?](#how-does-my-app-find-cassette)

**3. Record once**, with your API key set as usual:

```sh
npm run test:record
```

**4. Then just test like you always do.** No API key, no cost, no waiting:

```sh
npm test
```

That's it. Your recordings live in `cassettes/default.json`. Commit that file, and everyone on your team (and your CI) gets fast, free tests too.

> **Not using npm?** The same idea works with any test command, like `cassette replay -- pytest` or `cassette replay -- go test ./...`.

## How does my app find cassette?

Out of the box, your app doesn't know cassette exists. It sends requests straight to the AI, because that's what its AI library is set up to do:

```
Normally:            your app  ─────────────────────────▶  the AI
                                 (cassette is running, but nobody is talking to it)
```

So something has to tell your app: **"send your requests to cassette instead."**

Think of the AI's address as a **contact saved in your phone**. Your app "calls" that contact whenever it needs the AI.
To put cassette in the middle, you change the number saved in that contact to cassette's. From then on, every call goes to cassette, and cassette passes it on:

```
While testing:       your app  ──▶  📼 cassette  ──▶  the AI
```

In AI libraries, that saved contact is a setting called **base URL**. The good news: cassette changes it for you.

### Using Anthropic or OpenAI? Nothing to do.

cassette points their libraries at itself automatically. The Quick start above is all you need.

### Using anything else? Add one option.

Most AI libraries can also read their base URL from an environment variable. Look in your library's docs for its name (something like `SOME_AI_BASE_URL`), then tell cassette about it with `--env`:

```json
"scripts": {
  "test": "cassette replay --env SOME_AI_BASE_URL=/v1 -- <your test command>",
  "test:record": "cassette auto --target https://api.some-ai.com --env SOME_AI_BASE_URL=/v1 -- <your test command>"
}
```

Both values come straight from your AI's normal address, split in two:

```
https://api.some-ai.com    +    /v1
       --target                 the path for --env
```

That's it: no code changes. Prefer shorter scripts? Put the `--env` part in `cassette.yaml` instead:

```yaml
env:
  SOME_AI_BASE_URL: /v1
```

<details>
<summary><b>My library has no such environment variable</b></summary>

Then set its base URL yourself in your **test setup** (your real app doesn't change), using cassette's address with the same path:

```js
// tests/setup.js
const client = new SomeAIClient({ baseURL: "http://127.0.0.1:4000/v1" });
```

And add `--port 4000` to both scripts, so cassette's address stays the same every run.

</details>

## Three ways to play the tape

| Command | What it does | Use it when |
|---|---|---|
| 🔁 `cassette auto` | Plays back what it knows, records what it doesn't | You're writing or changing tests |
| ▶️ `cassette replay` | **Only** plays back. Anything new is an error | In CI, so nobody gets a surprise bill |
| 🔴 `cassette record` | Throws away the old tape and records everything fresh | Your prompts changed a lot and you want a clean start |

In `replay`, a request that was never recorded fails loudly with a clear message telling you to run `cassette auto`.
It even fails the run if your app quietly caught the error and your tests passed anyway.

## Smart about things that change

Some apps put things in their requests that are different every run, like today's date or a random ID:

```
Monday:   "Today is 2026-10-19. Summarize my emails."
Tuesday:  "Today is 2026-10-20. Summarize my emails."
```

A plain recorder would think those are two different questions and fail.
cassette knows dates, times and random IDs aren't really part of the question, so **both match the same recording**. It works out of the box.

Got something else that changes? Add a `cassette.yaml` next to your tests:

```yaml
# Ignore these fields completely
ignore:
  - metadata.user_id
  - messages.*.id          # the "id" in every message

# Swap changing text for a fixed placeholder before comparing
replace:
  - pattern: 'C:\\Users\\[^\\]+'
    with: '<home folder>'

# Turn off the built-in date / time / ID rules
# default_rules: false
```

Your recordings always keep the original text; these rules only affect how requests are compared.

## Streaming works too

When an AI "types" its answer a few words at a time, cassette records every piece and plays it back the same way, so your app behaves exactly like it does with the real thing.

Replay is instant by default. Want the original typing speed (say, to test a loading animation)?

```sh
cassette replay --realtime -- npm test
```

## Safe to commit

Recordings are made to live in git, so cassette **never saves your API keys**. Keys in headers and in the address are removed before anything is written.
Recordings are plain, readable JSON, so when an AI answer changes you'll see exactly what changed in your pull request.

## Use it in CI

Because `replay` never calls the AI, your CI doesn't need an API key at all. Here's a GitHub Actions example:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version: stable
- run: go install github.com/TemirlanN-dev/cassette@latest
- run: npm test    # runs "cassette replay -- jest" from your package.json
```

If a test sends a request that isn't recorded, the build fails and tells you which one.

## All the options

```
cassette <record | auto | replay> [options] [-- your test command]

  --target URL     the real AI service to record from (needed for record and auto)
  --cassette FILE  where recordings are saved (default: cassettes/default.json)
  --config FILE    your settings file (default: cassette.yaml, optional)
  --env NAME=PATH  also point this environment variable at cassette (repeatable)
  --port NUMBER    which port to listen on (default: a free one when running a command, else 4000)
  --realtime       replay streamed answers at their original speed (auto and replay)
```

No test command? cassette runs on its own until you press Ctrl+C, and you point your app at it yourself.

## Good to know

cassette works with any AI service that answers ordinary web requests. A few things it doesn't handle (yet):

- **Audio and image responses.** It's built for text and JSON answers.
- **Live voice / "realtime" APIs.** These keep a two-way connection open instead of asking and answering.
- **One tape per test.** For now, a whole test run shares one recording file.

## Coming soon

- 🧪 **Jest, Vitest and pytest helpers:** cassette starts by itself when your tests do, with a separate recording for each test
- 🔍 **Diff mode:** change a prompt and see how the AI's behavior changed, step by step
- 💰 **Cost report:** how many tokens and dollars each test would have cost

## Contributing

Ideas, bug reports and pull requests are all welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) to get started.

## License

[MIT](LICENSE). Do whatever you like with it. Press record. 📼
