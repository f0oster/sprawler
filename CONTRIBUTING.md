# Contributing

Thanks for your interest in contributing to Sprawler.

## Getting started

1. Fork the repository and clone your fork
2. Install Go 1.23+
3. Copy `.env.example` to `.env.local` and configure your environment
4. Build: `make build`
5. Run tests: `make test`

## Development workflow

The Makefile provides all common tasks:

| Command | What it does |
|---|---|
| `make build` | Build the binary to `bin/sprawler` |
| `make test` | Run all tests |
| `make test-race` | Run all tests with the race detector |
| `make cover` | Run tests with per-package coverage report |
| `make vet` | Run `go vet` |
| `make fmt` | Run `gofmt` on all files |
| `make check` | Run `vet` + `fmt` and fail if `gofmt` produced changes |
| `make clean` | Remove the `bin/` directory |

Before submitting a PR, run `make check` and `make test` to catch issues early.

## Making changes

- Create a branch from `main` for your work
- Keep commits focused on a single change
- Use conventional commit prefixes (`feat:`, `fix:`, `test:`, `chore:`, `refactor:`, `docs:`)
- Run `make check` and `make test` before submitting

## Pull requests

- Open a PR against `main` with a clear description of what and why
- Keep PRs focused -- one feature or fix per PR
- Include tests for new functionality where practical
- Update documentation if your change affects configuration, output, or behavior

## Reporting issues

Open a GitHub issue with:
- What you expected to happen
- What actually happened
- Steps to reproduce (if applicable)
- Relevant log output or configuration

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
