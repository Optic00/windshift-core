# Contributing to Windshift

Thanks for your interest in contributing! See the [README](README.md) for a project overview.

> **Note:** The GitHub repository is a read-only push mirror. All development and contributions happen on [Codeberg](https://codeberg.org/realigned/windshift-core). Please fork, clone, and open pull requests there.

## Prerequisites

- **Go**: the exact version declared in `go.mod`
- **Node.js** and **npm**: the exact versions pinned by `.nvmrc` and
  `frontend/package.json`
- **Docker** (starts PostgreSQL and other services for local development)

## Development Setup

```bash
# Clone the repo
git clone https://codeberg.org/realigned/windshift-core.git && cd windshift-core/core

# Select the CI Node version and install frontend dependencies
nvm use # or configure mise/asdf from .nvmrc
cd frontend && npm ci && cd ..

# Install the pinned Go analysis tools used by CI
make dev-tools

# Install git hooks
make hooks

# Start PostgreSQL + dev server (SQLite for main app, PostgreSQL for logbook)
./dev.sh
```

The dev server runs on `localhost:7777`.

### Git Hooks

The project includes a pre-commit hook that runs the Go linter and architecture
guards, enforces fresh-schema/upgrade-migration pairing, runs Biome on
non-Svelte files, checks Svelte types, and validates OpenAPI generation. Install
it with:

```bash
make hooks
```

To bypass the hook for a quick commit, use `git commit --no-verify`.

For frontend-only development with hot reload:

```bash
cd frontend && npm run dev
```

To run the design system viewer:

```bash
cd frontend && npm run ds:dev
```

For a standalone Go build that includes test utilities:

```bash
make dev-build
```

## Project Structure

```
.
├── internal/          # Go backend
│   ├── handlers/      # HTTP request handlers
│   ├── models/        # Data models
│   ├── services/      # Business logic
│   ├── repository/    # Data access layer
│   └── database/      # Database setup and migrations
├── frontend/          # Svelte 5 / Vite / Tailwind CSS
├── cmd/ws/            # CLI client
└── .github/workflows/ # CI pipelines
```

## Making Changes

1. Create a feature branch from `main`.
2. Keep commits focused and descriptive.

## Code Style

### Go

The project uses `gofmt`, `goimports`, and `staticcheck`. Lint configuration lives in `.golangci.yml`.

```bash
make lint
```

To reproduce the blocking GitHub workflows locally, including clean dependency
installation, tests, vulnerability/signature checks, and production builds:

```bash
make ci             # both workflows
make ci-go          # Go CI only
make ci-frontend    # frontend CI only
```

### Frontend

The project uses [Biome](https://biomejs.dev/) (config: `frontend/biome.json`).

```bash
cd frontend
npm run lint        # check
npm run format      # auto-format
```
## Submitting a Pull Request

Please open all pull requests on [Codeberg](https://codeberg.org/realigned/windshift-core).

1. Push your branch and open a PR against `main`.
2. CI will run automatically:
   - **Go**: lint + unit tests
   - **Frontend**: lint, tests, bundle size check
   - **PR title lint** and **merge-conflict check**
3. Describe *what* changed and *why*. Reference related issues if applicable.

## Contributor License Agreement

By submitting a pull request you agree to the [CLA](CLA.md). The project is dual-licensed under the **AGPL v3.0** and the **Windshift Commercial License**.

## Code of Conduct

Be respectful, constructive, and collaborative.
