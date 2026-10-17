# Contributing to cassette 📼

Thanks for wanting to help! Bug reports, ideas, docs fixes and code are all welcome.

## Found a bug or have an idea?

[Open an issue](https://github.com/TemirlanN-dev/cassette/issues). For bugs, these help a lot:

- the command you ran
- what you expected, and what happened instead
- which AI service and library you were using

**Please never paste API keys,** and check any cassette file before sharing it.

## Setting up

You'll need [Go](https://go.dev/dl/). Then:

```sh
git clone https://github.com/TemirlanN-dev/cassette.git
cd cassette
go test ./...
```

If the tests pass, you're ready. They use a pretend AI service, so they never call a real one or cost anything.

## Where things live

| Folder | What's in it |
|---|---|
| `main.go` | the `record`, `auto` and `replay` commands, and running your test command |
| `internal/proxy/` | the part that sits between the app and the AI: records, replays, or fails |
| `internal/match/` | how cassette decides two requests are "the same" (the `cassette.yaml` rules) |
| `internal/cassette/` | the recording file: what's in it, how it's saved, and keeping keys out of it |

## Making a change

1. Make your change, and add a test for it.
2. Run `gofmt -w .` and `go vet ./...` to tidy up.
3. Run `go test ./...` and make sure everything passes.
4. Open a pull request saying what you changed and why.

A few house rules:

- **Tests never call a real AI.** Use a pretend one (see `newFakeAPI` in `internal/proxy/proxy_test.go`).
- **Keys never reach the cassette file.** If you add support for a new AI service, check how it sends its key and make sure cassette removes it.
- **Keep it simple.** Small, focused pull requests are much easier to review.

## Not sure where to start?

Check the "Coming soon" list in the [README](README.md), or ask in an issue. Happy to help!
